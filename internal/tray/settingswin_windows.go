//go:build windows

package tray

// The settings window is an Edge app window, i.e. a window of another process. To
// put it back where the user left it, the tray finds it by its title, moves it there
// and then watches where the user drags it; the last place goes to bsctl.json.

import (
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/eyescreamxd/bsctl/internal/config"
	"github.com/eyescreamxd/bsctl/internal/ui"
)

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")

	procPostMessageW                 = modUser32.NewProc("PostMessageW")
	procSetForegroundWindow          = modUser32.NewProc("SetForegroundWindow")
	procEnumWindows                  = modUser32.NewProc("EnumWindows")
	procGetWindowTextW               = modUser32.NewProc("GetWindowTextW")
	procGetClassNameW                = modUser32.NewProc("GetClassNameW")
	procIsWindow                     = modUser32.NewProc("IsWindow")
	procIsWindowVisible              = modUser32.NewProc("IsWindowVisible")
	procIsIconic                     = modUser32.NewProc("IsIconic")
	procIsZoomed                     = modUser32.NewProc("IsZoomed")
	procGetWindowRect                = modUser32.NewProc("GetWindowRect")
	procSetWindowPos                 = modUser32.NewProc("SetWindowPos")
	procShowWindow                   = modUser32.NewProc("ShowWindow")
	procMonitorFromPoint             = modUser32.NewProc("MonitorFromPoint")
	procSetThreadDpiAwarenessContext = modUser32.NewProc("SetThreadDpiAwarenessContext")
)

const (
	swRestore             = 9
	swpNoZOrder           = 0x0004
	swpNoActivate         = 0x0010
	monitorDefaultToNull  = 0
	dpiPerMonitorAwareV2  = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4
	wmCloseMsg            = 0x0010
	settingsFindTimeout   = 15 * time.Second
	settingsPollInterval  = 500 * time.Millisecond
	settingsRestoreWindow = 2 * time.Second // Edge may still move a fresh window during this time
)

type settingsWindow struct {
	mu       sync.Mutex
	hwnd     uintptr
	launchAt time.Time
}

// launched notes that Edge is starting; starting() then swallows repeated clicks.
func (w *settingsWindow) launched() {
	w.mu.Lock()
	w.launchAt = time.Now()
	w.mu.Unlock()
}

func (w *settingsWindow) starting() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hwnd == 0 && time.Since(w.launchAt) < 5*time.Second
}

// close closes the tracked window (the tray is quitting, its page would be dead).
func (w *settingsWindow) close() {
	w.mu.Lock()
	h := w.hwnd
	w.mu.Unlock()
	if h != 0 && isSettingsWindow(h) {
		procPostMessageW.Call(h, wmCloseMsg, 0, 0)
	}
}

type winRect struct{ Left, Top, Right, Bottom int32 }

// focus brings the tracked settings window to the front; false if it is gone.
func (w *settingsWindow) focus() bool {
	w.mu.Lock()
	h := w.hwnd
	w.mu.Unlock()
	if h == 0 || !isSettingsWindow(h) {
		return false
	}
	if r, _, _ := procIsIconic.Call(h); r != 0 {
		procShowWindow.Call(h, swRestore)
	}
	procSetForegroundWindow.Call(h)
	return true
}

// track waits for the new Edge window, restores the saved place and records moves.
// stale are settings windows left from an earlier tray run (their page is dead).
func (w *settingsWindow) track(a *trayApp, want *config.WindowRect, stale []uintptr) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if procSetThreadDpiAwarenessContext.Find() == nil {
		procSetThreadDpiAwarenessContext.Call(dpiPerMonitorAwareV2) // physical pixels, like Edge itself
	}
	for _, h := range stale {
		procPostMessageW.Call(h, wmCloseMsg, 0, 0)
	}
	var h uintptr
	for deadline := time.Now().Add(settingsFindTimeout); h == 0 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		for _, c := range findSettingsWindows() {
			if !contains(stale, c) {
				h = c
				break
			}
		}
	}
	if h == 0 {
		a.lg.Printf("окно настроек: не нашёл окно Edge %q, место не запоминаю", ui.SettingsTitle)
		return
	}
	w.mu.Lock()
	w.hwnd = h
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.hwnd == h {
			w.hwnd = 0
		}
		w.mu.Unlock()
	}()

	if want != nil && rectVisible(*want) {
		for end := time.Now().Add(settingsRestoreWindow); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			if !isSettingsWindow(h) {
				return
			}
			if r, ok := windowRect(h); ok && r != *want {
				procSetWindowPos.Call(h, 0, uintptr(want.X), uintptr(want.Y), uintptr(want.W), uintptr(want.H), swpNoZOrder|swpNoActivate)
			}
		}
	}

	last, _ := windowRect(h)
	stable := 0
	for {
		time.Sleep(settingsPollInterval)
		if !isSettingsWindow(h) {
			return
		}
		if r, _, _ := procIsIconic.Call(h); r != 0 {
			continue
		}
		if r, _, _ := procIsZoomed.Call(h); r != 0 {
			continue
		}
		r, ok := windowRect(h)
		if !ok {
			continue
		}
		if r != last {
			last, stable = r, 0
			continue
		}
		if stable++; stable == 1 { // the move has settled
			if cur := a.config().SettingsWindow; cur == nil || *cur != r {
				a.updateConfig(func(c *config.Config) { c.SettingsWindow = &r })
			}
		}
	}
}

func contains(list []uintptr, h uintptr) bool {
	for _, x := range list {
		if x == h {
			return true
		}
	}
	return false
}

func windowRect(h uintptr) (config.WindowRect, bool) {
	var r winRect
	if ok, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return config.WindowRect{}, false
	}
	return config.WindowRect{X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)}, true
}

// rectVisible: the middle of the title bar is on some monitor.
func rectVisible(r config.WindowRect) bool {
	x, y := int32(r.X+r.W/2), int32(r.Y+15)
	m, _, _ := procMonitorFromPoint.Call(uintptr(uint32(x))|uintptr(uint32(y))<<32, monitorDefaultToNull)
	return m != 0
}

func windowText(h uintptr, proc *windows.LazyProc) string {
	buf := make([]uint16, 256)
	n, _, _ := proc.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func isSettingsWindow(h uintptr) bool {
	if ok, _, _ := procIsWindow.Call(h); ok == 0 {
		return false
	}
	// prefix: a browser may add its name to the title
	return strings.HasPrefix(windowText(h, procGetWindowTextW), ui.SettingsTitle) && windowText(h, procGetClassNameW) == "Chrome_WidgetWin_1"
}

var (
	enumMu    sync.Mutex
	enumFound []uintptr
	enumCB    = windows.NewCallback(func(h, _ uintptr) uintptr {
		if v, _, _ := procIsWindowVisible.Call(h); v != 0 && isSettingsWindow(h) {
			enumFound = append(enumFound, h)
		}
		return 1 // go on
	})
)

// findSettingsWindows lists the visible settings windows.
func findSettingsWindows() []uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = nil
	procEnumWindows.Call(enumCB, 0)
	return append([]uintptr(nil), enumFound...)
}
