//go:build windows

// Package tray is BlackSharkTray: the headset and mouse workers, the tray icons
// and menus, and the backend of the settings window.
package tray

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/eyescreamxd/bsctl/internal/audio"
	"github.com/eyescreamxd/bsctl/internal/autostart"
	"github.com/eyescreamxd/bsctl/internal/config"
	"github.com/eyescreamxd/bsctl/internal/eapo"
	"github.com/eyescreamxd/bsctl/internal/headset"
	"github.com/eyescreamxd/bsctl/internal/hid"
	"github.com/eyescreamxd/bsctl/internal/icon"
	"github.com/eyescreamxd/bsctl/internal/razer"
	"github.com/eyescreamxd/bsctl/internal/ui"
	"github.com/eyescreamxd/bsctl/internal/version"
	"github.com/eyescreamxd/bsctl/internal/wintray"
)

const (
	iconHeadset = 100 // same id as the old tray, so Windows keeps its "always show" setting
	iconMouse   = 101
)

type trayApp struct {
	lg      *log.Logger
	tray    *wintray.Tray
	mu      sync.Mutex
	cfg     *config.Config
	cfgTime time.Time
	hs      *hsWorker
	ms      *msWorker

	uiOnce sync.Once
	ui     *trayUI
	uiSrv  *ui.Server

	upd        *updateState // newer release, guarded by mu
	dongleTask bool         // the dongle reset task exists, guarded by mu
}

// Run is BlackSharkTray: one tray per user session, blocks until the user quits.
func Run() {
	// One tray per user session.
	name, _ := windows.UTF16PtrFromString(`Local\BlackSharkTray`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	a := newTrayApp(config.NewLogger(false))
	a.start()
	autostart.Heal(a.lg) // the exe may have moved (e.g. into dist)
	if err := a.tray.Run(); err != nil {
		a.lg.Printf("трей: %v", err)
	}
	a.stop()
}

// RunSelfTest starts the tray, builds both menus, changes the icons and exits.
func RunSelfTest() {
	a := newTrayApp(config.NewLogger(true))
	a.start()
	go func() {
		time.Sleep(4 * time.Second)
		a.lg.Printf("selftest: значок наушников добавлен: %v, мыши: %v", a.tray.Added(iconHeadset), a.tray.Added(iconMouse))
		if h := wintray.IconHandle(icon.Headset(icon.Look{Level: 50, Online: true})); h != 0 {
			a.lg.Printf("selftest: HICON создаётся")
			wintray.DestroyIcon(h)
		} else {
			a.lg.Printf("selftest: HICON НЕ создаётся")
		}
		menus := []func() []wintray.MenuItem{a.headsetMenu}
		if a.ms != nil {
			menus = append(menus, a.mouseMenu)
		}
		for _, m := range menus {
			items := m()
			hm, acts := wintray.BuildMenu(items)
			a.lg.Printf("selftest: меню %q: %d строк, %d действий, hmenu=%v", items[0].Title, len(items), len(acts), hm != 0)
			wintray.DestroyMenu(hm)
		}
		for i, l := range []icon.Look{{Level: 80, Online: true, Muted: true}, {Level: 30, Online: true, Charging: true}} {
			a.tray.Set(iconHeadset, fmt.Sprintf("selftest %d", i), icon.Headset(l))
			a.tray.Set(iconMouse, fmt.Sprintf("selftest %d", i), icon.Mouse(l))
			time.Sleep(700 * time.Millisecond)
		}
		if s := os.Getenv("BSCTL_SELFTEST_HOLD"); s != "" {
			n, _ := strconv.Atoi(s)
			time.Sleep(time.Duration(n) * time.Second)
		}
		a.tray.Quit()
	}()
	if err := a.tray.Run(); err != nil {
		a.lg.Printf("selftest: трей: %v", err)
	}
	a.stop()
	a.lg.Printf("selftest: выход без ошибок")
}

func newTrayApp(lg *log.Logger) *trayApp {
	cfg, err := config.Load()
	if err != nil {
		lg.Printf("bsctl.json: %v", err)
	}
	a := &trayApp{lg: lg, tray: wintray.NewTray(), cfg: cfg, cfgTime: config.ModTime()}
	a.ui = &trayUI{a: a}
	a.hs = &hsWorker{app: a, lg: lg, reqs: make(chan string, 16), battery: -1, mic: -1, eqBase: -1, status: "BlackShark: поиск…"}
	a.tray.AddIcon(iconHeadset, "BlackShark: поиск…", icon.Headset(icon.Look{Level: -1}), a.headsetMenu, a.openSettings)
	if cfg.Mouse.Enabled {
		a.ms = &msWorker{app: a, lg: lg, reqs: make(chan string, 16), events: make(chan razer.DockEvent, 32), level: -1, status: "Мышь: поиск…", done: make(chan struct{})}
		a.tray.AddIcon(iconMouse, "Мышь: поиск…", icon.Mouse(icon.Look{Level: -1}), a.mouseMenu, a.openSettings)
	}
	return a
}

func (a *trayApp) start() {
	a.lg.Printf("tray %s started", version.Version)
	if _, _, ok := eapo.Dirs(); ok {
		a.lg.Printf("Equalizer APO: %s", micFormatHint())
	}
	go a.keepNoise()
	go a.watchUpdates()
	go a.refreshDongleTask()
	go a.hs.run()
	if a.ms != nil {
		go a.ms.run()
	}
}

func (a *trayApp) stop() {
	a.ui.win.close()
	if a.ms != nil {
		a.ms.stop()
	}
	a.lg.Printf("tray: выход")
}

// note: the headset worker is not stopped explicitly; closing the process ends
// the recording and frees the dongle.

func (a *trayApp) config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.cfg
}

