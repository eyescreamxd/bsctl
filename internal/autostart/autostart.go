// Package autostart manages the HKCU Run value that starts BlackSharkTray.exe at logon.
package autostart

import "strings"

// TrayExe is the file name of the tray build.
const TrayExe = "BlackSharkTray.exe"

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// runTarget is the exe path of a Run value: `"C:\x\BlackSharkTray.exe"` -> `C:\x\BlackSharkTray.exe`.
func runTarget(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if i := strings.Index(v[1:], `"`); i >= 0 {
			return v[1 : 1+i]
		}
		return v[1:]
	}
	return v
}

// needsHeal reports whether the Run value should be pointed at exe: only for the
// tray build and only when the value names another file.
func needsHeal(value, exe string) bool {
	return strings.EqualFold(baseName(exe), TrayExe) && !strings.EqualFold(runTarget(value), exe)
}
