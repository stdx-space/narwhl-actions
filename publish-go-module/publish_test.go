package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeProxy stores what is PUT and serves it back like the registry Worker at pkg.narwhl.dev.
type fakeProxy struct {
	mu      sync.Mutex
	files   map[string][]byte
	headers http.Header
	status  int
}

func (p *fakeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		p.headers = r.Header.Clone()
		if p.status != 0 {
			w.WriteHeader(p.status)
			io.WriteString(w, `{"error":"already published from commit bbbb"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		base := strings.TrimSuffix(r.URL.Path, ".zip")
		p.files[base+".zip"] = body
		p.files[base+".mod"] = []byte("module " + host + "/authz\n\ngo 1.25.0\n")
		p.files[base[:strings.LastIndex(base, "/")]+"/list"] = []byte("v0.1.0\n")
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		if b, ok := p.files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}
}

func TestUploadAndVerify(t *testing.T) {
	r, err := Pack(context.Background(), testRepo(t), "go/authz/v0.1.0", host, "go")
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeProxy{files: map[string][]byte{}}
	srv := httptest.NewServer(fp)
	defer srv.Close()

	created, err := Upload(context.Background(), srv.Client(), srv.URL, r, "tok")
	if err != nil || !created {
		t.Fatalf("Upload = %v, %v", created, err)
	}
	if got := fp.headers.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := fp.headers.Get("Module-Time"); got != "2026-11-04T12:30:00Z" {
		t.Errorf("Module-Time = %q", got)
	}
	if _, ok := fp.files["/"+host+"/authz/@v/v0.1.0.zip"]; !ok {
		t.Errorf("zip not stored at the GOPROXY path; have %v", keys(fp.files))
	}
	if err := Verify(context.Background(), srv.Client(), srv.URL, r); err != nil {
		t.Errorf("Verify: %v", err)
	}

	// A proxy serving other code for the version fails verification.
	fp.files["/"+host+"/authz/@v/v0.1.0.zip"] = testZip(t)
	if err := Verify(context.Background(), srv.Client(), srv.URL, r); err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Errorf("Verify with a different zip = %v, want a hash mismatch", err)
	}
}

func TestUploadConflict(t *testing.T) {
	r, err := Pack(context.Background(), testRepo(t), "go/authz/v0.1.0", host, "go")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&fakeProxy{files: map[string][]byte{}, status: http.StatusConflict})
	defer srv.Close()
	_, err = Upload(context.Background(), srv.Client(), srv.URL, r, "tok")
	if err == nil || !strings.Contains(err.Error(), "already published from commit bbbb") {
		t.Errorf("Upload error = %v", err)
	}
}

func TestOIDCToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "bearer runner-secret" || r.URL.Query().Get("audience") != "https://"+host {
			http.Error(w, "bad request", http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"value":"jwt"}`)
	}))
	defer srv.Close()

	t.Setenv("REGISTRY_TOKEN", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", srv.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-secret")
	got, err := OIDCToken(context.Background(), srv.Client(), "https://"+host)
	if err != nil || got != "jwt" {
		t.Errorf("OIDCToken = %q, %v", got, err)
	}

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	if _, err := OIDCToken(context.Background(), srv.Client(), "x"); err == nil || !strings.Contains(err.Error(), "id-token: write") {
		t.Errorf("OIDCToken outside Actions = %v", err)
	}
	t.Setenv("REGISTRY_TOKEN", "manual")
	if got, _ := OIDCToken(context.Background(), srv.Client(), "x"); got != "manual" {
		t.Errorf("OIDCToken with REGISTRY_TOKEN = %q", got)
	}
}

func testZip(t *testing.T) []byte {
	r, err := Pack(context.Background(), testRepoWith(t, "package authz // changed\n"), "go/authz/v0.1.0", host, "go")
	if err != nil {
		t.Fatal(err)
	}
	return r.Zip
}

func testRepoWith(t *testing.T, authzGo string) string {
	repo := testRepo(t)
	run(t, repo, "sh", "-c", "printf '%s' \"$0\" > go/authz/authz.go && git add go/authz/authz.go && "+
		"git -c user.name=t -c user.email=t@example.com commit -q -m change && git tag -f go/authz/v0.1.0 >/dev/null", authzGo)
	return repo
}

func keys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