func (a *trayApp) updateConfig(f func(*config.Config)) config.Config {
	a.mu.Lock()
	f(a.cfg)
	c := *a.cfg
	err := config.Save(a.cfg)
	a.cfgTime = config.ModTime()
	a.mu.Unlock()
	if err != nil {
		a.lg.Printf("сохранение bsctl.json: %v", err)
	}
	return c
}

// reloadIfChanged picks up manual edits of bsctl.json.
func (a *trayApp) reloadIfChanged() bool {
	t := config.ModTime()
	a.mu.Lock()
	defer a.mu.Unlock()
	if t.Equal(a.cfgTime) {
		return false
	}
	a.cfgTime = t
	c, err := config.Load()
	if err != nil {
		a.lg.Printf("bsctl.json: %v — изменения не применены", err)
		return false
	}
	c.DevicePath, c.UseSetOutput = a.cfg.DevicePath, a.cfg.UseSetOutput
	a.cfg = c
	a.cfgTime = config.ModTime()
	a.lg.Printf("bsctl.json изменён, применяю")
	return true
}

func send(ch chan string, req string) {
	select {
	case ch <- req:
	default:
	}
}

func openInNotepad(path string) { _ = exec.Command("notepad.exe", path).Start() }

// ---------------- headset ----------------

type hsWorker struct {
	app  *trayApp
	lg   *log.Logger
	reqs chan string
	hs   *headset.Headset

	mu        sync.Mutex // guards the fields the menu reads
	status    string
	mic       int // -1 unknown, 0 on, 1 muted
	connected bool
	battery   int
	charging  bool

	hold        *audio.MicHold // keeps the mic recording while sidetone is on
	holdErr     string
	eqBase      int // "speaker preset EQ" value right after our apply, -1 unknown
	sentinelOff bool
	resets      []time.Time
	lastApply   time.Time
	micAsks     int
	lastKey     string
	wokeAt      time.Time // last wake from sleep, zero once the headset linked or the dongle was reset
}

func (w *hsWorker) run() {
	cfg := w.app.config()
	interval := time.Duration(cfg.PollSeconds) * time.Second
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastWall := time.Now().Round(0)
	ticks := 0
	w.poll(true)
	for {
		var events chan *headset.Response
		if w.hs != nil {
			events = w.hs.Events
		}
		select {
		case r := <-w.reqs:
			w.handle(r)
		case ev := <-events:
			w.onEvent(ev)
		case <-tick.C:
			now := time.Now().Round(0) // wall clock: jumps after sleep/hibernate
			if gap := now.Sub(lastWall); gap > 3*interval+10*time.Second {
				w.lg.Printf("пробуждение после сна (%v), переподключаюсь", gap.Round(time.Second))
				w.drop()
				w.wokeAt = time.Now()
				time.Sleep(8 * time.Second)
			}
			lastWall = time.Now().Round(0)
			if w.app.reloadIfChanged() {
				w.apply("bsctl.json изменён")
				if w.app.ms != nil {
					send(w.app.ms.reqs, "config")
				}
			}
			ticks++
			w.poll(ticks%12 == 0)
		}
	}
}

