//go:build windows

package autostart

import (
	"log"
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

// Heal points an existing Run value at the running BlackSharkTray.exe when it names
// another file (the exe was moved, e.g. into dist). No value: autostart stays off.
func Heal(lg *log.Logger) {
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(baseName(exe), TrayExe) {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	if err != nil || !needsHeal(v, exe) {
		return
	}
	if err := k.SetStringValue(runValue, `"`+exe+`"`); err != nil {
		lg.Printf("автозапуск: не удалось обновить путь: %v", err)
		return
	}
	lg.Printf("автозапуск: путь обновлён на %s (был %s)", exe, v)
}
