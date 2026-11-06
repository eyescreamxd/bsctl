//go:build windows

package tray

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/eyescreamxd/bsctl/internal/config"
	"github.com/eyescreamxd/bsctl/internal/eapo"
	"github.com/eyescreamxd/bsctl/internal/headset"
	"github.com/eyescreamxd/bsctl/internal/ui"
)

// trayUI connects the settings page with the tray workers.
type trayUI struct {
	a   *trayApp
	win settingsWindow

	mu      sync.Mutex
	eapo    eapo.Status
	eapoAt  time.Time
	eapoErr string
}

// openSettings shows the settings window: brings the open one to the front, or opens
// a new Edge app window at the place where it was last time.
func (a *trayApp) openSettings() {
	a.uiOnce.Do(func() {
		srv, err := ui.Start(a.ui)
		if err != nil {
			a.lg.Printf("окно настроек: %v", err)
			return
		}
		a.uiSrv = srv
	})
	if a.uiSrv == nil {
		return
	}
	if a.ui.win.focus() || a.ui.win.starting() {
		return
	}
	stale := findSettingsWindows() // left from an earlier tray run
	rect := a.config().SettingsWindow
	if edge := edgePath(); edge != "" {
		args := []string{"--app=" + a.uiSrv.URL()}
		if rect == nil {
			args = append(args, "--window-size=960,1200")
		}
		if err := exec.Command(edge, args...).Start(); err == nil {
			a.ui.win.launched()
			go a.ui.win.track(a, rect, stale)
			return
		} else {
			a.lg.Printf("Edge: %v", err)
		}
	}
	verb, _ := windows.UTF16PtrFromString("open")
	u, _ := windows.UTF16PtrFromString(a.uiSrv.URL())
	_ = windows.ShellExecute(0, verb, u, nil, nil, windows.SW_SHOWNORMAL)
}

// setNoise is the tray menu switch: off, or on with an engine.
func (a *trayApp) setNoise(on bool, engine string) {
	c := a.updateConfig(func(c *config.Config) {
		c.Noise.Enabled = on
		if engine != "" {
			c.Noise.Engine = engine
		}
	})
	_ = a.ui.applyAPO(c)
}