func (w *hsWorker) drop() {
	if w.hs != nil {
		w.hs.Close()
		w.hs = nil
	}
	w.mu.Lock()
	w.connected = false
	w.mu.Unlock()
	w.syncMicHold()
}

// syncMicHold records the headset mic while sidetone is on and the headset is connected.
func (w *hsWorker) syncMicHold() {
	cfg := w.app.config()
	w.mu.Lock()
	want := cfg.Sidetone && cfg.SidetoneMicOpen && w.connected && w.hs != nil
	w.mu.Unlock()
	switch {
	case want && w.hold == nil:
		h, err := audio.OpenMicHold()
		if err != nil {
			if err.Error() != w.holdErr {
				w.holdErr = err.Error()
				w.lg.Printf("самопрослушивание: не открыть микрофон: %v", err)
			}
			return
		}
		w.hold, w.holdErr = h, ""
		w.lg.Printf("самопрослушивание: держу микрофон открытым (%s)", h.Name)
	case !want && w.hold != nil:
		w.hold.Close()
		w.hold = nil
		w.lg.Printf("самопрослушивание: микрофон отпущен")
	}
}

func (w *hsWorker) setStatus(s string) {
	w.mu.Lock()
	w.status = s
	w.mu.Unlock()
	w.show()
}

// poll keeps the dongle in PC mode, notices (re)connections and dropped settings.
func (w *hsWorker) poll(full bool) {
	if hid.ProcessRunning("RazerAppEngine.exe") {
		if w.hs != nil {
			w.lg.Printf("запущен Synapse — отпускаю гарнитуру, пока он работает")
			w.drop()
		}
		w.setStatus("BlackShark: запущен Synapse — гарнитурой управляет он")
		return
	}
	if w.hs != nil && !w.hs.Alive() {
		w.lg.Printf("связь с донглом потеряна: %v", w.hs.ReadErr())
		w.drop()
	}
	if w.hs == nil {
		cfg := w.app.config()
		conn := cfg.HeadsetConn()
		hs, err := headset.Open(&conn, w.lg)
		if err != nil {
			w.setStatus(fmt.Sprintf("BlackShark: %v", err))
			return
		}
		cfg.DevicePath, cfg.UseSetOutput = conn.DevicePath, conn.UseSetOutput
		if old := w.app.config(); old.DevicePath != cfg.DevicePath || old.UseSetOutput != cfg.UseSetOutput {
			w.app.updateConfig(func(c *config.Config) { c.DevicePath, c.UseSetOutput = cfg.DevicePath, cfg.UseSetOutput })
		}
		w.hs = hs
		w.eqBase, w.micAsks = -1, 0
		w.mu.Lock()
		w.connected, w.mic = false, -1
		w.mu.Unlock()
		full = true
		w.lg.Printf("донгл открыт: %s", hs.Path())
	}
	c, err := w.hs.Connected() // asserts remote mode: also keeps the dongle in PC mode
	if errors.Is(err, headset.ErrNoResponse) {
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		w.setStatus("BlackShark: донгл молчит (гарнитура выключена?)")
		return
	}
	if err != nil {
		w.lg.Printf("связь с донглом потеряна: %v", err)
		w.drop()
		w.setStatus("BlackShark: донгл недоступен")
		return
	}
	w.mu.Lock()
	was := w.connected
	w.connected = c
	w.mu.Unlock()
	w.checkWake(c)
	if !c {
		if was {
			w.lg.Printf("гарнитура отключилась")
		}
		w.syncMicHold()
		w.setStatus("BlackShark: гарнитура выключена")
		return
	}
	cfg := w.app.config()
	switch {
	case !was:
		w.lg.Printf("гарнитура подключена, отправляю настройки")
		time.Sleep(1500 * time.Millisecond)
		w.apply("подключение")
		full = true
		w.micAsks = 0 // the mic state may have changed while it was off
		w.mu.Lock()
		w.mic = -1
		w.mu.Unlock()
	case cfg.ReapplyMinutes > 0 && time.Since(w.lastApply) > time.Duration(cfg.ReapplyMinutes)*time.Minute:
		w.apply("по таймеру reapplyMinutes")
	default:
		w.checkSettingsKept()
	}
	if full || w.battery < 0 {
		b, berr := w.hs.Battery()
		ch, cerr := w.hs.Charging()
		w.mu.Lock()
		if berr == nil {
			w.battery = b
		}
		if cerr == nil {
			w.charging = ch
		}
		w.mu.Unlock()
	}
	w.mu.Lock()
	needMic := w.mic < 0
	w.mu.Unlock()
	if needMic && (full || w.micAsks == 0) && w.micAsks < 3 {
		w.micAsks++
		if m, err := w.hs.MicMuted(); err == nil {
			w.setMic(m, "запрос")
		} else if w.micAsks == 3 {
			w.lg.Printf("гарнитура не говорит состояние микрофона — покажу его после нажатия кнопки")
		}
	}
	w.syncMicHold()
	w.show()
}

