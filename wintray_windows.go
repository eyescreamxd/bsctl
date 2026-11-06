//go:build windows

package main

// A small notification-area (tray) implementation on plain Win32: one hidden
// window, several icons, popup menus built on every click from fresh state.

import (
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW         = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW          = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW           = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow            = modUser32.NewProc("DestroyWindow")
	procGetMessageW              = modUser32.NewProc("GetMessageW")
	procTranslateMessage         = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW         = modUser32.NewProc("DispatchMessageW")
	procPostMessageW             = modUser32.NewProc("PostMessageW")
	procPostQuitMessage          = modUser32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW   = modUser32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW              = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu           = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu              = modUser32.NewProc("DestroyMenu")
	procSetForegroundWindow      = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos             = modUser32.NewProc("GetCursorPos")
	procCreateIconFromResourceEx = modUser32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = modUser32.NewProc("DestroyIcon")
	procShellNotifyIconW         = modShell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW         = modKernel32.NewProc("GetModuleHandleW")
)

const (
	wmNull        = 0x0000
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmEndSession  = 0x0016
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmApp         = 0x8000
	wmTrayNotify  = wmApp + 1
	wmTrayRefresh = wmApp + 2
	wmTrayQuit    = wmApp + 3

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	mfString    = 0x0
	mfGrayed    = 0x1
	mfChecked   = 0x8
	mfPopup     = 0x10
	mfSeparator = 0x800

	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	wsOverlappedWindow = 0x00CF0000
)

type notifyIconData struct {
	CbSize          uint32
	Wnd             windows.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32 // union with uTimeout
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        windows.GUID
	BalloonIcon     windows.Handle
}

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}

type winMsg struct {
	Hwnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       struct{ X, Y int32 }
	LPrivate uint32
}

// Compile-time checks of the C struct sizes on 64-bit Windows.
var (
	_ [976 - unsafe.Sizeof(notifyIconData{})]byte
	_ [unsafe.Sizeof(notifyIconData{}) - 976]byte
	_ [80 - unsafe.Sizeof(wndClassEx{})]byte
	_ [unsafe.Sizeof(wndClassEx{}) - 80]byte
	_ [48 - unsafe.Sizeof(winMsg{})]byte
	_ [unsafe.Sizeof(winMsg{}) - 48]byte
)

// MenuItem describes one line of a popup menu.
type MenuItem struct {
	Title     string
	Disabled  bool
	Checked   bool
	Separator bool
	Action    func()
	Sub       []MenuItem
}

type trayIcon struct {
	id     uint32
	tip    string
	ico    []byte
	dirty  bool
	hicon  windows.Handle
	added  bool
	menu   func() []MenuItem
	onLeft func() // left click; nil = show the menu
}

type Tray struct {
	hwnd           windows.Handle
	taskbarCreated uint32
	mu             sync.Mutex
	icons          []*trayIcon
	ready          chan struct{}
}

var theTray *Tray // window procedure needs it

// NewTray prepares the icons. menu builders run on the UI thread.
func NewTray() *Tray { return &Tray{ready: make(chan struct{})} }

func (t *Tray) AddIcon(id uint32, tip string, ico []byte, menu func() []MenuItem, onLeft func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.icons = append(t.icons, &trayIcon{id: id, tip: tip, ico: ico, dirty: true, menu: menu, onLeft: onLeft})
}

// Set updates an icon from any goroutine.
func (t *Tray) Set(id uint32, tip string, ico []byte) {
	t.mu.Lock()
	for _, ic := range t.icons {
		if ic.id == id {
			ic.tip, ic.ico, ic.dirty = tip, ico, true
		}
	}
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), wmTrayRefresh, 0, 0)
	}
}

// Quit closes the tray from any goroutine.
func (t *Tray) Quit() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), wmTrayQuit, 0, 0)
	}
}

// Run creates the window and pumps messages until Quit. Blocks.
func (t *Tray) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	theTray = t

	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("BlackSharkTrayWindow")
	wc := wndClassEx{
		WndProc:   windows.NewCallback(trayWndProc),
		Instance:  windows.Handle(inst),
		ClassName: cls,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}
	name, _ := windows.UTF16PtrFromString("BlackSharkTray")
	h, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)),
		wsOverlappedWindow, 0, 0, 0, 0, 0, 0, inst, 0)
	if h == 0 {
		return err
	}
	tb, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tb)))

	t.mu.Lock()
	t.hwnd = windows.Handle(h)
	t.taskbarCreated = uint32(r)
	t.mu.Unlock()
	t.refresh()
	close(t.ready)
	go t.retryMissing()

	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	t := theTray
	switch uint32(msg) {
	case wmTrayNotify:
		switch uint32(lparam) & 0xFFFF {
		case wmLButtonUp:
			if f := t.leftAction(uint32(wparam)); f != nil {
				f()
			} else {
				t.showMenu(uint32(wparam))
			}
		case wmRButtonUp:
			t.showMenu(uint32(wparam))
		}
		return 0
	case wmTrayRefresh:
		t.refresh()
		return 0
	case wmTrayQuit, wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmEndSession:
		if wparam != 0 {
			t.removeAll()
		}
		return 0
	case wmDestroy:
		t.removeAll()
		procPostQuitMessage.Call(0)
		return 0
	}
	if t != nil && t.taskbarCreated != 0 && uint32(msg) == t.taskbarCreated {
		// Explorer restarted: the icons must be added again.
		t.mu.Lock()
		for _, ic := range t.icons {
			ic.added, ic.dirty = false, true
		}
		t.mu.Unlock()
		t.refresh()
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func setUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)-1]
		u = append(u, 0)
	}
	copy(dst, u)
}

