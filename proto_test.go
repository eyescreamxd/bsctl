package main

import (
	"bytes"
	"testing"
)

// Packets copied from Synapse logs (trailing zeros trimmed).
func TestBuildPacketMatchesSynapse(t *testing.T) {
	cases := []struct {
		name   string
		target byte
		block  []byte
		want   []byte
	}{
		{"remote", targetDongle, remoteBlock(true), []byte{2, 128, 7, 0, 0, 80, 65, 14, 0, 2, 225, 1}},
		{"connection", targetHeadset, getBlock(cmdConnection), []byte{2, 128, 8, 0, 0, 80, 65, 8, 0, 3, 32}},
		{"battery", targetHeadset, getBlock(cmdBattery), []byte{2, 128, 8, 0, 0, 80, 65, 8, 0, 3, 33}},
		{"mic preset", targetHeadset, setBlock(cmdMicPreset, 3), []byte{2, 128, 9, 0, 0, 80, 65, 8, 0, 4, 150, 0, 1, 3}},
		{"preset index", targetHeadset, setBlock(cmdPresetIndex, 250), []byte{2, 128, 9, 0, 0, 80, 65, 8, 0, 4, 147, 0, 1, 250}},
		{"eq group", targetHeadset, setBlock(cmdEQGroup, 2), []byte{2, 128, 9, 0, 0, 80, 65, 8, 0, 4, 157, 0, 1, 2}},
		{"firmware", targetHeadset, firmwareBlock(), []byte{2, 128, 11, 0, 0, 80, 65, 8, 0, 6, 1, 194, 3, 248, 95, 4}},
		{"custom eq csgo", targetHeadset, customEQBlock([]int{-5, -4, 3, 5, 3, -2, 1, 5, 4, -4}),
			[]byte{2, 128, 18, 0, 0, 80, 65, 8, 0, 13, 149, 0, 10, 251, 252, 3, 5, 3, 254, 1, 5, 4, 252}},
	}
	for _, c := range cases {
		got := buildPacket(c.target, c.block)
		if len(got) != 64 {
			t.Fatalf("%s: len %d", c.name, len(got))
		}
		if !bytes.Equal(trimZeros(got), c.want) {
			t.Errorf("%s:\n got  %v\n want %v", c.name, trimZeros(got), c.want)
		}
	}
}

func pad64(b []byte) []byte { p := make([]byte, 64); copy(p, b); return p }

