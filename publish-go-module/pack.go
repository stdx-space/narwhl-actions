package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

// Release is one module version, packed from a tag and ready to publish.
type Release struct {
	Module module.Version // packages.narwhl.dev/go/authz@v0.1.0
	Dir    string         // go/authz: go.mod directory and tag prefix
	Tag    string         // go/authz/v0.1.0
	Commit string         // full hash of the tagged commit
	Time   time.Time      // committer time of the tagged commit
	GoMod  []byte         // go.mod as committed
	Zip    []byte         // module zip, as golang.org/x/mod/zip creates it
	Hash   string         // h1: hash of the zip, as go.sum and sum.golang.org record it
}

// ParseTag splits a module release tag "go/authz/v0.1.0" into the module
// directory "go/authz" and version "v0.1.0".
func ParseTag(tag string) (dir, version string, err error) {
	i := strings.LastIndex(tag, "/")
	if i <= 0 {
		return "", "", fmt.Errorf("tag %q: want <module directory>/<version>, such as go/authz/v0.1.0", tag)
	}
	dir, version = tag[:i], tag[i+1:]
	if path.Clean(dir) != dir || strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "../") {
		return "", "", fmt.Errorf("tag %q: %q is not a clean relative directory", tag, dir)
	}
	if !semver.IsValid(version) || semver.Canonical(version) != version {
		return "", "", fmt.Errorf("tag %q: %q is not a canonical semantic version such as v1.2.3", tag, version)
	}
	if semver.Build(version) != "" {
		return "", "", fmt.Errorf("tag %q: build metadata (%s) is not allowed in module versions", tag, semver.Build(version))
	}
	if module.IsPseudoVersion(version) {
		return "", "", fmt.Errorf("tag %q: %s is a pseudo-version", tag, version)
	}
	return dir, version, nil
}

// ExpectedPath is the module path a go.mod in dir must declare to be
// published at version: <host>/<dir>, plus /vN from v2 on.
func ExpectedPath(host, dir, version string) string {
	p := host + "/" + dir
	if major := semver.Major(version); major != "v0" && major != "v1" {
		p += "/" + major
	}
	return p
}

// Pack builds the release for tag from the git repository at repoRoot. It
// reads only committed content of the tagged commit, never the working tree.
func Pack(ctx context.Context, repoRoot, tag, host string) (*Release, error) {
	dir, version, err := ParseTag(tag)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}

	commit, err := git(ctx, root, "rev-parse", "--verify", "--end-of-options", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("tag %s not found in %s: %w", tag, root, err)
	}
	commit = strings.TrimSpace(commit)

	goMod, err := git(ctx, root, "show", commit+":"+dir+"/go.mod")
	if err != nil {
		return nil, fmt.Errorf("%s/go.mod at %s: %w", dir, tag, err)
	}
	f, err := modfile.ParseLax(dir+"/go.mod", []byte(goMod), nil)
	if err != nil {
		return nil, err
	}
	if f.Module == nil {
		return nil, fmt.Errorf("%s/go.mod has no module directive", dir)
	}
	modPath := f.Module.Mod.Path
	if want := ExpectedPath(host, dir, version); modPath != want {
		return nil, fmt.Errorf("%s/go.mod declares module %s, but tag %s publishes %s", dir, modPath, tag, want)
	}
	m := module.Version{Path: modPath, Version: version}
	if err := module.Check(m.Path, m.Version); err != nil {
		return nil, err
	}

	committed, err := git(ctx, root, "show", "-s", "--format=%cI", commit)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(committed))
	if err != nil {
		return nil, fmt.Errorf("commit time of %s: %w", commit, err)
	}

	var zip bytes.Buffer
	if err := modzip.CreateFromVCS(&zip, m, root, commit, dir); err != nil {
		return nil, err
	}
	hash, err := checkZip(m, zip.Bytes())
	if err != nil {
		return nil, err
	}

	return &Release{
		Module: m,
		Dir:    dir,
		Tag:    tag,
		Commit: commit,
		Time:   t.UTC(),
		GoMod:  []byte(goMod),
		Zip:    zip.Bytes(),
		Hash:   hash,
	}, nil
}

// checkZip runs the go command's own validation over the zip and returns its
// h1: hash.
func checkZip(m module.Version, zip []byte) (string, error) {
	tmp, err := os.CreateTemp("", "publish-go-module-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(zip); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	cf, err := modzip.CheckZip(m, tmp.Name())
	if err != nil {
		return "", err
	}
	if err := cf.Err(); err != nil {
		return "", fmt.Errorf("module zip for %s is invalid: %w", m, err)
	}
	return dirhash.HashZip(tmp.Name(), dirhash.Hash1)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return string(out), nil
}
