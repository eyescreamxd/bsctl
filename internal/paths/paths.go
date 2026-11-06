// Package paths locates the program folder: bsctl.json, bsctl.log and the
// plugin folders live next to the exe.
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