func TestParseResponses(t *testing.T) {
	// Battery 59%, with stale garbage after the frame.
	bat := pad64([]byte{2, 14, 80, 73, 8, 207, 245, 31, 0, 0, 4, 0, 33, 1, 1, 59, 73, 1, 192, 30, 116, 165, 3, 3, 0, 17, 141, 23, 50, 80, 73, 1, 192})
	r, ok := parseResponse(bat)
	if !ok || !matchOp(cmdBattery)(r) || len(r.Data) != 1 || r.Data[0] != 59 {
		t.Fatalf("battery: ok=%v r=%+v", ok, r)
	}
	// Serial number.
	ser := pad64([]byte{2, 28, 80, 73, 8, 229, 254, 41, 0, 0, 18, 0, 0, 1, 15, 65, 67, 50, 51, 51, 55, 68, 50, 55, 52, 48, 51, 53, 49, 48, 73, 1})
	r, ok = parseResponse(ser)
	if !ok || !matchOp(cmdSerial)(r) || string(r.Data) != "AC2337D27403510" {
		t.Fatalf("serial: ok=%v r=%+v", ok, r)
	}
	// Dongle firmware 2.4.1.0 must not be taken for "edition".
	fw := pad64([]byte{2, 17, 80, 73, 8, 247, 127, 136, 165, 3, 7, 0, 1, 194, 4, 2, 4, 1, 0, 51, 55})
	r, ok = parseResponse(fw)
	if !ok || !matchFirmware(r) || matchOp(cmdEdition)(r) || !bytes.Equal(r.Data, []byte{2, 4, 1, 0}) {
		t.Fatalf("fw: ok=%v r=%+v", ok, r)
	}
	// Edition answer from the dongle.
	ed := pad64([]byte{2, 14, 80, 73, 8, 246, 74, 136, 165, 3, 4, 0, 1, 1, 1, 0})
	r, ok = parseResponse(ed)
	if !ok || !matchOp(cmdEdition)(r) || matchFirmware(r) {
		t.Fatalf("edition: ok=%v r=%+v", ok, r)
	}
	// A consumer-key event (Volume-) must not be taken for the connection answer (both use 0x20).
	ev := pad64([]byte{2, 13, 80, 73, 0x11, 0xC0, 0xBF, 0xDB, 0xF8, 0, 3, 0, 0x20, 0xEA, 0})
	r, ok = parseResponse(ev)
	if !ok || matchOp(cmdConnection)(r) {
		t.Fatalf("event matched as answer: ok=%v r=%+v", ok, r)
	}
	conn := pad64([]byte{2, 14, 80, 73, 8, 0xC8, 0xD5, 0x86, 9, 0, 4, 0, 0x20, 1, 1, 1})
	r, ok = parseResponse(conn)
	if !ok || !matchOp(cmdConnection)(r) || r.Data[0] != 1 {
		t.Fatalf("connection answer: ok=%v r=%+v", ok, r)
	}
	// Several frames in one report: two events, then our battery answer.
	multi := pad64([]byte{2, 40,
		80, 73, 1, 0xC0, 0, 0xDC, 0xF8, 0, 3, 0, 0x11, 0xFB, 0x17,
		80, 73, 0x11, 0xC7, 0, 0xDC, 0xF8, 0, 3, 0, 0x20, 0xEA, 0,
		80, 73, 8, 0xCF, 0xF5, 0x1F, 0, 0, 4, 0, 33, 1, 1, 61,
		0x49, 0x11})
	fr := parseFrames(multi)
	if len(fr) != 3 || !matchOp(cmdBattery)(fr[2]) || fr[2].Data[0] != 61 || matchOp(cmdConnection)(fr[1]) {
		t.Fatalf("multi-frame: %d frames", len(fr))
	}
	// Garbage / event reports are rejected.
	for _, g := range [][]byte{pad64([]byte{2, 70, 1, 2}), pad64([]byte{1, 14, 80, 73}), pad64([]byte{2, 14, 73, 1, 192})} {
		if _, ok := parseResponse(g); ok {
			t.Errorf("garbage accepted: %v", g[:6])
		}
	}
}

func TestBands(t *testing.T) {
	if _, err := parseBands("0,0,0,0,0,0,0,0,0,0"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseBands("0,0,0"); err == nil {
		t.Fatal("short list accepted")
	}
	if _, err := parseBands("0,0,0,0,0,0,0,0,0,13"); err == nil {
		t.Fatal("out of range accepted")
	}
	for _, p := range presets {
		if p.Bands != nil {
			if err := validateBands(p.Bands); err != nil {
				t.Errorf("%s: %v", p.Key, err)
			}
		}
	}
}

func TestIcon(t *testing.T) {
	for _, ico := range [][]byte{headsetIcon(devLook{Level: 57, Online: true, Muted: true}), mouseIcon(devLook{Level: -1})} {
		if len(ico) != 6+16+40+32*32*4+32*32/8 {
			t.Fatalf("ico size %d", len(ico))
		}
	}
}

func TestSidetoneScale(t *testing.T) {
	// Synapse: floor(p/100*15) to the headset, ceil(v/15*100) back.
	for p, want := range map[int]byte{0: 0, 25: 3, 50: 7, 70: 10, 75: 11, 100: 15, 150: 15, -5: 0} {
		if got := sidetoneRaw(p); got != want {
			t.Errorf("sidetoneRaw(%d) = %d, want %d", p, got, want)
		}
	}
	for v, want := range map[byte]int{0: 0, 7: 47, 15: 100, 10: 67} {
		if got := sidetonePercent(v); got != want {
			t.Errorf("sidetonePercent(%d) = %d, want %d", v, got, want)
		}
	}
	got := trimZeros(buildPacket(targetHeadset, setBlock(cmdSidetoneVol, sidetoneRaw(50))))
	want := []byte{2, 128, 9, 0, 0, 80, 65, 8, 0, 4, 153, 0, 1, 7}
	if !bytes.Equal(got, want) {
		t.Fatalf("sidetone volume packet %v, want %v", got, want)
	}
}