// checkSettingsKept: after our apply the "speaker preset EQ" flag has a known value.
// If it changes by itself, the dongle went back to its own (quiet) profile.
func (w *hsWorker) checkSettingsKept() {
	if w.sentinelOff {
		return
	}
	st, err := w.hs.EQStatus()
	if err != nil {
		return
	}
	if w.eqBase < 0 {
		w.eqBase = st
		return
	}
	if st == w.eqBase {
		return
	}
	now := time.Now()
	w.resets = append(w.resets, now)
	for len(w.resets) > 0 && now.Sub(w.resets[0]) > 10*time.Minute {
		w.resets = w.resets[1:]
	}
	if len(w.resets) >= 3 {
		w.sentinelOff = true
		w.lg.Printf("флаг EQ меняется сам по себе (%d -> %d) слишком часто — перестаю на него смотреть", w.eqBase, st)
		return
	}
	w.apply(fmt.Sprintf("донгл сбросил настройки (флаг EQ %d -> %d)", w.eqBase, st))
}

func (w *hsWorker) apply(why string) {
	if w.hs == nil {
		return
	}
	w.mu.Lock()
	conn := w.connected
	w.mu.Unlock()
	if !conn {
		return
	}
	cfg := w.app.config()
	w.lg.Printf("отправляю настройки: %s", why)
	if err := w.hs.Apply(cfg.HeadsetSettings()); err != nil {
		w.lg.Printf("применение настроек: %v", err)
		return
	}
	w.lastApply = time.Now()
	if st, err := w.hs.EQStatus(); err == nil {
		w.eqBase = st
	}
}

// setSidetone sends only the sidetone commands (no EQ resend, no audio click).
func (w *hsWorker) setSidetone(c config.Config) {
	w.mu.Lock()
	conn := w.connected
	w.mu.Unlock()
	if w.hs == nil || !conn {
		return
	}
	if err := w.hs.SetSidetone(c.Sidetone, c.SidetoneLevel); err != nil {
		w.lg.Printf("самопрослушивание: %v", err)
		return
	}
	if c.Sidetone {
		w.lg.Printf("самопрослушивание: включено, громкость %d%%", c.SidetoneLevel)
	} else {
		w.lg.Printf("самопрослушивание: выключено")
	}
	w.syncMicHold()
}

func (w *hsWorker) setMic(muted bool, src string) {
	v := 0
	if muted {
		v = 1
	}
	w.mu.Lock()
	changed := w.mic != v
	w.mic = v
	w.mu.Unlock()
	if changed {
		w.lg.Printf("микрофон: %s (%s)", map[bool]string{true: "ВЫКЛЮЧЕН", false: "включён"}[muted], src)
	}
	w.show()
}

func (w *hsWorker) onEvent(f *headset.Response) {
	if !headset.IsNotify(f) {
		return
	}
	switch f.Op0 {
	case headset.CmdMicMute:
		w.setMic(f.Data[0] != 0, "кнопка")
	case headset.CmdBattery:
		w.mu.Lock()
		w.battery = int(f.Data[0])
		w.mu.Unlock()
		w.show()
	case headset.CmdCharging:
		w.mu.Lock()
		w.charging = f.Data[0] != 0
		w.mu.Unlock()
		w.show()
	case headset.CmdConnection:
		w.mu.Lock()
		was := w.connected
		w.mu.Unlock()
		if f.Data[0] == 1 && !was {
			w.poll(true)
		} else if f.Data[0] == 0 && was {
			w.mu.Lock()
			w.connected = false
			w.mu.Unlock()
			w.lg.Printf("гарнитура отключилась")
			w.syncMicHold()
			w.setStatus("BlackShark: гарнитура выключена")
		}
	}
}

