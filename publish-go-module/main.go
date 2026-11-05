// Command publish-go-module packs the Go module named by a release tag and
// publishes it to a module proxy such as packages.narwhl.dev. It is also the
// narwhl/actions/publish-go-module action; see README.md.
//
// A tag go/authz/v0.1.0 publishes the module whose go.mod is go/authz/go.mod,
// at version v0.1.0. That go.mod must declare packages.narwhl.dev/go/authz
// (packages.narwhl.dev/go/authz/v2 for v2.x.y tags, and so on).
//
//	publish-go-module -repo ../library -tag go/authz/v0.1.0            # pack, upload, verify
//	publish-go-module -repo ../library -tag go/authz/v0.1.0 -out dist  # pack into a GOPROXY tree only
//
// In GitHub Actions the tag defaults to the pushed tag and the upload is
// authorized with the job's OIDC token (needs `permissions: id-token: write`).
// Elsewhere, set REGISTRY_TOKEN.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"golang.org/x/mod/module"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("publish-go-module: ")

	tag := flag.String("tag", defaultTag(), "release tag, <module directory>/<version>")
	repo := flag.String("repo", ".", "root of the git repository holding the tag")
	host := flag.String("host", "packages.narwhl.dev", "host part of the module paths")
	proxy := flag.String("proxy", "", "module proxy to publish to (default https://<host>)")
	audience := flag.String("audience", "", "OIDC token audience (default https://<host>)")
	mirror := flag.String("mirror", "https://proxy.golang.org", "public mirror to warm after publishing; empty to skip")
	out := flag.String("out", "", "write the .zip, .mod and .info into this GOPROXY directory instead of uploading")
	flag.Parse()

	if *tag == "" {
		log.Fatal("no tag: pass -tag, or run on a tag push in GitHub Actions")
	}
	if *proxy == "" {
		*proxy = "https://" + *host
	}
	if *audience == "" {
		*audience = "https://" + *host
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := &http.Client{Timeout: 2 * time.Minute}

	r, err := Pack(ctx, *repo, *tag, *host)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("packed %s from %s (%s, %d bytes, %s)", r.Module, r.Tag, r.Commit[:12], len(r.Zip), r.Hash)

	if *out != "" {
		if err := writeTree(*out, r); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s into %s", r.Module, *out)
		return
	}

	token, err := OIDCToken(ctx, client, *audience)
	if err != nil {
		log.Fatal(err)
	}
	created, err := Upload(ctx, client, *proxy, r, token)
	if err != nil {
		log.Fatal(err)
	}
	if created {
		log.Printf("published %s to %s", r.Module, *proxy)
	} else {
		log.Printf("%s was already published from this commit", r.Module)
	}
	if err := Verify(ctx, client, *proxy, r); err != nil {
		log.Fatal(err)
	}
	log.Printf("verified %s through %s", r.Module, *proxy)

	if *mirror != "" {
		// Best effort: the mirror fetches from the proxy on its own schedule too.
		if err := Warm(ctx, client, *mirror, r.Module); err != nil {
			log.Printf("warning: %s does not have %s yet: %v", *mirror, r.Module, err)
		} else {
			log.Printf("%s has %s", *mirror, r.Module)
		}
	}
	if err := summarize(r, created); err != nil {
		log.Printf("warning: writing job summary: %v", err)
	}
}

// defaultTag is the pushed tag when running in GitHub Actions.
func defaultTag() string {
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		return os.Getenv("GITHUB_REF_NAME")
	}
	return ""
}

// writeTree lays the release out as a GOPROXY directory, usable as
// GOPROXY=file:///abs/path/to/dir.
func writeTree(dir string, r *Release) error {
	p, err := module.EscapePath(r.Module.Path)
	if err != nil {
		return err
	}
	v, err := module.EscapeVersion(r.Module.Version)
	if err != nil {
		return err
	}
	base := filepath.Join(dir, filepath.FromSlash(p), "@v")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	info, err := json.Marshal(struct {
		Version string
		Time    string
	}{r.Module.Version, r.Time.Format(time.RFC3339)})
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{v + ".zip": r.Zip, v + ".mod": r.GoMod, v + ".info": info} {
		if err := os.WriteFile(filepath.Join(base, name), data, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(base, "list"), []byte(r.Module.Version+"\n"), 0o644)
}

// summarize appends the release to the GitHub Actions job summary, if any.
func summarize(r *Release, created bool) error {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	status := "published"
	if !created {
		status = "already published (same commit)"
	}
	_, err = fmt.Fprintf(f, "### %s\n\n| | |\n|---|---|\n| Status | %s |\n| Commit | `%s` |\n| go.sum | `%s %s %s` |\n\n```sh\ngo get %s\n```\n",
		r.Module, status, r.Commit, r.Module.Path, r.Module.Version, r.Hash, r.Module)
	return err
}
