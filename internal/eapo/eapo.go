// Package eapo drives Equalizer APO on the headset microphone: noise suppression, mic EQ and gain.
package eapo

// Equalizer APO integration for the BlackShark microphone: noise suppression
// (a VST plugin: RNNoise or DeepFilterNet), the mic EQ and extra gain. The headset
// acknowledges its own mic EQ commands but does not change the sound, so the
// mic EQ lives here. The tray writes its
// own file bsctl.txt in the APO config folder and includes it from config.txt;
// nothing else of the user's APO setup is touched (except the stock demo config).

import (
	"fmt"
	"strings"
)

const (
	OwnFile      = "bsctl.txt"
	eapoUserFile = "user.txt"
	eapoMarker   = "BlackSharkTray (bsctl)"
	eapoDLLDir   = "bsctl" // plugins are copied to <APO config>\bsctl: audiodg cannot read the user profile
)

// NoiseConfig: settings for the Equalizer APO part.
type NoiseConfig struct {
	Enabled bool    `json:"enabled"`   // noise suppression on the BlackShark mic
	Engine  string  `json:"engine"`    // rnnoise | deepfilternet
	GainDB  float64 `json:"micGainDb"` // extra mic gain after the plugin, dB (0..12)
}

// Engine is a VST2 plugin Equalizer APO runs on the mic.
type Engine struct {
	Key, Title string
	Short      string // for the menu
	Note       string // for the settings window
	Subdir     string // next to the program: <Subdir>\<DLL>
	DLL        string
	Need48k    bool    // works only on a 48 kHz stream
	NeedVCRT   bool    // needs the Visual C++ 2015-2022 runtime in System32
	MonoGainDB float64 // the plugin is stereo and averages its inputs; APO feeds the 2nd one
	// with silence on a mono mic, so the voice would come out 6 dB quieter
}

var Engines = []Engine{
	{Key: "rnnoise", Title: "RNNoise", Short: "лёгкий, задержка ~20 мс",
		Note:   "Лёгкий, задержка ~20 мс. В паузах глушит фон полностью, но щелчки клавиатуры пропускает заметнее.",
		Subdir: "rnnoise", DLL: "rnnoise_mono.dll", Need48k: true},
	{Key: "deepfilternet", Title: "DeepFilterNet", Short: "чище, задержка ~70 мс",
		Note:   "Чище, особенно клавиатура и щелчки (плагин Alt Denoiser). Задержка ~70 мс, процессор нагружает больше.",
		Subdir: "dfn", DLL: "alt_denoiser.dll", NeedVCRT: true, MonoGainDB: 6.02},
}

// FindEngine returns the engine by key; unknown keys mean RNNoise.
func FindEngine(key string) Engine {
	for _, e := range Engines {
		if e.Key == key {
			return e
		}
	}
	return Engines[0]
}

// Mic describes the capture endpoint we process.
type Mic struct {
	GUID       string // "{...}" — the endpoint id part Equalizer APO matches
	Name       string // "Razer BlackShark V2 Pro"
	Connection string // "Microphone" / "Микрофон"
	SampleRate int    // 0 = unknown
	Format     waveFormat
	FormatFrom string // where the format came from, for the log
	RegFormat  []byte // raw registry copy of the format, for the log
	APOOnIt    bool   // Equalizer APO is installed on this endpoint
}

// micEQFreqs are the centre frequencies of the 10 mic EQ bands, Hz.
var micEQFreqs = []int{31, 63, 125, 250, 500, 1000, 2000, 4000, 8000, 16000}