func (w *hsWorker) handle(r string) {
	switch {
	case strings.HasPrefix(r, "preset:"):
		key := strings.TrimPrefix(r, "preset:")
		w.app.updateConfig(func(c *config.Config) { c.Preset = key })
		w.apply("эквалайзер: " + key)
	case r == "sidetone" || strings.HasPrefix(r, "sidetone:"):
		c := w.app.updateConfig(func(c *config.Config) {
			if n, err := strconv.Atoi(strings.TrimPrefix(r, "sidetone:")); err == nil {
				c.Sidetone, c.SidetoneLevel = true, n
			} else {
				c.Sidetone = !c.Sidetone
			}
		})
		w.setSidetone(c)
	case r == "apply":
		if w.hs == nil {
			w.poll(true) // opening the dongle applies the settings anyway
			return
		}
		w.apply("кнопка в меню")
	case r == "eq": // from the settings window: only the EQ, no click from other settings
		w.drainSame(r)
		if !w.ready() {
			return
		}
		c := w.app.config()
		g, i, b := c.EffectiveEQ()
		if err := w.hs.ClearEQStatus(); err == nil {
			err = w.hs.SetEQ(g, i, b)
			if err != nil {
				w.lg.Printf("эквалайзер: %v", err)
			}
		}
	case r == "sidetone-cfg":
		w.drainSame(r)
		w.setSidetone(w.app.config())
	}
}

// ready: the headset is open and connected.
func (w *hsWorker) ready() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hs != nil && w.connected
}

// drainSame drops queued duplicates of a request (slider moves come in bursts);
// the handler applies the latest config anyway.
func (w *hsWorker) drainSame(r string) {
	for {
		select {
		case next := <-w.reqs:
			if next != r {
				go func() { w.reqs <- next }() // keep other requests, in order enough
				return
			}
		default:
			return
		}
	}
}

func (w *hsWorker) show() {
	w.mu.Lock()
	look := icon.Look{Level: w.battery, Online: w.connected, Charging: w.charging, Muted: w.mic == 1}
	text := w.status
	if w.connected {
		text = "BlackShark V2 Pro: "
		if w.battery >= 0 {
			text += fmt.Sprintf("%d%%", w.battery)
		} else {
			text += "подключена"
		}
		if w.charging {
			text += ", заряжается"
		}
		w.status = text
	}
	tip := text
	if w.connected && w.mic == 1 {
		tip += "\nМикрофон ВЫКЛЮЧЕН"
	}
	w.mu.Unlock()
	key := fmt.Sprintf("%s|%+v", tip, look)
	if key == w.lastKey {
		return
	}
	w.lastKey = key
	w.app.tray.Set(iconHeadset, tip, icon.Headset(look))
}

func (a *trayApp) headsetMenu() []wintray.MenuItem {
	w := a.hs
	w.mu.Lock()
	status, mic, conn := w.status, w.mic, w.connected
	w.mu.Unlock()
	cfg := a.config()
	micLine := "Микрофон: состояние неизвестно"
	switch {
	case !conn:
		micLine = "Микрофон: —"
	case mic == 0:
		micLine = "Микрофон: включён"
	case mic == 1:
		micLine = "Микрофон: ВЫКЛЮЧЕН"
	}
	var sidetoneLevels []wintray.MenuItem
	for _, n := range []int{25, 50, 75, 100} {
		n := n
		sidetoneLevels = append(sidetoneLevels, wintray.MenuItem{Title: fmt.Sprintf("%d%%", n), Checked: cfg.Sidetone && cfg.SidetoneLevel == n,
			Action: func() { send(w.reqs, fmt.Sprintf("sidetone:%d", n)) }})
	}
	var eq []wintray.MenuItem
	for _, p := range headset.Presets {
		key := p.Key
		eq = append(eq, wintray.MenuItem{Title: p.Title, Checked: key == cfg.Preset, Action: func() { send(w.reqs, "preset:"+key) }})
	}
	cur := eapo.FindEngine(cfg.Noise.Engine).Key
	noise := []wintray.MenuItem{{Title: "Выключен", Checked: !cfg.Noise.Enabled, Action: func() { go a.setNoise(false, "") }}}
	for _, e := range eapo.Engines {
		key := e.Key
		noise = append(noise, wintray.MenuItem{Title: e.Title + " (" + e.Short + ")", Checked: cfg.Noise.Enabled && cur == key,
			Action: func() { go a.setNoise(true, key) }})
	}
	items := []wintray.MenuItem{
		{Title: status, Disabled: true},
		{Title: micLine, Disabled: true},
	}
	items = append(items, a.updateMenu()...)
	items = append(items, []wintray.MenuItem{
		{Separator: true},
		{Title: "Настройки (эквалайзеры, шумодав)…", Action: a.openSettings},
		{Title: "Эквалайзер", Sub: eq},
		{Title: "Шумодав", Sub: noise},
		{Title: "Самопрослушивание", Checked: cfg.Sidetone, Action: func() { send(w.reqs, "sidetone") }},
		{Title: "Громкость самопрослушивания", Sub: sidetoneLevels},
		{Title: "Применить настройки", Action: func() { send(w.reqs, "apply") }},
	}...)
	items = append(items, a.dongleMenu()...)
	return append(items, []wintray.MenuItem{
		{Separator: true},
		{Title: "Открыть bsctl.json", Action: func() { openInNotepad(config.Path()) }},
		{Title: "Открыть лог", Action: func() { openInNotepad(config.LogPath()) }},
		{Title: "Запускать с Windows", Checked: autostart.Enabled(), Action: func() {
			on := !autostart.Enabled()
			if err := autostart.Set(on); err != nil {
				a.lg.Printf("autostart: %v", err)
			}
		}},
		{Separator: true},
		{Title: "Выход", Action: a.tray.Quit},
	}...)
}

