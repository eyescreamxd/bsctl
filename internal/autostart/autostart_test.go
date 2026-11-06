package autostart

import "testing"

func TestNeedsHeal(t *testing.T) {
	exe := `C:\bsctl\dist\BlackSharkTray.exe`
	cases := []struct {
		value, exe string
		want       bool
	}{
		{`"C:\bsctl\BlackSharkTray.exe"`, exe, true},               // old place in the repo root
		{`"C:\bsctl\dist\BlackSharkTray.exe"`, exe, false},         // already right
		{`"c:\BSCTL\dist\blacksharktray.exe"`, exe, false},         // case does not matter on Windows
		{`C:\bsctl\dist\BlackSharkTray.exe`, exe, false},           // without quotes
		{`"C:\bsctl\BlackSharkTray.exe"`, `C:\x\bsctl.exe`, false}, // only the tray build heals
	}
	for _, c := range cases {
		if got := needsHeal(c.value, c.exe); got != c.want {
			t.Errorf("needsHeal(%s, %s) = %v, want %v", c.value, c.exe, got, c.want)
		}
	}
}
