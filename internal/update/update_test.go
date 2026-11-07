package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.8.0", "0.7.1", true},
		{"0.7.10", "0.7.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.7.1", "0.7.1", false},
		{"v0.7.0", "0.7.1", false},
		{"v0.8.0-rc1", "0.7.1", false},
		{"garbage", "0.7.1", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestParseSum(t *testing.T) {
	h := "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if got, err := ParseSum([]byte(h + "  bsctl-setup-0.8.0.exe\n")); err != nil || got != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Errorf("got %q %v", got, err)
	}
	for _, bad := range []string{"", "abc", "zz" + h[2:]} {
		if _, err := ParseSum([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// fakeGitHub serves one release with an installer; sum overrides the published hash.
func fakeGitHub(t *testing.T, body []byte, sum string) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if sum == "" {
		s := sha256.Sum256(body)
		sum = hex.EncodeToString(s[:])
	}
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no UA", http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v0.8.0","html_url":"x","assets":[
			{"name":"bsctl-setup-0.8.0.exe","browser_download_url":"%[1]s/setup"},
			{"name":"bsctl-setup-0.8.0.exe.sha256","browser_download_url":"%[1]s/sum"}]}`, srv.URL)
	})
	mux.HandleFunc("/setup", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/sum", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "%s  bsctl-setup-0.8.0.exe\n", sum) })
	return srv, srv.URL + "/latest"
}

func TestLatestAndDownload(t *testing.T) {
	body := []byte("MZ fake installer")
	srv, latest := fakeGitHub(t, body, "")
	c := Client{HTTP: srv.Client(), UserAgent: "bsctl-test"}
	r, err := c.Latest(context.Background(), latest)
	if err != nil || r.Version() != "0.8.0" {
		t.Fatalf("latest: %+v %v", r, err)
	}
	dst := filepath.Join(t.TempDir(), "setup.exe")
	if err := c.Download(context.Background(), r, dst); err != nil {
		t.Fatalf("download: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(body) {
		t.Errorf("content: %q", b)
	}
}

func TestDownloadBadSum(t *testing.T) {
	srv, latest := fakeGitHub(t, []byte("MZ tampered"), "0000000000000000000000000000000000000000000000000000000000000000")
	c := Client{HTTP: srv.Client(), UserAgent: "bsctl-test"}
	r, err := c.Latest(context.Background(), latest)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "setup.exe")
	if err := c.Download(context.Background(), r, dst); err == nil {
		t.Fatal("bad hash accepted")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("file with a bad hash left on disk")
	}
}

func TestInstallerMissing(t *testing.T) {
	r := Release{Tag: "v0.8.0", Assets: []Asset{{Name: "bsctl-setup-0.8.0.exe", URL: "x"}}}
	if _, _, ok := r.Installer(); ok {
		t.Error("installer without .sha256 accepted")
	}
}