// ---------------- mouse and dock ----------------

type msWorker struct {
	app    *trayApp
	lg     *log.Logger
	reqs   chan string
	events chan razer.DockEvent

	dock       *razer.Link // Mouse Dock Pro: its light and (usually) the radio to the mouse
	mouse      *razer.Link // the way to the mouse: cable, own dongle or the dock
	stopEvents func()
	soon       <-chan time.Time
	done       chan struct{}

	mu       sync.Mutex // fields below are read by the menu
	status   string
	dockLine string
	level    int
	charging bool
	online   bool

	seen       bool
	fails      int
	synapse    bool
	lastFx     string
	lastFxTime time.Time
	lastErr    string
	lastKey    string
}

func (w *msWorker) run() {
	defer close(w.done)
	cfg := w.app.config()
	interval := time.Duration(cfg.Mouse.PollSeconds) * time.Second
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastWall := time.Now().Round(0)
	w.poll()
	for {
		select {
		case r := <-w.reqs:
			if r == "quit" {
				w.shutdown()
				return
			}
			w.handle(r)
		case ev := <-w.events:
			w.onDockEvent(ev)
		case <-w.soon:
			w.soon = nil
			w.poll()
		case <-tick.C:
			now := time.Now().Round(0)
			if gap := now.Sub(lastWall); gap > 3*interval+10*time.Second {
				w.lg.Printf("мышь: пробуждение после сна, переподключаюсь")
				w.drop()
				time.Sleep(5 * time.Second)
			}
			lastWall = time.Now().Round(0)
			w.poll()
		}
	}
}

func (w *msWorker) stop() {
	send(w.reqs, "quit")
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
	}
}

func (w *msWorker) drop() {
	if w.stopEvents != nil {
		w.stopEvents()
		w.stopEvents = nil
	}
	if w.mouse != nil && w.mouse != w.dock {
		w.mouse.Close()
	}
	if w.dock != nil {
		w.dock.Close()
	}
	w.dock, w.mouse, w.lastFx = nil, nil, ""
	w.mu.Lock()
	w.online = false
	w.mu.Unlock()
}

func (w *msWorker) logOnce(msg string) {
	if msg != w.lastErr {
		w.lastErr = msg
		w.lg.Printf("мышь: %s", msg)
	}
}

func (w *msWorker) connect() {
	cfg := w.app.config()
	if l, err := razer.OpenLink(razer.PIDMouseDock, w.lg, false); err == nil {
		w.dock = l
		fw, _ := l.Firmware(true)
		var n int
		w.stopEvents, n = razer.DockEvents(w.lg, false, w.events)
		w.lg.Printf("мышь: найден Mouse Dock Pro (прошивка %s), слушаю события в %d коллекциях", fw, n)
		if cfg.Mouse.DockLighting {
			if err := l.DockBrightness(cfg.Mouse.DockBrightness); err != nil {
				w.lg.Printf("мышь: яркость дока: %v", err)
			}
		}
	} else if !errors.Is(err, razer.ErrNotFound) {
		w.logOnce(err.Error())
	}
	for _, pid := range []uint16{razer.PIDBasiliskW, razer.PIDBasiliskRx} {
		if l, err := razer.OpenLink(pid, w.lg, false); err == nil {
			w.mouse = l
			w.lg.Printf("мышь: подключена как %s", razer.PIDName(pid))
			break
		}
	}
	if w.mouse == nil && w.dock != nil {
		w.mouse = w.dock
	}
}