func edgePath() string {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`, registry.QUERY_VALUE); err == nil {
		p, _, _ := k.GetStringValue("")
		k.Close()
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		p := filepath.Join(os.Getenv(env), `Microsoft\Edge\Application\msedge.exe`)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// eapoState returns the Equalizer APO status, refreshed at most every few seconds.
func (u *trayUI) eapoState() (eapo.Status, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Since(u.eapoAt) > 4*time.Second {
		u.eapo = eapo.Detect()
		u.eapoAt = time.Now()
	}
	return u.eapo, u.eapoErr
}

func (u *trayUI) State() ui.State {
	a := u.a
	var st ui.State
	st.FillPresets()
	cfg := a.config()

	w := a.hs
	w.mu.Lock()
	st.Headset.Connected, st.Headset.Battery, st.Headset.Charging = w.connected, w.battery, w.charging
	st.Headset.Mic, st.Headset.Status = w.mic, w.status
	w.mu.Unlock()

	st.EQ.Preset, st.EQ.Custom = cfg.Preset, cfg.CustomEQ
	_, _, st.EQ.Bands = cfg.EffectiveEQ()
	st.Mic.Preset, st.Mic.Custom = cfg.MicPreset, cfg.MicCustomEQ
	st.Mic.Bands = cfg.MicCustomEQ
	if p, ok := headset.FindMicPreset(cfg.MicPreset); ok && p.Bands != nil {
		st.Mic.Bands = p.Bands
	}
	st.Sidetone.On, st.Sidetone.Level = cfg.Sidetone, cfg.SidetoneLevel

	es, eerr := u.eapoState()
	st.Noise.Enabled, st.Noise.GainDB = cfg.Noise.Enabled, cfg.Noise.GainDB
	st.Noise.Engine = eapo.FindEngine(cfg.Noise.Engine).Key
	st.Noise.Installed = es.Installed
	if es.Mic != nil {
		st.Noise.MicName, st.Noise.SampleRate = es.Mic.Name, es.Mic.SampleRate
	}
	st.Noise.Problem = es.ProblemFor(cfg.Noise)
	if st.Noise.Problem == "" && eerr != "" {
		st.Noise.Problem = eerr
	}
	st.Noise.Active = cfg.Noise.Enabled && st.Noise.Problem == ""
	if p := es.ProblemFor(eapo.NoiseConfig{}); p != "" && cfg.MicEQ() != nil {
		st.Mic.Note = "Эквалайзер микрофона не работает: " + p + "."
	}
	if es.Installed {
		if b, err := os.ReadFile(filepath.Join(es.ConfigDir, "config.txt")); err == nil {
			st.Noise.DemoInclude = eapo.HasDemoInclude(string(b))
		}
	}

	st.Mouse.Enabled = cfg.Mouse.Enabled
	st.Mouse.Lighting, st.Mouse.Brightness = cfg.Mouse.DockLighting, cfg.Mouse.DockBrightness
	if m := a.ms; m != nil {
		m.mu.Lock()
		st.Mouse.Status, st.Mouse.Dock = m.status, m.dockLine
		m.mu.Unlock()
	}
	return st
}

func (u *trayUI) Do(r ui.Request) error {
	a := u.a
	switch r.Op {
	case "eq.preset":
		if _, ok := headset.FindPreset(r.Key); !ok {
			return fmt.Errorf("нет пресета %q", r.Key)
		}
		a.updateConfig(func(c *config.Config) { c.Preset = r.Key })
		send(a.hs.reqs, "eq")
	case "eq.bands":
		if err := ui.CheckBands(r.Bands); err != nil {
			return err
		}
		a.updateConfig(func(c *config.Config) { c.Preset, c.CustomEQ = "custom", append([]int(nil), r.Bands...) })
		send(a.hs.reqs, "eq")
	case "mic.preset":
		if _, ok := headset.FindMicPreset(r.Index); !ok {
			return fmt.Errorf("нет пресета микрофона %d", r.Index)
		}
		c := a.updateConfig(func(c *config.Config) { c.MicPreset = r.Index })
		return u.applyAPO(c)
	case "mic.bands":
		if err := ui.CheckBands(r.Bands); err != nil {
			return err
		}
		c := a.updateConfig(func(c *config.Config) {
			c.MicPreset, c.MicCustomEQ = headset.MicCustomIndex, append([]int(nil), r.Bands...)
		})
		return u.applyAPO(c)
	case "sidetone":
		a.updateConfig(func(c *config.Config) {
			if r.On != nil {
				c.Sidetone = *r.On
			}
			if r.Level != nil {
				c.SidetoneLevel = max(0, min(100, *r.Level))
				c.Sidetone = true
			}
		})
		send(a.hs.reqs, "sidetone-cfg")
	case "noise":
		if r.Engine != "" && eapo.FindEngine(r.Engine).Key != r.Engine {
			return fmt.Errorf("нет шумодава %q", r.Engine)
		}
		c := a.updateConfig(func(c *config.Config) {
			if r.On != nil {
				c.Noise.Enabled = *r.On
			}
			if r.Engine != "" {
				c.Noise.Engine = r.Engine
			}
			if r.GainDB != nil {
				c.Noise.GainDB = max(0, min(20, *r.GainDB))
			}
		})
		return u.applyAPO(c)
	case "noise.demoOff":
		es, _ := u.eapoState()
		if !es.Installed {
			return fmt.Errorf("Equalizer APO не найден")
		}
		p := filepath.Join(es.ConfigDir, "config.txt")
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(eapo.DisableDemoInclude(string(b))), 0o644); err != nil {
			return fmt.Errorf("запись config.txt: %w", err)
		}
		a.lg.Printf("Equalizer APO: демо-фильтр example.txt отключён в config.txt")
	case "dock":
		if a.ms == nil {
			return fmt.Errorf("мышь выключена в настройках (mouse.enabled)")
		}
		if r.On != nil {
			if *r.On != a.config().Mouse.DockLighting {
				send(a.ms.reqs, "dock-toggle")
			}
		}
		if r.Brightness != nil {
			send(a.ms.reqs, fmt.Sprintf("bright:%d", max(0, min(100, *r.Brightness))))
		}
	case "apply":
		send(a.hs.reqs, "apply")
	default:
		return ui.ErrUnknownOp
	}
	return nil
}

// applyAPO writes the Equalizer APO files (noise suppression, mic EQ, gain) and
// remembers the outcome.
func (u *trayUI) applyAPO(c config.Config) error {
	nc := c.Noise
	st, err := eapo.Apply(nc, c.MicEQ(), u.a.lg.Printf)
	u.mu.Lock()
	u.eapo, u.eapoAt = st, time.Now()
	u.eapoErr = ""
	if err != nil {
		u.eapoErr = err.Error()
	}
	u.mu.Unlock()
	if err != nil {
		u.a.lg.Printf("Equalizer APO: %v", err)
		return err
	}
	if p := st.ProblemFor(nc); p != "" && nc.Enabled {
		u.a.lg.Printf("шумодав %s: %s", eapo.FindEngine(nc.Engine).Title, p)
	}
	return nil
}

// keepNoise re-checks the APO files once a minute: the APO Editor rewrites
// config.txt as a whole and may drop our Include line.
func (a *trayApp) keepNoise() {
	for {
		c := a.config()
		if c.Noise.Enabled || c.Noise.GainDB != 0 || c.MicEQ() != nil {
			if _, err := eapo.Apply(c.Noise, c.MicEQ(), a.lg.Printf); err != nil {
				a.lg.Printf("Equalizer APO: %v", err)
			}
		}
		time.Sleep(time.Minute)
	}
}

// micFormatHint is a short status for the log at startup.
func micFormatHint() string {
	st := eapo.Detect()
	if st.Mic == nil {
		return "микрофон BlackShark не найден"
	}
	m := st.Mic
	return fmt.Sprintf("%s (%s), %s [%s], Equalizer APO на нём: %v; формат в реестре: %s",
		m.Name, strings.TrimSpace(m.Connection), m.Format, m.FormatFrom, m.APOOnIt, eapo.HexBytes(m.RegFormat, 24))
}
