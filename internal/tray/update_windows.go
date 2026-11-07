//go:build windows

package tray

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/eyescreamxd/bsctl/internal/paths"
	"github.com/eyescreamxd/bsctl/internal/update"
	"github.com/eyescreamxd/bsctl/internal/version"
	"github.com/eyescreamxd/bsctl/internal/wintray"
)

const updateEvery = 6 * time.Hour

// updateState is a newer release found on GitHub.
type updateState struct {
	rel  update.Release
	busy bool // the installer is being downloaded
	err  string
}

func updateClient() update.Client {
	return update.Client{HTTP: &http.Client{Timeout: 2 * time.Minute}, UserAgent: "bsctl/" + version.Version}
}

// watchUpdates checks GitHub soon after the start and then every few hours.
// A portable build (dist) is a developer build: it is not updated.
func (a *trayApp) watchUpdates() {
	if n := update.RemoveInstallers(os.TempDir()); n > 0 {
		a.lg.Printf("обновление: удалено старых установщиков: %d", n)
	}
	if paths.Portable() {
		return
	}
	time.Sleep(30 * time.Second)
	for {
		a.checkUpdate()
		time.Sleep(updateEvery)
	}
}

func (a *trayApp) checkUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := updateClient().Latest(ctx, update.LatestURL)
	if err != nil {
		return // offline or GitHub is down: try again later, nothing to tell
	}
	if !update.Newer(rel.Version(), version.Version) {
		return
	}
	if _, _, ok := rel.Installer(); !ok {
		return
	}
	a.mu.Lock()
	fresh := a.upd == nil || a.upd.rel.Tag != rel.Tag
	if fresh {
		a.upd = &updateState{rel: rel}
	}
	a.mu.Unlock()
	if fresh {
		a.lg.Printf("доступно обновление %s (сейчас %s)", rel.Version(), version.Version)
	}
}

// updateMenu is the menu line for a found update (nothing when there is none).
func (a *trayApp) updateMenu() []wintray.MenuItem {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.upd == nil {
		return nil
	}
	u := a.upd
	switch {
	case u.busy:
		return []wintray.MenuItem{{Title: "Скачиваю обновление " + u.rel.Version() + "…", Disabled: true}}
	case u.err != "":
		return []wintray.MenuItem{{Title: "Обновление " + u.rel.Version() + " не скачалось — повторить", Action: func() { go a.installUpdate() }}}
	}
	return []wintray.MenuItem{{Title: "Установить обновление " + u.rel.Version(), Action: func() { go a.installUpdate() }}}
}

// installUpdate downloads the installer, checks its hash, starts it silently and
// quits: the installer replaces the files and starts the new tray.
func (a *trayApp) installUpdate() {
	a.mu.Lock()
	u := a.upd
	if u == nil || u.busy {
		a.mu.Unlock()
		return
	}
	u.busy, u.err = true, ""
	rel := u.rel
	a.mu.Unlock()

	fail := func(err error) {
		a.lg.Printf("обновление %s: %v", rel.Version(), err)
		a.mu.Lock()
		u.busy, u.err = false, err.Error()
		a.mu.Unlock()
	}
	dst := filepath.Join(os.TempDir(), fmt.Sprintf("bsctl-setup-%s.exe", rel.Version()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a.lg.Printf("обновление %s: скачиваю", rel.Version())
	if err := updateClient().Download(ctx, rel, dst); err != nil {
		fail(err)
		return
	}
	if err := exec.Command(dst, "/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART").Start(); err != nil {
		fail(fmt.Errorf("не запустить установщик: %w", err))
		return
	}
	a.lg.Printf("обновление %s: установщик запущен, выхожу", rel.Version())
	a.tray.Quit()
}