func (w *msWorker) poll() {
	if hid.ProcessRunning("RazerAppEngine.exe") {
		if !w.synapse {
			w.lg.Printf("мышь: запущен Synapse — не трогаю мышь и док, пока он работает")
			w.drop()
		}
		w.synapse = true
		w.setStatus("Мышь: запущен Synapse — управляет он", "")
		return
	}
	w.synapse = false
	if w.mouse == nil {
		w.connect()
	}
	if w.mouse == nil {
		w.setStatus("Мышь: не найдена (нет ни дока, ни мыши)", "Док: не найден")
		return
	}
	level, err := w.mouse.BatteryLevel()
	charging := false
	if err == nil {
		w.mu.Lock()
		charging = w.charging
		w.mu.Unlock()
		if ch, cerr := w.mouse.Charging(); cerr == nil {
			charging = ch
		}
	}
	switch {
	case err == nil:
		w.fails = 0
		w.seen = true
		w.lastErr = ""
		w.mu.Lock()
		if w.charging != charging && w.online {
			w.lg.Printf("мышь: %s, %d%%", map[bool]string{true: "заряжается", false: "не заряжается"}[charging], level)
		}
		if !w.online {
			w.lg.Printf("мышь на связи: %d%%%s", level, map[bool]string{true: ", заряжается", false: ""}[charging])
		}
		w.level, w.charging, w.online = level, charging, true
		w.mu.Unlock()
	case razer.MouseOffline(err):
		w.fails++
		w.mu.Lock()
		if w.online && w.fails >= 2 {
			w.online = false
			w.lg.Printf("мышь не отвечает (спит или выключена)")
		}
		if !w.seen {
			w.online = false
		}
		w.mu.Unlock()
		w.logOnce(err.Error())
	default:
		w.lg.Printf("мышь: связь потеряна: %v", err)
		w.drop()
	}
	w.updateDock()
	w.show()
}

func (w *msWorker) updateDock() {
	cfg := w.app.config()
	if w.dock == nil || !cfg.Mouse.DockLighting {
		return
	}
	w.mu.Lock()
	level, charging, online := w.level, w.charging, w.online
	w.mu.Unlock()
	if !online && (cfg.Mouse.DockRedWhenOff || !w.seen) {
		level = -1 // like Synapse: no mouse -> red
	}
	effect, col := razer.DockLook(level, online && charging)
	key := razer.FxName(effect, col)
	if key == w.lastFx && time.Since(w.lastFxTime) < 10*time.Minute {
		return
	}
	if err := w.dock.DockEffect(effect, col); err != nil {
		w.lg.Printf("мышь: подсветка дока: %v", err)
		var re *razer.Error
		if !errors.As(err, &re) && !errors.Is(err, razer.ErrOffline) {
			w.drop()
		}
		return
	}
	if key != w.lastFx {
		w.lg.Printf("мышь: док -> %s", key)
	}
	w.lastFx, w.lastFxTime = key, time.Now()
}

func (w *msWorker) onDockEvent(ev razer.DockEvent) {
	switch {
	case ev.ID == razer.DockEvBattery && len(ev.Data) >= 4 && ev.Data[3] == 1:
		level, charging := razer.BatteryPercent(ev.Data[0]), ev.Data[1] != 0
		w.mu.Lock()
		if charging != w.charging {
			w.lg.Printf("мышь: %s, %d%% (событие дока)", map[bool]string{true: "заряжается", false: "не заряжается"}[charging], level)
		}
		w.level, w.charging, w.online = level, charging, true
		w.mu.Unlock()
		w.seen, w.fails = true, 0
		w.updateDock()
		w.show()
	case ev.ID == razer.DockEvWireless && len(ev.Data) >= 2 && ev.Data[1] == 1:
		if ev.Data[0] == 3 {
			w.soon = time.After(2 * time.Second)
		} else if ev.Data[0] == 2 {
			w.mu.Lock()
			was := w.online
			w.online = false
			w.mu.Unlock()
			if was {
				w.lg.Printf("мышь отключилась от дока (сон или выключение)")
			}
			w.updateDock()
			w.show()
		}
	}
}

