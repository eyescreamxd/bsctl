package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyescreamxd/bsctl/internal/config"
	"github.com/eyescreamxd/bsctl/internal/eapo"
	"github.com/eyescreamxd/bsctl/internal/headset"
)

type fakeBackend struct {
	mu  sync.Mutex
	cfg config.Config
	ops []string
}

func (f *fakeBackend) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	var st State
	st.FillPresets()
	st.Headset.Connected, st.Headset.Battery, st.Headset.Mic = true, 85, 1
	st.EQ.Preset, st.EQ.Custom = f.cfg.Preset, f.cfg.CustomEQ
	_, _, st.EQ.Bands = f.cfg.EffectiveEQ()
	st.Mic.Preset, st.Mic.Custom = f.cfg.MicPreset, f.cfg.MicCustomEQ
	if p, ok := headset.FindMicPreset(f.cfg.MicPreset); ok && p.Bands != nil {
		st.Mic.Bands = p.Bands
	} else {
		st.Mic.Bands = f.cfg.MicCustomEQ
	}
	st.Sidetone.On, st.Sidetone.Level = f.cfg.Sidetone, f.cfg.SidetoneLevel
	st.Noise.Enabled, st.Noise.GainDB, st.Noise.Installed = f.cfg.Noise.Enabled, f.cfg.Noise.GainDB, true
	st.Noise.Engine = eapo.FindEngine(f.cfg.Noise.Engine).Key
	st.Noise.MicName, st.Noise.SampleRate, st.Noise.DemoInclude = "Razer BlackShark V2 Pro", 48000, true
	st.Noise.Active = f.cfg.Noise.Enabled
	st.Mouse.Enabled, st.Mouse.Status, st.Mouse.Dock = true, "Basilisk V3 Pro: 84%", "Док: цвет #00FF00"
	st.Mouse.Lighting, st.Mouse.Brightness = f.cfg.Mouse.DockLighting, f.cfg.Mouse.DockBrightness
	return st
}

func (f *fakeBackend) Do(r Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, r.Op)
	switch r.Op {
	case "eq.preset":
		f.cfg.Preset = r.Key
	case "eq.bands":
		if err := CheckBands(r.Bands); err != nil {
			return err
		}
		f.cfg.Preset, f.cfg.CustomEQ = "custom", r.Bands
	case "mic.preset":
		f.cfg.MicPreset = r.Index
	case "mic.bands":
		f.cfg.MicPreset, f.cfg.MicCustomEQ = 255, r.Bands
	case "noise":
		if r.On != nil {
			f.cfg.Noise.Enabled = *r.On
		}
		if r.Engine != "" {
			f.cfg.Noise.Engine = r.Engine
		}
	default:
		return ErrUnknownOp
	}
	return nil
}

func TestUIServer(t *testing.T) {
	fb := &fakeBackend{cfg: *config.Default()}
	s, err := Start(fb)
	if err != nil {
		t.Fatal(err)
	}
	if secs, _ := strconv.Atoi(os.Getenv("UI_PREVIEW_SECONDS")); secs > 0 {
		fmt.Println("UI:", s.URL())
		time.Sleep(time.Duration(secs) * time.Second)
		return
	}
	base := "http://" + s.addr
	get := func(url string, hdr map[string]string) int {
		req, _ := http.NewRequest("GET", url, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := get(base+"/", nil); c != 403 {
		t.Errorf("page without token: %d", c)
	}
	if c := get(s.URL(), nil); c != 200 {
		t.Errorf("page with token: %d", c)
	}
	if c := get(base+"/api/state", map[string]string{"X-Token": s.token}); c != 200 {
		t.Errorf("state: %d", c)
	}
	// DNS rebinding: a foreign Host header is refused even with the token.
	req, _ := http.NewRequest("GET", base+"/api/state", nil)
	req.Host = "evil.example:80"
	req.Header.Set("X-Token", s.token)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("foreign host: %d", r.StatusCode)
	}
	post := func(body any) (int, State) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+"/api/do", bytes.NewReader(b))
		req.Header.Set("X-Token", s.token)
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var st State
		_ = json.NewDecoder(r.Body).Decode(&st)
		return r.StatusCode, st
	}
	c, st := post(map[string]any{"op": "eq.bands", "bands": []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}})
	if c != 200 || st.EQ.Preset != "custom" || st.EQ.Bands[9] != 10 {
		t.Errorf("eq.bands: %d %+v", c, st.EQ)
	}
	if c, _ := post(map[string]any{"op": "eq.bands", "bands": []int{13, 0, 0, 0, 0, 0, 0, 0, 0, 0}}); c != 422 {
		t.Errorf("out of range accepted: %d", c)
	}
	if c, _ := post(map[string]any{"op": "nope"}); c != 422 {
		t.Errorf("unknown op: %d", c)
	}
	if c, st := post(map[string]any{"op": "noise", "on": true}); c != 200 || !st.Noise.Enabled {
		t.Errorf("noise: %d", c)
	}
}

func TestSettingsTitle(t *testing.T) {
	b, _ := webFS.ReadFile("web/index.html")
	if !strings.Contains(string(b), "<title>"+SettingsTitle+"</title>") {
		t.Fatalf("web/index.html <title> must be %q: the tray finds the window by it", SettingsTitle)
	}
}
