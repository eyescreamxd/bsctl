package razer

import (
	"bytes"
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
	args := effectArgs(FxStatic, ColGreen)
	p = rzPacket(0xE0, rzSetEffect, byte(len(args)), args)
	want = []byte{0x00, 0xE0, 0, 0, 0, 0x09, 0x0F, 0x02, 0x00, 0x00, 0x01, 0x00, 0x00, 0x01, 0x00, 0xFF, 0x00}
	if !bytes.Equal(p[:17], want) || p[88] != 0xFB {
		t.Fatalf("static packet: % X crc %02X", p[:17], p[88])
	}
	if a := effectArgs(FxBreathing, ColRed); a[3] != 1 || a[5] != 1 {
		t.Fatalf("breathing single flags: % X", a)
	}
	if a := effectArgs(FxSpectrum); len(a) != 6 || a[2] != FxSpectrum {
		t.Fatalf("spectrum: % X", a)
	}
	r, ok := parseRz(append([]byte{2, 0x1F, 0, 0, 0, 2, 7, 0x80, 0, 225}, make([]byte, 80)...))
	if !ok || r.Status != rzStatusOK || BatteryPercent(r.Args[1]) != 88 {
		t.Fatalf("parse: %+v", r)
	}
}

func TestBatteryPercent(t *testing.T) {
	// Pairs seen in Synapse logs.
	for raw, want := range map[byte]int{225: 88, 184: 72, 189: 74, 255: 100, 0: 0} {
		if got := BatteryPercent(raw); got != want {
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
		{-1, false, FxStatic, ColRed}, {100, true, FxStatic, ColGreen}, {99, true, FxBreathing, ColGreen},
		{75, false, FxStatic, ColGreen}, {74, false, FxStatic, ColYellow}, {50, false, FxStatic, ColYellow},
		{49, true, FxBreathing, ColOrange}, {25, false, FxStatic, ColOrange}, {24, false, FxStatic, ColRed}, {0, false, FxStatic, ColRed},
	}
	for _, c := range cases {
		fx, col := DockLook(c.level, c.charging)
		if fx != c.fx || col != c.col {
			t.Errorf("%d%% charging=%v: got %d %v", c.level, c.charging, fx, col)
		}
	}
}

func TestDockEvent(t *testing.T) {
	ev, ok := parseDockEvent([]byte{5, 49, 225, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if !ok || ev.ID != DockEvBattery || BatteryPercent(ev.Data[0]) != 88 || ev.Data[3] != 1 {
		t.Fatalf("battery event: %+v", ev)
	}
	ev, ok = parseDockEvent([]byte{5, 9, 2, 1, 0, 0})
	if !ok || ev.ID != DockEvWireless || ev.Data[0] != 2 {
		t.Fatalf("wireless event: %+v", ev)
	}
	if _, ok := parseDockEvent([]byte{2, 0, 0, 0}); ok {
		t.Fatal("media key report taken for an event")
	}
	if s := ev.String(); s == "" {
		t.Fatal("empty description")
	}
}

func TestParseColor(t *testing.T) {
	if c, _ := ParseColor("#ff4500"); c != ColOrange {
		t.Fatalf("ParseColor: %v", c)
	}
}
