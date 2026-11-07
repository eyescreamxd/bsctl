// Package update finds a newer release on GitHub and downloads its installer
// with a SHA-256 check. The repository is public, so no token is needed.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// LatestURL is the GitHub API endpoint of the latest release.
const LatestURL = "https://api.github.com/repos/eyescreamxd/bsctl/releases/latest"

const maxInstaller = 100 << 20 // refuse anything bigger than 100 MB

// Release is the part of the GitHub release JSON the tray needs.
type Release struct {
	Tag    string  `json:"tag_name"`
	Page   string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset is a file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Version is the tag without the leading "v".
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Installer returns the download links of bsctl-setup-<version>.exe and its .sha256 file.
func (r Release) Installer() (setup, sum string, ok bool) {
	name := "bsctl-setup-" + r.Version() + ".exe"
	for _, a := range r.Assets {
		switch a.Name {
		case name:
			setup = a.URL
		case name + ".sha256":
			sum = a.URL
		}
	}
	return setup, sum, setup != "" && sum != ""
}

// Newer reports whether version a (x.y.z, optional "v") is newer than b.
// Unparsable versions are never newer.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ParseSum takes the hash from a sha256sum-style line ("<hex>  <name>").
func ParseSum(b []byte) (string, error) {
	f := strings.Fields(string(b))
	if len(f) == 0 || len(f[0]) != 64 {
		return "", errors.New("файл контрольной суммы испорчен")
	}
	if _, err := hex.DecodeString(f[0]); err != nil {
		return "", errors.New("файл контрольной суммы испорчен")
	}
	return strings.ToLower(f[0]), nil
}

// Client talks to GitHub; UserAgent is required by the GitHub API.
type Client struct {
	HTTP      *http.Client
	UserAgent string
}

func (c Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub ответил %s", resp.Status)
	}
	return resp, nil
}

// Latest fetches the latest release from url (LatestURL in the program).
func (c Client) Latest(ctx context.Context, url string) (Release, error) {
	var r Release
	resp, err := c.get(ctx, url)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return r, fmt.Errorf("ответ GitHub: %w", err)
	}
	return r, nil
}

// Download saves the installer of r to dst and checks it against the published hash.
// On any error dst is removed.
func (c Client) Download(ctx context.Context, r Release, dst string) (err error) {
	setupURL, sumURL, ok := r.Installer()
	if !ok {
		return fmt.Errorf("в релизе %s нет установщика", r.Tag)
	}
	resp, err := c.get(ctx, sumURL)
	if err != nil {
		return err
	}
	sumText, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if err != nil {
		return err
	}
	want, err := ParseSum(sumText)
	if err != nil {
		return err
	}
	resp, err = c.get(ctx, setupURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxInstaller+1))
	if err != nil {
		return err
	}
	if n > maxInstaller {
		return errors.New("установщик слишком большой")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return errors.New("контрольная сумма установщика не совпала")
	}
	return nil
}