func (w *msWorker) handle(r string) {
	switch {
	case r == "poll":
		w.poll()
	case r == "config":
		w.lastFx = ""
		if cfg := w.app.config(); w.dock != nil && cfg.Mouse.DockLighting {
			_ = w.dock.DockBrightness(cfg.Mouse.DockBrightness)
		}
		w.updateDock()
	case r == "dock-toggle":
		c := w.app.updateConfig(func(c *config.Config) { c.Mouse.DockLighting = !c.Mouse.DockLighting })
		w.lastFx = ""
		if w.dock != nil {
			if c.Mouse.DockLighting {
				_ = w.dock.DockBrightness(c.Mouse.DockBrightness)
				w.updateDock()
			} else if err := w.dock.DockEffect(razer.FxSpectrum); err != nil {
				w.lg.Printf("мышь: подсветка дока: %v", err)
			}
		}
		w.lg.Printf("мышь: подсветка дока по заряду: %v", c.Mouse.DockLighting)
		w.show()
	case strings.HasPrefix(r, "bright:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r, "bright:"))
		c := w.app.updateConfig(func(c *config.Config) { c.Mouse.DockBrightness = n })
		if w.dock != nil {
			if err := w.dock.DockBrightness(c.Mouse.DockBrightness); err != nil {
				w.lg.Printf("мышь: яркость дока: %v", err)
			}
		}
	}
}

// shutdown: like Synapse on exit, leave the dock on its rainbow instead of a stale battery color.
func (w *msWorker) shutdown() {
	if w.dock != nil && w.app.config().Mouse.DockLighting {
		_ = w.dock.DockEffect(razer.FxSpectrum)
	}
	w.drop()
}

func (w *msWorker) setStatus(status, dock string) {
	w.mu.Lock()
	w.status, w.dockLine = status, dock
	w.mu.Unlock()
	w.show()
}

func (w *msWorker) show() {
	cfg := w.app.config()
	w.mu.Lock()
	look := icon.Look{Level: w.level, Online: w.online, Charging: w.charging}
	if w.mouse != nil && !w.synapse {
		switch {
		case w.online:
			w.status = fmt.Sprintf("Basilisk V3 Pro: %d%%", w.level)
			if w.charging {
				w.status += ", заряжается"
			}
		case w.seen:
			w.status = fmt.Sprintf("Basilisk V3 Pro: спит (было %d%%)", w.level)
		default:
			w.status = "Basilisk V3 Pro: не отвечает (выключена?)"
		}
		switch {
		case w.dock == nil:
			w.dockLine = "Док: не найден"
		case !cfg.Mouse.DockLighting:
			w.dockLine = "Док: подсветка не управляется"
		case w.lastFx != "":
			w.dockLine = "Док: " + w.lastFx
		default:
			w.dockLine = "Док: найден"
		}
	}
	tip := w.status
	w.mu.Unlock()
	key := fmt.Sprintf("%s|%+v", tip, look)
	if key == w.lastKey {
		return
	}
	w.lastKey = key
	w.app.tray.Set(iconMouse, tip, icon.Mouse(look))
}

func (a *trayApp) mouseMenu() []wintray.MenuItem {
	w := a.ms
	w.mu.Lock()
	status, dockLine := w.status, w.dockLine
	w.mu.Unlock()
	cfg := a.config()
	var bright []wintray.MenuItem
	for _, n := range []int{10, 30, 50, 75, 100} {
		n := n
		bright = append(bright, wintray.MenuItem{Title: fmt.Sprintf("%d%%", n), Checked: cfg.Mouse.DockBrightness == n,
			Action: func() { send(w.reqs, fmt.Sprintf("bright:%d", n)) }})
	}
	items := []wintray.MenuItem{{Title: status, Disabled: true}}
	if dockLine != "" {
		items = append(items, wintray.MenuItem{Title: dockLine, Disabled: true})
	}
	return append(items,
		wintray.MenuItem{Separator: true},
		wintray.MenuItem{Title: "Настройки…", Action: a.openSettings},
		wintray.MenuItem{Title: "Подсветка дока показывает заряд мыши", Checked: cfg.Mouse.DockLighting, Action: func() { send(w.reqs, "dock-toggle") }},
		wintray.MenuItem{Title: "Яркость дока", Sub: bright},
		wintray.MenuItem{Title: "Проверить заряд сейчас", Action: func() { send(w.reqs, "poll") }},
		wintray.MenuItem{Separator: true},
		wintray.MenuItem{Title: "Открыть лог", Action: func() { openInNotepad(config.LogPath()) }},
		wintray.MenuItem{Title: "Выход", Action: a.tray.Quit},
	)
}
