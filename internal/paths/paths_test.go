package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDir(t *testing.T) {
	exe, local := t.TempDir(), t.TempDir()
	if d := stateDir(exe, true, local); d != exe {
		t.Errorf("portable: %s", d)
	}
	if d := stateDir(exe, false, ""); d != exe {
		t.Errorf("no LOCALAPPDATA: %s", d)
	}
	want := filepath.Join(local, "bsctl")
	if d := stateDir(exe, false, local); d != want {
		t.Errorf("installed: %s, want %s", d, want)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Errorf("state folder not created: %v", err)
	}
}
