package eapo

import (
	"strings"
	"testing"
)

const stockEAPO = "Preamp: -6 dB\r\nInclude: example.txt\r\nGraphicEQ: 25 0; 40 0; 63 0; 100 0; 160 0; 250 0; 400 0; 630 0; 1000 0; 1600 0; 2500 0; 4000 0; 6300 0; 10000 0; 16000 0"

func TestEAPOMainConfig(t *testing.T) {
	if !isStockEAPOConfig(stockEAPO) {
		t.Fatal("stock config not recognized")
	}
	next, write := nextMainConfig(stockEAPO)
	if !write || !includesFile(next, OwnFile) || includesFile(next, "example.txt") {
		t.Fatalf("stock replacement: %q", next)
	}
	// The user changed the preamp: keep the file, only add our include (CRLF kept).
	user := strings.Replace(stockEAPO, "-6 dB", "0 dB", 1)
	if isStockEAPOConfig(user) {
		t.Fatal("edited config taken for stock")
	}
	next, write = nextMainConfig(user)
	if !write || !strings.HasPrefix(next, user+"\r\n") || !strings.HasSuffix(next, "Include: bsctl.txt\r\n") {
		t.Fatalf("append: %q", next)
	}
	if _, write := nextMainConfig(next); write {
		t.Fatal("include added twice")
	}
	if !HasDemoInclude(next) {
		t.Fatal("demo include not seen")
	}
	off := DisableDemoInclude(next)
	if HasDemoInclude(off) || !strings.Contains(off, "# Include: example.txt") || !includesFile(off, OwnFile) {
		t.Fatalf("disable demo: %q", off)
	}
}

func TestEAPOOwnConfig(t *testing.T) {
	mic := Mic{GUID: "{a1b2}", Name: "Razer BlackShark V2 Pro", Connection: "Микрофон", SampleRate: 48000}
	s := eapoOwnConfig(mic, NoiseConfig{Enabled: true, GainDB: 6}, nil, `C:\Program Files\EqualizerAPO\config\bsctl\rnnoise_mono.dll`)
	for _, want := range []string{"Device: {a1b2}\n", "Stage: capture\n", `VSTPlugin: Library "C:\Program Files\EqualizerAPO\config\bsctl\rnnoise_mono.dll"` + "\n", "Preamp: 6 dB\n", "Stage: post-mix capture\nDevice: all\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if s := eapoOwnConfig(mic, NoiseConfig{}, nil, "x"); strings.Contains(s, "Device:") || strings.Contains(s, "VSTPlugin") {
		t.Errorf("disabled config has commands:\n%s", s)
	}
	if s := eapoOwnConfig(mic, NoiseConfig{GainDB: 3.5}, nil, "x"); strings.Contains(s, "VSTPlugin") || !strings.Contains(s, "Preamp: 3.5 dB") {
		t.Errorf("gain only:\n%s", s)
	}
	if strings.Contains(s, "Preamp: 6.02") {
		t.Errorf("RNNoise must not get the mono compensation:\n%s", s)
	}
}

func TestEAPOOwnConfigDeepFilterNet(t *testing.T) {
	mono := Mic{GUID: "{a1b2}", Name: "Razer BlackShark V2 Pro", SampleRate: 48000, Format: waveFormat{0xFFFE, 1, 16, 48000}}
	s := eapoOwnConfig(mono, NoiseConfig{Enabled: true, Engine: "deepfilternet", GainDB: 3}, nil, `C:\x\alt_denoiser.dll`)
	// order matters: compensation before the plugin, user gain after it
	i, j, k := strings.Index(s, "Preamp: 6.02 dB\n"), strings.Index(s, `VSTPlugin: Library "C:\x\alt_denoiser.dll"`), strings.Index(s, "Preamp: 3 dB\n")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("bad DeepFilterNet config:\n%s", s)
	}
	stereo := mono
	stereo.Format.Channels = 2
	if s := eapoOwnConfig(stereo, NoiseConfig{Enabled: true, Engine: "deepfilternet"}, nil, "x"); strings.Contains(s, "6.02") {
		t.Errorf("stereo mic needs no compensation:\n%s", s)
	}
	if e := FindEngine("nope"); e.Key != "rnnoise" {
		t.Errorf("unknown engine -> %q", e.Key)
	}
}

func TestParseWaveFormat(t *testing.T) {
	// WAVEFORMATEXTENSIBLE, 1 channel, 16 bit, 48000 Hz, front center, PCM subformat.
	ext := []byte{0xFE, 0xFF, 1, 0, 0x80, 0xBB, 0, 0, 0x00, 0x77, 0x01, 0, 2, 0, 16, 0, 22, 0, 16, 0, 4, 0, 0, 0,
		1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71}
	// WAVEFORMATEX, 2 channels, 32-bit float, 44100 Hz.
	flt := []byte{3, 0, 2, 0, 0x44, 0xAC, 0, 0, 0x20, 0x62, 0x05, 0, 8, 0, 32, 0, 0, 0}
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name string
		blob []byte
		want waveFormat
		ok   bool
	}{
		{"bare extensible", ext, waveFormat{0xFFFE, 1, 16, 48000}, true},
		{"VT_BLOB + size header", cat([]byte{0x41, 0, 0, 0, 40, 0, 0, 0}, ext), waveFormat{0xFFFE, 1, 16, 48000}, true},
		// The user's machine: bytes 4..7 hold 1, the structure comes later.
		{"header with 1 at offset 4", cat([]byte{0x41, 0, 0, 0, 1, 0, 0, 0, 40, 0, 0, 0}, ext), waveFormat{0xFFFE, 1, 16, 48000}, true},
		{"float stereo", cat([]byte{0x41, 0, 0, 0, 18, 0, 0, 0}, flt), waveFormat{3, 2, 32, 44100}, true},
		{"garbage", []byte{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0}, waveFormat{}, false},
		{"too short", []byte{0xFE, 0xFF, 1, 0, 0x80, 0xBB}, waveFormat{}, false},
	}
	for _, c := range cases {
		got, ok := parseWaveFormat(c.blob)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
	if s := (waveFormat{0xFFFE, 1, 16, 48000}).String(); s != "48000 Гц, 1 кан., 16 бит" {
		t.Errorf("String: %q", s)
	}
}

func TestEAPOOwnConfigMicEQ(t *testing.T) {
	mic := Mic{GUID: "{a1b2}", Name: "Razer BlackShark V2 Pro", SampleRate: 48000}
	eq := []int{-8, -7, -5, -3, -1, 1, 3, 2, 1, 0}
	// EQ alone is enough to target the mic
	s := eapoOwnConfig(mic, NoiseConfig{}, eq, "x")
	want := "GraphicEQ: 31 -8; 63 -7; 125 -5; 250 -3; 500 -1; 1000 1; 2000 3; 4000 2; 8000 1; 16000 0\n"
	if !strings.Contains(s, "Device: {a1b2}\n") || !strings.Contains(s, want) {
		t.Errorf("EQ only:\n%s", s)
	}
	// order: plugin, then EQ, then the user gain
	s = eapoOwnConfig(mic, NoiseConfig{Enabled: true, GainDB: 4}, eq, "x")
	i, j, k := strings.Index(s, "VSTPlugin:"), strings.Index(s, "GraphicEQ:"), strings.Index(s, "Preamp: 4 dB")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("bad order:\n%s", s)
	}
}
