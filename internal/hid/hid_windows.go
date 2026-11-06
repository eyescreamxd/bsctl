//go:build windows

package hid

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modHid = windows.NewLazySystemDLL("hid.dll")

	procHidDGetHidGuid        = modHid.NewProc("HidD_GetHidGuid")
	procHidDGetAttributes     = modHid.NewProc("HidD_GetAttributes")
	procHidDGetPreparsedData  = modHid.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData = modHid.NewProc("HidD_FreePreparsedData")
	procHidPGetCaps           = modHid.NewProc("HidP_GetCaps")
	procHidDGetProductString  = modHid.NewProc("HidD_GetProductString")
	procHidDSetOutputReport   = modHid.NewProc("HidD_SetOutputReport")
	procHidDFlushQueue        = modHid.NewProc("HidD_FlushQueue")
	procHidDSetFeature        = modHid.NewProc("HidD_SetFeature")
	procHidDGetFeature        = modHid.NewProc("HidD_GetFeature")
)

const (
	hidpStatusSuccess = 0x00110000
	waitObject0       = 0x00000000
)

var ErrTimeout = errors.New("timeout")

type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
	_             uint16
}

type hidpCaps struct {
	Usage                     uint16
	UsagePage                 uint16
	InputReportByteLength     uint16
	OutputReportByteLength    uint16
	FeatureReportByteLength   uint16
	Reserved                  [17]uint16
	NumberLinkCollectionNodes uint16
	NumberInputButtonCaps     uint16
	NumberInputValueCaps      uint16
	NumberInputDataIndices    uint16
	NumberOutputButtonCaps    uint16
	NumberOutputValueCaps     uint16
	NumberOutputDataIndices   uint16
	NumberFeatureButtonCaps   uint16
	NumberFeatureValueCaps    uint16
	NumberFeatureDataIndices  uint16
}

// HIDInfo describes one HID top-level collection of the dongle.
type HIDInfo struct {
	Path      string
	VendorID  uint16
	ProductID uint16
	UsagePage uint16
	Usage     uint16
	InLen     int
	OutLen    int
	FeatLen   int
	Product   string
	Err       string
}

func (i HIDInfo) String() string {
	s := fmt.Sprintf("usage %04X:%04X in=%d out=%d feature=%d %q", i.UsagePage, i.Usage, i.InLen, i.OutLen, i.FeatLen, i.Product)
	if i.Err != "" {
		s += " ERR: " + i.Err
	}
	return s + "\n      " + i.Path
}

// Enumerate lists the HID collections of vid:pid that are present.
func Enumerate(vid, pid uint16) ([]HIDInfo, error) {
	var g windows.GUID
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&g)))
	paths, err := windows.CM_Get_Device_Interface_List("", &g, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, fmt.Errorf("CM_Get_Device_Interface_List: %w", err)
	}
	needle := fmt.Sprintf("vid_%04x&pid_%04x", vid, pid)
	var out []HIDInfo
	for _, p := range paths {
		if !strings.Contains(strings.ToLower(p), needle) {
			continue
		}
		info := HIDInfo{Path: p}
		h, err := openPath(p, 0)
		if err != nil {
			info.Err = err.Error()
			out = append(out, info)
			continue
		}
		fillInfo(h, &info)
		windows.CloseHandle(h)
		out = append(out, info)
	}
	return out, nil
}

func openPath(path string, access uint32) (windows.Handle, error) {
	return openPathFlags(path, access, windows.FILE_FLAG_OVERLAPPED)
}

func openPathFlags(path string, access, flags uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, flags, 0)
}

func fillInfo(h windows.Handle, info *HIDInfo) {
	var a hiddAttributes
	a.Size = uint32(unsafe.Sizeof(a))
	if r, _, _ := procHidDGetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&a))); r != 0 {
		info.VendorID, info.ProductID = a.VendorID, a.ProductID
	}
	var ppd uintptr
	if r, _, _ := procHidDGetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&ppd))); r != 0 {
		var c hidpCaps
		if st, _, _ := procHidPGetCaps.Call(ppd, uintptr(unsafe.Pointer(&c))); uint32(st) == hidpStatusSuccess {
			info.UsagePage, info.Usage = c.UsagePage, c.Usage
			info.InLen, info.OutLen, info.FeatLen = int(c.InputReportByteLength), int(c.OutputReportByteLength), int(c.FeatureReportByteLength)
		}
		procHidDFreePreparsedData.Call(ppd)
	}
	buf := make([]uint16, 128)
	if r, _, _ := procHidDGetProductString.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2)); r != 0 {
		info.Product = windows.UTF16ToString(buf)
	}
}

// Device is an open HID collection. Buffers and OVERLAPPED structs live on the
// heap inside the struct so the kernel can write into them asynchronously.
type Device struct {
	Info         HIDInfo
	h            windows.Handle
	rbuf, wbuf   []byte
	rov, wov     *windows.Overlapped
	UseSetOutput bool
}

