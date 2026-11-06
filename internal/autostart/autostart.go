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
