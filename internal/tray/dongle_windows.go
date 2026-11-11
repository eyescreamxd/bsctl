//go:build windows

package tray

import (
	"time"

	"github.com/eyescreamxd/bsctl/internal/donglereset"
	"github.com/eyescreamxd/bsctl/internal/wintray"
)

// wakeResetAfter: how long after a wake the headset may stay unlinked before
// the tray restarts the dongle.
const wakeResetAfter = 30 * time.Second

func (a *trayApp) refreshDongleTask() {
	on := donglereset.Installed()
	a.mu.Lock()
	a.dongleTask = on
	a.mu.Unlock()
}

func (a *trayApp) dongleTaskOn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dongleTask
}

// resetDongle restarts the dongle: through the task when it exists, otherwise
// with a UAC prompt (manual only).
func (a *trayApp) resetDongle(why string, manual bool) {
	var err error
	switch {
	case a.dongleTaskOn():
		err = donglereset.Run()
	case manual:
		err = donglereset.RestartNow()
	default:
		return
	}
	if err != nil {
		a.lg.Printf("перезапуск донгла (%s): %v", why, err)
		return
	}
	a.lg.Printf("перезапускаю донгл: %s", why)
}

func (a *trayApp) toggleDongleTask() {
	on := !a.dongleTaskOn()
	var err error
	if on {
		err = donglereset.Enable()
	} else {
		err = donglereset.Disable(true)
	}
	a.refreshDongleTask()
	if err != nil {
		a.lg.Printf("перезапуск донгла после сна: %v", err)
		return
	}
	a.lg.Printf("перезапуск донгла после сна: %s", map[bool]string{true: "включён", false: "выключен"}[on])
}

// checkWake restarts the dongle once when the headset is still not linked a while
// after a wake: some sleeps leave the dongle's radio dead until it is re-plugged.
func (w *hsWorker) checkWake(linked bool) {
	if w.wokeAt.IsZero() {
		return
	}
	if linked {
		w.wokeAt = time.Time{}
		return
	}
	if time.Since(w.wokeAt) < wakeResetAfter {
		return
	}
	w.wokeAt = time.Time{}
	go w.app.resetDongle("гарнитура не подключилась после сна", false)
}

func (a *trayApp) dongleMenu() []wintray.MenuItem {
	return []wintray.MenuItem{
		{Title: "Перезапустить донгл", Action: func() { go a.resetDongle("кнопка в меню", true) }},
		{Title: "Перезапускать донгл после сна", Checked: a.dongleTaskOn(), Action: func() { go a.toggleDongleTask() }},
	}
}
