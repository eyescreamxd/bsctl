//go:build windows

package autostart

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/eyescreamxd/bsctl/internal/paths"
)

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "BlackSharkTray"
)

func trayExePath() string {
	p, _ := os.Executable()
	if !strings.EqualFold(baseName(p), TrayExe) {
		p = paths.ExeDir() + `\` + TrayExe
	}
	return p
}

// Set adds or removes the Run value for the tray.
func Set(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValue, `"`+trayExePath()+`"`)
	}
	if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// Enabled reports whether the Run value exists.
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}
