// Package ui serves the settings page to the Edge app window.
package ui

// Settings window: a tiny local web server (127.0.0.1, random port, random token)
// that serves one page; the tray opens it as an Edge "app" window. All logic stays
// in the tray, the page only shows state and sends commands.

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/eyescreamxd/bsctl/internal/eapo"
	"github.com/eyescreamxd/bsctl/internal/headset"
	"github.com/eyescreamxd/bsctl/internal/version"
)

//go:embed web/index.html
var webFS embed.FS

// SettingsTitle is the <title> of the page; the tray finds the Edge window by it.
const SettingsTitle = "BlackShark — настройки"

type Preset struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Index int    `json:"index"`
	Bands []int  `json:"bands"`
}

type State struct {
	Version string `json:"version"`
	Headset struct {
		Connected bool   `json:"connected"`
		Battery   int    `json:"battery"`
		Charging  bool   `json:"charging"`
		Mic       int    `json:"mic"` // -1 unknown, 0 on, 1 muted
		Status    string `json:"status"`
	} `json:"headset"`
	EQ struct {
		Preset  string   `json:"preset"`
		Bands   []int    `json:"bands"`
		Custom  []int    `json:"custom"`
		Presets []Preset `json:"presets"`
	} `json:"eq"`
	Mic struct {
		Preset  int      `json:"preset"`
		Bands   []int    `json:"bands"`
		Custom  []int    `json:"custom"`
		Presets []Preset `json:"presets"`
		Note    string   `json:"note"`
	} `json:"mic"`
	Sidetone struct {
		On    bool `json:"on"`
		Level int  `json:"level"`
	} `json:"sidetone"`
	Noise struct {
		Enabled     bool     `json:"enabled"`
		Engine      string   `json:"engine"`
		Engines     []Engine `json:"engines"`
		GainDB      float64  `json:"gainDb"`
		Installed   bool     `json:"installed"`
		Active      bool     `json:"active"`
		Problem     string   `json:"problem"`
		MicName     string   `json:"micName"`
		SampleRate  int      `json:"sampleRate"`
		DemoInclude bool     `json:"demoInclude"`
	} `json:"noise"`
	Mouse struct {
		Enabled    bool   `json:"enabled"`
		Status     string `json:"status"`
		Dock       string `json:"dock"`
		Lighting   bool   `json:"lighting"`
		Brightness int    `json:"brightness"`
	} `json:"mouse"`
	Bands []string `json:"bandNames"`
}

type Engine struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

type Request struct {
	Op         string   `json:"op"`
	Key        string   `json:"key,omitempty"`
	Engine     string   `json:"engine,omitempty"`
	Index      int      `json:"index,omitempty"`
	Bands      []int    `json:"bands,omitempty"`
	On         *bool    `json:"on,omitempty"`
	Level      *int     `json:"level,omitempty"`
	GainDB     *float64 `json:"gainDb,omitempty"`
	Brightness *int     `json:"brightness,omitempty"`
}

type Backend interface {
	State() State
	Do(r Request) error
}

type Server struct {
	backend Backend
	token   string
	addr    string
	srv     *http.Server
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start listens on a random local port.
func Start(b Backend) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{backend: b, token: newToken(), addr: ln.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/do", s.do)
	s.srv = &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *Server) URL() string { return "http://" + s.addr + "/?t=" + s.token }

// guard: only our own host name (no DNS rebinding) and only with the token.
func (s *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.addr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr && r.Host != "localhost:"+port {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		tok := r.Header.Get("X-Token")
		if tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if tok != s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.backend.State())
}

func (s *Server) do(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "POST application/json", http.StatusMethodNotAllowed)
		return
	}
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := s.backend.Do(req); err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(s.backend.State())
}

// FillPresets puts the static preset lists into a state.
func (st *State) FillPresets() {
	st.Version = version.Version
	st.Bands = headset.BandNames
	for _, p := range headset.Presets {
		st.EQ.Presets = append(st.EQ.Presets, Preset{Key: p.Key, Title: p.Title, Bands: p.Bands})
	}
	for _, p := range headset.MicPresets {
		st.Mic.Presets = append(st.Mic.Presets, Preset{Key: p.Key, Title: p.Title, Index: int(p.Index), Bands: p.Bands})
	}
	for _, e := range eapo.Engines {
		st.Noise.Engines = append(st.Noise.Engines, Engine{Key: e.Key, Title: e.Title, Note: e.Note})
	}
}

// CheckBands validates a request's 10 bands.
func CheckBands(b []int) error {
	if len(b) != 10 {
		return fmt.Errorf("нужно 10 полос, пришло %d", len(b))
	}
	return headset.ValidateBands(b)
}

var ErrUnknownOp = errors.New("неизвестная команда")
