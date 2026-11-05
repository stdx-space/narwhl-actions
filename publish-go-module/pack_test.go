package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const host = "packages.narwhl.dev"

func TestParseTag(t *testing.T) {
	for _, tc := range []struct {
		tag, dir, version string
	}{
		{"go/authz/v0.1.0", "go/authz", "v0.1.0"},
		{"go/authn/v1.2.3-rc.1", "go/authn", "v1.2.3-rc.1"},
		{"go/authz/v2.0.0", "go/authz", "v2.0.0"},
	} {
		dir, version, err := ParseTag(tc.tag)
		if err != nil || dir != tc.dir || version != tc.version {
			t.Errorf("ParseTag(%q) = %q, %q, %v; want %q, %q", tc.tag, dir, version, err, tc.dir, tc.version)
		}
	}
	for _, tag := range []string{
		"v0.1.0",
		"go/authz/0.1.0",
		"go/authz/v0.1",
		"go/authz/v0.1.0+build",
		"go/authz/v0.0.0-20261104152400-abcdef123456",
		"go/../authz/v0.1.0",
		"/go/authz/v0.1.0",
		"go//authz/v0.1.0",
	} {
		if _, _, err := ParseTag(tag); err == nil {
			t.Errorf("ParseTag(%q) succeeded, want error", tag)
		}
	}
}

func TestExpectedPath(t *testing.T) {
	for version, want := range map[string]string{
		"v0.1.0": host + "/go/authz",
		"v1.9.0": host + "/go/authz",
		"v2.0.0": host + "/go/authz/v2",
		"v3.1.0": host + "/go/authz/v3",
	} {
		if got := ExpectedPath(host, "go/authz", version); got != want {
			t.Errorf("ExpectedPath(%s) = %s, want %s", version, got, want)
		}
	}
}

// testRepo creates a git repository laid out like narwhl/library.
func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go/go.work", "go 1.25.0\n\nuse ./authz\n")
	write("go/authz/go.mod", "module "+host+"/go/authz\n\ngo 1.25.0\n")
	write("go/authz/authz.go", "package authz\n")
	write("go/authz/cmd/fgagen/main.go", "package main\n\nfunc main() {}\n")
	write("go/authz/fga/go.mod", "module "+host+"/go/authz/fga\n") // nested module: excluded
	write("go/authz/fga/fga.go", "package fga\n")
	write("go/authn/go.mod", "module github.com/narwhl/library/go/authn\n") // wrong path
	write("README.md", "library\n")

	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	run(t, dir, "git", "tag", "go/authz/v0.1.0")
	run(t, dir, "git", "tag", "go/authz/v2.0.0")
	run(t, dir, "git", "tag", "go/authn/v0.1.0")
	run(t, dir, "git", "tag", "go/missing/v0.1.0")
	write("go/authz/untracked.go", "package authz\n") // never committed: excluded
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-11-04T20:30:00+08:00", "GIT_AUTHOR_DATE=2026-11-04T20:30:00+08:00")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func TestPack(t *testing.T) {
	repo := testRepo(t)
	r, err := Pack(context.Background(), repo, "go/authz/v0.1.0", host)
	if err != nil {
		t.Fatal(err)
	}
	if r.Module.Path != host+"/go/authz" || r.Module.Version != "v0.1.0" || r.Dir != "go/authz" {
		t.Errorf("module = %v in %s", r.Module, r.Dir)
	}
	if want := time.Date(2026, 11, 4, 12, 30, 0, 0, time.UTC); !r.Time.Equal(want) || r.Time.Location() != time.UTC {
		t.Errorf("time = %v, want %v", r.Time, want)
	}
	if len(r.Commit) != 40 || !strings.HasPrefix(r.Hash, "h1:") {
		t.Errorf("commit = %q, hash = %q", r.Commit, r.Hash)
	}
	if string(r.GoMod) != "module "+host+"/go/authz\n\ngo 1.25.0\n" {
		t.Errorf("go.mod = %q", r.GoMod)
	}

	zr, err := zip.NewReader(bytes.NewReader(r.Zip), int64(len(r.Zip)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	prefix := host + "/go/authz@v0.1.0/"
	want := []string{prefix + "authz.go", prefix + "cmd/fgagen/main.go", prefix + "go.mod"}
	if !slices.Equal(names, want) {
		t.Errorf("zip files:\n got %q\nwant %q", names, want)
	}
}

func TestPackRejects(t *testing.T) {
	repo := testRepo(t)
	for tag, want := range map[string]string{
		"go/authz/v2.0.0":   "publishes " + host + "/go/authz/v2",
		"go/authn/v0.1.0":   "declares module github.com/narwhl/library/go/authn",
		"go/missing/v0.1.0": "go/missing/go.mod",
		"go/authz/v9.9.9":   "not found",
	} {
		_, err := Pack(context.Background(), repo, tag, host)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Pack(%s) error = %v, want it to mention %q", tag, err, want)
		}
	}
}
