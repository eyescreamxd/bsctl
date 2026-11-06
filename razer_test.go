package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestRzPacket(t *testing.T) {
	// OpenRazer's battery request for wireless mice (transaction id 0x1F): crc 0x85.
	p := rzPacket(0x1F, rzGetBattery, 0, nil)
	want := []byte{0x00, 0x1F, 0, 0, 0, 0x02, 0x07, 0x80}
	if len(p) != 90 || !bytes.Equal(p[:8], want) || p[88] != 0x85 {
		t.Fatalf("battery packet: % X ... crc %02X", p[:8], p[88])
	}
	// Dock (transaction id 0xE0|n): static green, region 0, not stored.
	args := effectArgs(fxStatic, colGreen)
	p = rzPacket(0xE0, rzSetEffect, byte(len(args)), args)
	want = []byte{0x00, 0xE0, 0, 0, 0, 0x09, 0x0F, 0x02, 0x00, 0x00, 0x01, 0x00, 0x00, 0x01, 0x00, 0xFF, 0x00}
	if !bytes.Equal(p[:17], want) || p[88] != 0xFB {
		t.Fatalf("static packet: % X crc %02X", p[:17], p[88])
	}
	if a := effectArgs(fxBreathing, colRed); a[3] != 1 || a[5] != 1 {
		t.Fatalf("breathing single flags: % X", a)
	}
	if a := effectArgs(fxSpectrum); len(a) != 6 || a[2] != fxSpectrum {
		t.Fatalf("spectrum: % X", a)
	}
	r, ok := parseRz(append([]byte{2, 0x1F, 0, 0, 0, 2, 7, 0x80, 0, 225}, make([]byte, 80)...))
	if !ok || r.Status != rzStatusOK || batteryPercent(r.Args[1]) != 88 {
		t.Fatalf("parse: %+v", r)
	}
}

func TestBatteryPercent(t *testing.T) {
	// Pairs seen in Synapse logs.
	for raw, want := range map[byte]int{225: 88, 184: 72, 189: 74, 255: 100, 0: 0} {
		if got := batteryPercent(raw); got != want {
			t.Errorf("%d -> %d, want %d", raw, got, want)
		}
	}
}

func TestDockLook(t *testing.T) {
	cases := []struct {
		level    int
		charging bool
		fx       byte
		col      [3]byte
	}{
		{-1, false, fxStatic, colRed}, {100, true, fxStatic, colGreen}, {99, true, fxBreathing, colGreen},
		{75, false, fxStatic, colGreen}, {74, false, fxStatic, colYellow}, {50, false, fxStatic, colYellow},
		{49, true, fxBreathing, colOrange}, {25, false, fxStatic, colOrange}, {24, false, fxStatic, colRed}, {0, false, fxStatic, colRed},
	}
	for _, c := range cases {
		fx, col := dockLook(c.level, c.charging)
		if fx != c.fx || col != c.col {
			t.Errorf("%d%% charging=%v: got %d %v", c.level, c.charging, fx, col)
		}
	}
}

func TestDockEvent(t *testing.T) {
	ev, ok := parseDockEvent([]byte{5, 49, 225, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if !ok || ev.ID != dockEvBattery || batteryPercent(ev.Data[0]) != 88 || ev.Data[3] != 1 {
		t.Fatalf("battery event: %+v", ev)
	}
	ev, ok = parseDockEvent([]byte{5, 9, 2, 1, 0, 0})
	if !ok || ev.ID != dockEvWireless || ev.Data[0] != 2 {
		t.Fatalf("wireless event: %+v", ev)
	}
	if _, ok := parseDockEvent([]byte{2, 0, 0, 0}); ok {
		t.Fatal("media key report taken for an event")
	}
	if s := ev.String(); s == "" {
		t.Fatal("empty description")
	}
}

func TestMicNotify(t *testing.T) {
	// From bsctl.log: the mic button, muted=1 and muted=0.
	for _, c := range []struct {
		hex   string
		muted byte
	}{
		{"020E504908F7994615000400550201014911F4C758F9000400040105324911E5EAE2F80003002000", 1},
		{"020E504908F6C13E15000400550201004911F4C758F9000400040105324911E5EAE2F80003002000", 0},
	} {
		b, _ := hex.DecodeString(c.hex)
		fr := parseFrames(pad64(b))
		if len(fr) != 1 || !isNotify(fr[0]) || fr[0].Op0 != cmdMicMute || fr[0].Data[0] != c.muted {
			t.Fatalf("mic frame %s: %+v", c.hex[:20], fr)
		}
		if matchOp(cmdConnection)(fr[0]) {
			t.Fatal("mic notify matched connection")
		}
	}
	if c, _ := parseColor("#ff4500"); c != colOrange {
		t.Fatalf("parseColor: %v", c)
	}
}