func iconHandle(ico []byte) windows.Handle {
	if len(ico) <= 22 {
		return 0
	}
	bits := ico[22:] // skip ICONDIR + ICONDIRENTRY, CreateIconFromResourceEx wants the image itself
	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&bits[0])), uintptr(len(bits)), 1, 0x00030000, 0, 0, 0)
	return windows.Handle(h)
}

// retryMissing asks for a refresh while some icon is not in the tray yet
// (at logon the shell may not be ready when the tray starts).
func (t *Tray) retryMissing() {
	for range time.Tick(10 * time.Second) {
		t.mu.Lock()
		missing := false
		for _, ic := range t.icons {
			missing = missing || !ic.added
		}
		hwnd := t.hwnd
		t.mu.Unlock()
		if missing && hwnd != 0 {
			procPostMessageW.Call(uintptr(hwnd), wmTrayRefresh, 0, 0)
		}
	}
}

// refresh runs on the UI thread: adds missing icons, updates changed ones.
func (t *Tray) refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ic := range t.icons {
		if !ic.dirty && ic.added {
			continue
		}
		newIcon := iconHandle(ic.ico)
		nid := notifyIconData{Wnd: t.hwnd, ID: ic.id, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTrayNotify, Icon: newIcon}
		nid.CbSize = uint32(unsafe.Sizeof(nid))
		setUTF16(nid.Tip[:], ic.tip)
		op := uintptr(nimModify)
		if !ic.added {
			op = nimAdd
		}
		r, _, _ := procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid)))
		if r == 0 && op == nimModify {
			// The shell may have lost it: try adding again.
			op = nimAdd
			r, _, _ = procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		}
		if r != 0 && op == nimAdd {
			// Some shells (Wine) only draw a new icon after a modify.
			procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
		}
		if r != 0 {
			ic.added, ic.dirty = true, false
			if ic.hicon != 0 {
				procDestroyIcon.Call(uintptr(ic.hicon))
			}
			ic.hicon = newIcon
		} else if newIcon != 0 {
			procDestroyIcon.Call(uintptr(newIcon)) // shell not ready (early autostart): next refresh retries
		}
	}
}

func (t *Tray) removeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ic := range t.icons {
		if ic.added {
			nid := notifyIconData{Wnd: t.hwnd, ID: ic.id}
			nid.CbSize = uint32(unsafe.Sizeof(nid))
			procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
			ic.added = false
		}
	}
}

func (t *Tray) leftAction(id uint32) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ic := range t.icons {
		if ic.id == id {
			return ic.onLeft
		}
	}
	return nil
}

func (t *Tray) showMenu(id uint32) {
	var build func() []MenuItem
	t.mu.Lock()
	for _, ic := range t.icons {
		if ic.id == id {
			build = ic.menu
		}
	}
	hwnd := t.hwnd
	t.mu.Unlock()
	if build == nil {
		return
	}
	hm, actions := buildMenu(build())
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(hm, tpmReturnCmd|tpmNoNotify|tpmRightButton|tpmBottomAlign,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	procPostMessageW.Call(uintptr(hwnd), wmNull, 0, 0) // KB135788: lets the menu close properly
	procDestroyMenu.Call(hm)
	if f := actions[cmd]; f != nil {
		f()
	}
}

// buildMenu creates a popup menu; ids map to the actions.
func buildMenu(items []MenuItem) (uintptr, map[uintptr]func()) {
	actions := map[uintptr]func(){}
	next := uintptr(1)
	var mk func(items []MenuItem) uintptr
	mk = func(items []MenuItem) uintptr {
		hm, _, _ := procCreatePopupMenu.Call()
		for _, it := range items {
			if it.Separator {
				procAppendMenuW.Call(hm, mfSeparator, 0, 0)
				continue
			}
			title, _ := windows.UTF16PtrFromString(it.Title)
			flags := uintptr(mfString)
			if it.Disabled {
				flags |= mfGrayed
			}
			if it.Checked {
				flags |= mfChecked
			}
			if len(it.Sub) > 0 {
				sub := mk(it.Sub)
				procAppendMenuW.Call(hm, flags|mfPopup, sub, uintptr(unsafe.Pointer(title)))
				continue
			}
			cmd := next
			next++
			if it.Action != nil {
				actions[cmd] = it.Action
			}
			procAppendMenuW.Call(hm, flags, cmd, uintptr(unsafe.Pointer(title)))
		}
		return hm
	}
	return mk(items), actions
}

// Added reports whether the shell accepted an icon (for the self test).
func (t *Tray) Added(id uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ic := range t.icons {
		if ic.id == id {
			return ic.added
		}
	}
	return false
}