// eapoOwnConfig renders bsctl.txt: noise suppression plugin, then the mic EQ
// (nil = flat, no line), then the extra gain.
func eapoOwnConfig(mic Mic, nc NoiseConfig, micEQ []int, dllPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: микрофон гарнитуры. Файл перезаписывается треем, правки делай в %s.\n", eapoMarker, eapoUserFile)
	if !nc.Enabled && nc.GainDB == 0 && micEQ == nil {
		b.WriteString("# Шумоподавление, эквалайзер и усиление выключены.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "# %s (%s), %d Гц\n", mic.Name, mic.Connection, mic.SampleRate)
	fmt.Fprintf(&b, "Device: %s\n", mic.GUID)
	b.WriteString("Stage: capture\n")
	if nc.Enabled {
		eng := FindEngine(nc.Engine)
		fmt.Fprintf(&b, "# Шумодав: %s\n", eng.Title)
		if eng.MonoGainDB != 0 && mic.Format.Channels <= 1 {
			b.WriteString("# Плагин стерео и делит моно-голос пополам, возвращаем уровень:\n")
			fmt.Fprintf(&b, "Preamp: %.2f dB\n", eng.MonoGainDB)
		}
		fmt.Fprintf(&b, "VSTPlugin: Library \"%s\"\n", dllPath)
	}
	if micEQ != nil {
		b.WriteString("# Эквалайзер микрофона\n")
		pts := make([]string, 0, len(micEQ))
		for i, v := range micEQ {
			if i < len(micEQFreqs) {
				pts = append(pts, fmt.Sprintf("%d %d", micEQFreqs[i], v))
			}
		}
		fmt.Fprintf(&b, "GraphicEQ: %s\n", strings.Join(pts, "; "))
	}
	if nc.GainDB != 0 {
		fmt.Fprintf(&b, "Preamp: %s dB\n", TrimFloat(nc.GainDB))
	}
	b.WriteString("Stage: post-mix capture\n")
	b.WriteString("Device: all\n")
	return b.String()
}

func TrimFloat(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

// eapoMainConfig is what config.txt becomes when it was the stock demo file.
func eapoMainConfig() string {
	return fmt.Sprintf("# Equalizer APO. Этот файл ведёт %s: демо-фильтры установщика убраны.\n"+
		"# Свои фильтры пиши в %s — трей его не трогает.\n"+
		"Include: %s\n"+
		"Include: %s\n", eapoMarker, eapoUserFile, OwnFile, eapoUserFile)
}

// isStockEAPOConfig: the config.txt the installer puts (−6 dB preamp, bass boost demo,
// flat GraphicEQ). Replacing it is safe and removes the demo filters.
func isStockEAPOConfig(s string) bool {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return true
	}
	if len(lines) != 3 || lines[0] != "Preamp: -6 dB" || lines[1] != "Include: example.txt" || !strings.HasPrefix(lines[2], "GraphicEQ:") {
		return false
	}
	for _, p := range strings.Split(strings.TrimPrefix(lines[2], "GraphicEQ:"), ";") {
		f := strings.Fields(p)
		if len(f) != 2 || f[1] != "0" {
			return false
		}
	}
	return true
}

// includesFile reports whether a config already includes name.
func includesFile(cfg, name string) bool {
	for _, l := range strings.Split(strings.ReplaceAll(cfg, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Include:") && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(l, "Include:")), name) {
			return true
		}
	}
	return false
}

// nextMainConfig decides what to do with config.txt: the new content and whether to write it.
func nextMainConfig(current string) (string, bool) {
	switch {
	case isStockEAPOConfig(current):
		return eapoMainConfig(), true
	case includesFile(current, OwnFile):
		return current, false
	}
	sep := "\n"
	if strings.Contains(current, "\r\n") {
		sep = "\r\n"
	}
	if current != "" && !strings.HasSuffix(current, "\n") {
		current += sep
	}
	return current + "Include: " + OwnFile + sep, true
}

// HasDemoInclude: config.txt still pulls the installer's demo filter (bass boost).
func HasDemoInclude(cfg string) bool { return includesFile(cfg, "example.txt") }

// DisableDemoInclude comments out "Include: example.txt".
func DisableDemoInclude(cfg string) string {
	lines := strings.SplitAfter(cfg, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "Include:") && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(t, "Include:")), "example.txt") {
			lines[i] = "# " + l[:len(l)-len(strings.TrimLeft(l, " \t"))] + strings.TrimLeft(l, " \t")
			lines[i] = strings.Replace(lines[i], "Include: example.txt", "Include: example.txt   (демо-фильтр установщика, отключён треем)", 1)
		}
	}
	return strings.Join(lines, "")
}

// waveFormat is the part of WAVEFORMATEX(TENSIBLE) the tray cares about.
type waveFormat struct {
	Tag, Channels, Bits, Rate int
}

func (f waveFormat) String() string {
	if f.Rate == 0 {
		return "формат неизвестен"
	}
	return fmt.Sprintf("%d Гц, %d кан., %d бит", f.Rate, f.Channels, f.Bits)
}

// parseWaveFormat finds a WAVEFORMATEX(TENSIBLE) in a property blob. The copy of
// PKEY_AudioEngine_DeviceFormat in the registry has an undocumented header in front
// of the structure, so look for a self-consistent structure at the first even offsets.
func parseWaveFormat(b []byte) (waveFormat, bool) {
	for off := 0; off+16 <= len(b) && off <= 64; off += 2 {
		u16 := func(i int) int { return int(b[off+i]) | int(b[off+i+1])<<8 }
		u32 := func(i int) int { return u16(i) | u16(i+2)<<16 }
		f := waveFormat{Tag: u16(0), Channels: u16(2), Rate: u32(4), Bits: u16(14)}
		align, avg := u16(12), u32(8)
		switch {
		case f.Tag != 1 && f.Tag != 3 && f.Tag != 0xFFFE: // PCM, IEEE float, EXTENSIBLE
		case f.Channels < 1 || f.Channels > 32 || f.Bits < 8 || f.Bits%8 != 0:
		case f.Rate < 4000 || f.Rate > 1536000:
		case align != f.Channels*f.Bits/8 || avg != f.Rate*align:
		default:
			return f, true
		}
	}
	return waveFormat{}, false
}

// HexBytes renders the first n bytes for the log.
func HexBytes(b []byte, n int) string {
	if len(b) > n {
		return fmt.Sprintf("% X …(%d байт)", b[:n], len(b))
	}
	return fmt.Sprintf("% X", b)
}