// OpenDevice opens a collection for overlapped reads and writes.
func OpenDevice(info HIDInfo) (*Device, error) {
	h, err := openPath(info.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return nil, err
	}
	re, err1 := windows.CreateEvent(nil, 1, 0, nil)
	we, err2 := windows.CreateEvent(nil, 1, 0, nil)
	if err1 != nil || err2 != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("CreateEvent: %v %v", err1, err2)
	}
	d := &Device{
		Info: info,
		h:    h,
		rbuf: make([]byte, max(info.InLen, 64)),
		wbuf: make([]byte, max(info.OutLen, 64)),
		rov:  &windows.Overlapped{HEvent: re},
		wov:  &windows.Overlapped{HEvent: we},
	}
	return d, nil
}

// OpenReadOnly opens a collection just to read its input reports (events).
func OpenReadOnly(info HIDInfo) (*Device, error) {
	h, err := openPath(info.Path, windows.GENERIC_READ)
	if err != nil {
		return nil, err
	}
	re, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &Device{Info: info, h: h, rbuf: make([]byte, max(info.InLen, 64)), rov: &windows.Overlapped{HEvent: re}}, nil
}

// OpenFeature opens a collection for feature reports only (synchronous handle).
// Windows keeps mouse and keyboard collections for itself, so if read/write
// access is refused, open with access 0: HidD_SetFeature/HidD_GetFeature still work.
func OpenFeature(info HIDInfo) (*Device, error) {
	h, err := openPathFlags(info.Path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0)
	if err != nil {
		h, err = openPathFlags(info.Path, 0, 0)
	}
	if err != nil {
		return nil, err
	}
	return &Device{Info: info, h: h}, nil
}

func (d *Device) Close() {
	if d == nil || d.h == windows.InvalidHandle {
		return
	}
	windows.CancelIoEx(d.h, nil)
	windows.CloseHandle(d.h)
	if d.rov != nil {
		windows.CloseHandle(d.rov.HEvent)
	}
	if d.wov != nil {
		windows.CloseHandle(d.wov.HEvent)
	}
	d.h = windows.InvalidHandle
}

// CancelIO aborts a pending ReadFile (used to stop a reader goroutine).
func (d *Device) CancelIO() { windows.CancelIoEx(d.h, nil) }

// SetFeature sends a feature report; buf[0] is the report ID.
func (d *Device) SetFeature(buf []byte) error {
	r, _, e := procHidDSetFeature.Call(uintptr(d.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return fmt.Errorf("HidD_SetFeature: %v", e)
	}
	return nil
}

// GetFeature reads a feature report into buf; set buf[0] to the report ID first.
func (d *Device) GetFeature(buf []byte) error {
	r, _, e := procHidDGetFeature.Call(uintptr(d.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return fmt.Errorf("HidD_GetFeature: %v", e)
	}
	return nil
}

func (d *Device) Flush() { procHidDFlushQueue.Call(uintptr(d.h)) }

func (d *Device) Write(pkt []byte) error {
	for i := range d.wbuf {
		d.wbuf[i] = 0
	}
	copy(d.wbuf, pkt)
	if d.UseSetOutput {
		r, _, e := procHidDSetOutputReport.Call(uintptr(d.h), uintptr(unsafe.Pointer(&d.wbuf[0])), uintptr(len(d.wbuf)))
		if r == 0 {
			return fmt.Errorf("HidD_SetOutputReport: %v", e)
		}
		return nil
	}
	windows.ResetEvent(d.wov.HEvent)
	var n uint32
	err := windows.WriteFile(d.h, d.wbuf, &n, d.wov)
	if err == windows.ERROR_IO_PENDING {
		ev, _ := windows.WaitForSingleObject(d.wov.HEvent, 1500)
		if ev != waitObject0 {
			windows.CancelIoEx(d.h, d.wov)
			windows.GetOverlappedResult(d.h, d.wov, &n, true)
			return fmt.Errorf("WriteFile: %w", ErrTimeout)
		}
		err = windows.GetOverlappedResult(d.h, d.wov, &n, false)
	}
	if err != nil {
		return fmt.Errorf("WriteFile: %w", err)
	}
	return nil
}

func (d *Device) Read(timeout time.Duration) ([]byte, error) {
	windows.ResetEvent(d.rov.HEvent)
	var n uint32
	err := windows.ReadFile(d.h, d.rbuf, &n, d.rov)
	if err == windows.ERROR_IO_PENDING {
		ms := uint32(timeout / time.Millisecond)
		if ms == 0 {
			ms = 1
		}
		ev, _ := windows.WaitForSingleObject(d.rov.HEvent, ms)
		if ev != waitObject0 {
			windows.CancelIoEx(d.h, d.rov)
			// A report may have arrived right before the cancel: keep it.
			if e := windows.GetOverlappedResult(d.h, d.rov, &n, true); e != nil || n == 0 {
				return nil, ErrTimeout
			}
			err = nil
		} else {
			err = windows.GetOverlappedResult(d.h, d.rov, &n, false)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("ReadFile: %w", err)
	}
	out := make([]byte, n)
	copy(out, d.rbuf[:n])
	return out, nil
}

// ProcessRunning reports whether a process with this exe name runs (Synapse check).
func ProcessRunning(name string) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), name) {
			return true
		}
	}
	return false
}
