package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Config lives next to the exe as bsctl.json. Defaults are the user's Synapse settings.
type Config struct {
	Preset          string `json:"preset"`              // custom | game | movie | music | apex | csgo | valorant | fortnite | cod
	CustomEQ        []int  `json:"customEQ"`            // 10 bands, dB -12..12: 31,63,125,250,500,1k,2k,4k,8k,16k
	MicPreset       int    `json:"micPreset"`           // mic EQ preset in the headset: 0 default, 1 boost, 2 broadcast, 3 conference, 255 custom
	MicCustomEQ     []int  `json:"micCustomEQ"`         // 10 bands for micPreset 255, dB -12..12
	Sidetone        bool   `json:"sidetone"`            // hear yourself
	SidetoneLevel   int    `json:"sidetoneLevel"`       // sidetone volume, % (Synapse default 50)
	SidetoneMicOpen bool   `json:"sidetoneKeepMicOpen"` // record the mic while sidetone is on (like Synapse), else it is silent
	AutoPowerOffMin int    `json:"autoPowerOffMinutes"` // 0 = never
	RemoteMode      bool   `json:"remoteMode"`          // put the dongle in PC-controlled mode before commands
	PollSeconds     int    `json:"pollSeconds"`         // tray: how often to check the headset
	ReapplyMinutes  int    `json:"reapplyMinutes"`      // also resend the settings every N minutes, 0 = only when needed
	DevicePath      string `json:"devicePath,omitempty"`
	UseSetOutput    bool   `json:"useSetOutputReport,omitempty"`

	Noise NoiseConfig `json:"noiseSuppression"` // Equalizer APO: noise suppression and gain for the BlackShark mic

	Mouse MouseConfig `json:"mouse"`

	SettingsWindow *WindowRect `json:"settingsWindow,omitempty"` // where the settings window was last
}

// WindowRect is a window position in screen pixels.
type WindowRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// MouseConfig: Basilisk V3 Pro and Mouse Dock Pro.
type MouseConfig struct {
	Enabled        bool `json:"enabled"`             // second tray icon with the mouse battery
	PollSeconds    int  `json:"pollSeconds"`         // how often to ask the mouse battery
	DockLighting   bool `json:"dockLighting"`        // dock light shows the mouse battery, like Synapse
	DockBrightness int  `json:"dockBrightness"`      // dock brightness, %
	DockRedWhenOff bool `json:"dockRedWhenMouseOff"` // Synapse paints the dock red when the mouse is off/asleep; default keeps the last color
}

func defaultConfig() *Config {
	return &Config{
		Preset:          "custom",
		CustomEQ:        []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		MicPreset:       3,
		MicCustomEQ:     []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		Sidetone:        false,
		SidetoneLevel:   50,
		SidetoneMicOpen: true,
		AutoPowerOffMin: 0,
		RemoteMode:      true,
		PollSeconds:     5,
		Noise:           NoiseConfig{Engine: "rnnoise"},
		Mouse: MouseConfig{
			Enabled:        true,
			PollSeconds:    30,
			DockLighting:   true,
			DockBrightness: 30,
		},
	}
}

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Dir(p)
}

func configPath() string { return filepath.Join(exeDir(), "bsctl.json") }
func logPath() string    { return filepath.Join(exeDir(), "bsctl.log") }

func loadConfig() (*Config, error) {
	c := defaultConfig()
	b, err := os.ReadFile(configPath())
	if os.IsNotExist(err) {
		return c, saveConfig(c)
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return defaultConfig(), fmt.Errorf("bsctl.json: %v", err)
	}
	err = c.validate()
	if err == nil && (!bytes.Contains(b, []byte(`"mouse"`)) || !bytes.Contains(b, []byte(`"engine"`))) {
		_ = saveConfig(c) // show the new options in the file
	}
	return c, err
}

func (c *Config) validate() error {
	if _, ok := findPreset(c.Preset); !ok {
		return fmt.Errorf("неизвестный пресет %q", c.Preset)
	}
	if err := validateBands(c.CustomEQ); err != nil {
		return err
	}
	if len(c.MicCustomEQ) == 0 {
		c.MicCustomEQ = make([]int, 10)
	}
	if err := validateBands(c.MicCustomEQ); err != nil {
		return fmt.Errorf("micCustomEQ: %v", err)
	}
	if _, ok := findMicPreset(c.MicPreset); !ok {
		return fmt.Errorf("micPreset %d: бывает 0, 1, 2, 3 или 255", c.MicPreset)
	}
	if c.MicPreset < 0 || c.MicPreset > 255 || c.AutoPowerOffMin < 0 || c.AutoPowerOffMin > 255 {
		return fmt.Errorf("micPreset и autoPowerOffMinutes должны быть 0..255")
	}
	if c.PollSeconds < 2 {
		c.PollSeconds = 2
	}
	c.SidetoneLevel = max(0, min(100, c.SidetoneLevel))
	c.Noise.GainDB = max(-12, min(20, c.Noise.GainDB))
	c.Noise.Engine = findEngine(c.Noise.Engine).Key
	if w := c.SettingsWindow; w != nil && (w.W < 200 || w.H < 150 || w.W > 20000 || w.H > 20000) {
		c.SettingsWindow = nil
	}
	if c.ReapplyMinutes < 0 {
		c.ReapplyMinutes = 0
	}
	if c.Mouse.PollSeconds < 10 {
		c.Mouse.PollSeconds = 10
	}
	c.Mouse.DockBrightness = max(0, min(100, c.Mouse.DockBrightness))
	return nil
}

func saveConfig(c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

func configModTime() time.Time {
	st, err := os.Stat(configPath())
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// effectiveEQ returns group, index and bands for the selected preset.
func (c *Config) effectiveEQ() (byte, byte, []int) {
	p, ok := findPreset(c.Preset)
	if !ok {
		p, _ = findPreset("custom")
	}
	bands := p.Bands
	if bands == nil {
		bands = c.CustomEQ
	}
	return p.Group, p.Index, bands
}

// micEQ returns the mic EQ curve Equalizer APO applies: the preset curve or the
// custom bands; nil when the curve is flat.
func (c *Config) micEQ() []int {
	bands := c.MicCustomEQ
	if p, ok := findMicPreset(c.MicPreset); ok && p.Bands != nil {
		bands = p.Bands
	}
	for _, v := range bands {
		if v != 0 {
			return bands
		}
	}
	return nil
}

func newLogger(alsoStdout bool) *log.Logger {
	path := logPath()
	if st, err := os.Stat(path); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(path, path+".1")
	}
	var w io.Writer = io.Discard
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		w = f
	}
	if alsoStdout {
		w = io.MultiWriter(w, os.Stdout)
	}
	return log.New(w, "", log.LstdFlags|log.Lmicroseconds)
}
