//go:build windows

package donglereset

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNoDongle: the dongle is not plugged in.
var ErrNoDongle = errors.New("донгл BlackShark не найден среди USB-устройств")

// InstanceID returns the device instance ID of the plugged-in dongle.
func InstanceID() (string, error) {
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_PRESENT|windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return "", fmt.Errorf("SetupDiGetClassDevsEx: %w", err)
	}
	defer set.Close()
	for i := 0; ; i++ {
		d, err := set.EnumDeviceInfo(i)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				return "", ErrNoDongle
			}
			continue
		}
		if id, err := set.DeviceInstanceID(d); err == nil && IsRoot(id) {
			return id, nil
		}
	}
}

func system32(exe string) string {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		dir = `C:\Windows\System32`
	}
	return filepath.Join(dir, exe)
}

// schtasks runs schtasks.exe without a console window (the tray has none).
func schtasks(args ...string) (string, error) {
	cmd := exec.Command(system32("schtasks.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Installed reports whether the task exists.
func Installed() bool {
	_, err := schtasks("/query", "/tn", TaskName)
	return err == nil
}

// Run starts the task; the dongle disappears and comes back within seconds.
func Run() error {
	if out, err := schtasks("/run", "/tn", TaskName); err != nil {
		return fmt.Errorf("schtasks /run: %v %s", err, out)
	}
	return nil
}

// Enable creates (or replaces) the task for the plugged-in dongle. Shows a UAC prompt.
func Enable() error {
	id, err := InstanceID()
	if err != nil {
		return err
	}
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return fmt.Errorf("пользователь: %w", err)
	}
	f, err := os.CreateTemp("", "bsctl-task-*.xml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_, err = f.Write(utf16LE(TaskXML(u.User.Sid.String(), id)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := runElevated(system32("schtasks.exe"), `/create /tn "`+TaskName+`" /xml "`+path+`" /f`); err != nil {
		return err
	}
	if !Installed() {
		return errors.New("задача не создалась")
	}
	return nil
}

// Disable removes the task. Removing a task made with elevation may need it too,
// so a refused plain attempt is retried with a UAC prompt when elevate is set.
func Disable(elevate bool) error {
	if !Installed() {
		return nil
	}
	if _, err := schtasks("/delete", "/tn", TaskName, "/f"); err == nil || !elevate {
		return err
	}
	if err := runElevated(system32("schtasks.exe"), `/delete /tn "`+TaskName+`" /f`); err != nil {
		return err
	}
	if Installed() {
		return errors.New("задача не удалилась")
	}
	return nil
}

// RestartNow restarts the dongle directly with a UAC prompt (for when the task is off).
func RestartNow() error {
	id, err := InstanceID()
	if err != nil {
		return err
	}
	return runElevated(system32("cmd.exe"), `/d /s /c "`+Command(id)+`"`)
}

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(strings.ReplaceAll(s, "\n", "\r\n")))
	b := make([]byte, 2, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE
	for _, c := range u {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return b
}

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize      uint32
	fMask       uint32
	hwnd        uintptr
	verb        *uint16
	file        *uint16
	parameters  *uint16
	directory   *uint16
	show        int32
	instApp     uintptr
	idList      uintptr
	class       *uint16
	keyClass    uintptr
	hotKey      uint32
	iconMonitor uintptr
	process     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
)

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevated starts exe with a UAC prompt, waits for it and checks the exit code.
func runElevated(exe, args string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	sei := shellExecuteInfo{
		fMask:      seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		verb:       verb,
		file:       file,
		parameters: params,
		show:       windows.SW_HIDE,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	if r, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("отменено в окне UAC")
		}
		return fmt.Errorf("запуск с правами администратора: %v", err)
	}
	if sei.process == 0 {
		return errors.New("запуск с правами администратора: нет процесса")
	}
	defer windows.CloseHandle(sei.process)
	if _, err := windows.WaitForSingleObject(sei.process, 60_000); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s завершился с кодом %d", filepath.Base(exe), code)
	}
	return nil
}
