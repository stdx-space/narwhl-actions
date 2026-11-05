package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
)

// OIDCToken returns the bearer token for the proxy. In GitHub Actions it asks
// the runner for an OIDC token with the given audience; elsewhere it uses
// $REGISTRY_TOKEN.
func OIDCToken(ctx context.Context, client *http.Client, audience string) (string, error) {
	if t := os.Getenv("REGISTRY_TOKEN"); t != "" {
		return t, nil
	}
	reqURL, reqToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if reqURL == "" || reqToken == "" {
		return "", errors.New("no OIDC token: run in GitHub Actions with `permissions: id-token: write`, or set REGISTRY_TOKEN")
	}
	u, err := url.Parse(reqURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "bearer "+reqToken)
	var body struct{ Value string }
	if err := do(client, req, http.StatusOK, &body); err != nil {
		return "", fmt.Errorf("requesting GitHub OIDC token: %w", err)
	}
	return body.Value, nil
}

// Upload PUTs the release zip to the proxy. It reports whether the version was
// newly published (false: the proxy already had it from the same commit).
func Upload(ctx context.Context, client *http.Client, proxy string, r *Release, token string) (bool, error) {
	u, err := fileURL(proxy, r.Module, ".zip")
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(r.Zip))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("Module-Time", r.Time.Format(time.RFC3339))
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusCreated:
		return true, nil
	case http.StatusOK:
		return false, nil
	}
	var e struct{ Error string }
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return false, fmt.Errorf("publishing %s: %s: %s", r.Module, resp.Status, e.Error)
	}
	return false, fmt.Errorf("publishing %s: %s: %s", r.Module, resp.Status, strings.TrimSpace(string(body)))
}

// Verify downloads the release back through the proxy, as the go command
// would, and checks the list, go.mod and zip hash.
func Verify(ctx context.Context, client *http.Client, proxy string, r *Release) error {
	m := r.Module
	list, err := get(ctx, client, proxy, m, "list")
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Fields(string(list)), m.Version) {
		return fmt.Errorf("%s/@v/list does not include %s", m.Path, m.Version)
	}
	mod, err := get(ctx, client, proxy, m, ".mod")
	if err != nil {
		return err
	}
	if !bytes.Equal(mod, r.GoMod) {
		return fmt.Errorf("%s.mod served by the proxy differs from the tagged go.mod", m)
	}
	zip, err := get(ctx, client, proxy, m, ".zip")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "publish-go-module-verify-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(zip); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	hash, err := dirhash.HashZip(tmp.Name(), dirhash.Hash1)
	if err != nil {
		return err
	}
	if hash != r.Hash {
		// h1: covers file names and contents, not zip metadata, so a re-run for
		// the same commit hashes the same. A mismatch means the proxy serves
		// code other than this tag's.
		return fmt.Errorf("%s.zip served by the proxy hashes to %s, the tag to %s", m, hash, r.Hash)
	}
	return nil
}

// Warm asks a public module mirror (proxy.golang.org) for the new version, so
// it is cached, recorded in sum.golang.org and indexed by pkg.go.dev now
// rather than on first use.
func Warm(ctx context.Context, client *http.Client, mirror string, m module.Version) error {
	_, err := get(ctx, client, mirror, m, ".info")
	return err
}

func get(ctx context.Context, client *http.Client, base string, m module.Version, file string) ([]byte, error) {
	var u string
	var err error
	if file == "list" {
		u, err = listURL(base, m.Path)
	} else {
		u, err = fileURL(base, m, file)
	}
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var body []byte
	if err := do(client, req, http.StatusOK, &body); err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	return body, nil
}

func listURL(base, modPath string) (string, error) {
	p, err := module.EscapePath(modPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(base, "/") + "/" + p + "/@v/list", nil
}

func fileURL(base string, m module.Version, ext string) (string, error) {
	p, err := module.EscapePath(m.Path)
	if err != nil {
		return "", err
	}
	v, err := module.EscapeVersion(m.Version)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(base, "/") + "/" + p + "/@v/" + v + ext, nil
}

// do sends req and decodes a JSON body into out, or copies raw bytes into a
// *[]byte.
func do(client *http.Client, req *http.Request, want int, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body[:min(len(body), 512)])))
	}
	if b, ok := out.(*[]byte); ok {
		*b = body
		return nil
	}
	return json.Unmarshal(body, out)
}
