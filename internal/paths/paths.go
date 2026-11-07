// Package paths locates the program folders: the exe folder (plugins) and the
// state folder (bsctl.json, bsctl.log).
package paths

import (
	"os"
	"path/filepath"
)

// ExeDir is the folder of the running exe ("." if unknown).
func ExeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Dir(p)
}

// Portable reports whether bsctl.json lies next to the exe (a build in dist):
// then the state stays there.
func Portable() bool {
	_, err := os.Stat(filepath.Join(ExeDir(), "bsctl.json"))
	return err == nil
}

// StateDir is the folder of bsctl.json and bsctl.log: next to the exe in portable
// mode, otherwise %LOCALAPPDATA%\bsctl (the installed copy; the install folder
// is replaced by updates).
func StateDir() string {
	return stateDir(ExeDir(), Portable(), os.Getenv("LOCALAPPDATA"))
}

func stateDir(exeDir string, portable bool, localAppData string) string {
	if portable || localAppData == "" {
		return exeDir
	}
	d := filepath.Join(localAppData, "bsctl")
	_ = os.MkdirAll(d, 0o755)
	return d
}
