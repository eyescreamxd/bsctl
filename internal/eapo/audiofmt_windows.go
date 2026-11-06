//go:build windows

package eapo

// Device format of an audio endpoint through the MMDevice API (the same value the
// Sound settings show), so we do not depend on how Windows lays it out in the registry.

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procPropVariantClear = modOle32.NewProc("PropVariantClear")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}

	pkeyAudioEngineDeviceFormat = propertyKey{
		fmtid: windows.GUID{Data1: 0xf19f064d, Data2: 0x082c, Data3: 0x4e27, Data4: [8]byte{0xbc, 0x73, 0x68, 0x82, 0xa1, 0xbb, 0x8e, 0x4c}},
		pid:   0,
	}
)

const (
	clsctxAll = 0x17
	stgmRead  = 0
	vtBlob    = 0x41

	// vtable slots
	enumGetDevice     = 5                   // IMMDeviceEnumerator::GetDevice
	devOpenPropStore  = 4                   // IMMDevice::OpenPropertyStore
	storeGetValue     = 5                   // IPropertyStore::GetValue
	unknownRelease    = 2                   // IUnknown::Release
	captureEndpointID = "{0.0.1.00000000}." // + endpoint GUID
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT on 64-bit Windows, blob member only.
type propVariant struct {
	vt       uint16
	_        [3]uint16
	blobSize uint32
	_        uint32
	blobData *byte
}

// Size checks: PROPVARIANT is 24 bytes and PROPERTYKEY 20 on amd64.
var (
	_ [24 - unsafe.Sizeof(propVariant{})]byte
	_ [unsafe.Sizeof(propVariant{}) - 24]byte
	_ [20 - unsafe.Sizeof(propertyKey{})]byte
)

// comObject is any COM interface pointer; only the first vtable slots are used.
type comObject struct{ vtbl *[8]uintptr }

func (o *comObject) release() {
	if o != nil {
		syscall.SyscallN(o.vtbl[unknownRelease], uintptr(unsafe.Pointer(o)))
	}
}

func hresultErr(what string, r uintptr) error {
	return fmt.Errorf("%s: 0x%08X", what, uint32(r))
}

// captureDeviceFormat returns the shared-mode format of a capture endpoint
// (guid as in the MMDevices registry, "{...}").
func captureDeviceFormat(guid string) (f waveFormat, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch e := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); {
	case e == nil || e == syscall.Errno(1): // S_OK, S_FALSE
		defer windows.CoUninitialize()
	case errors.Is(e, syscall.Errno(windows.RPC_E_CHANGED_MODE)): // COM is already up on this thread
	default:
		return f, fmt.Errorf("CoInitializeEx: %v", e)
	}

	var enum *comObject
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if int32(r) < 0 || enum == nil {
		return f, hresultErr("MMDeviceEnumerator", r)
	}
	defer enum.release()

	id, err := windows.UTF16PtrFromString(captureEndpointID + guid)
	if err != nil {
		return f, err
	}
	var dev *comObject
	r, _, _ = syscall.SyscallN(enum.vtbl[enumGetDevice], uintptr(unsafe.Pointer(enum)), uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(&dev)))
	if int32(r) < 0 || dev == nil {
		return f, hresultErr("GetDevice", r)
	}
	defer dev.release()

	var store *comObject
	r, _, _ = syscall.SyscallN(dev.vtbl[devOpenPropStore], uintptr(unsafe.Pointer(dev)), stgmRead, uintptr(unsafe.Pointer(&store)))
	if int32(r) < 0 || store == nil {
		return f, hresultErr("OpenPropertyStore", r)
	}
	defer store.release()

	pv := new(propVariant)
	r, _, _ = syscall.SyscallN(store.vtbl[storeGetValue], uintptr(unsafe.Pointer(store)), uintptr(unsafe.Pointer(&pkeyAudioEngineDeviceFormat)), uintptr(unsafe.Pointer(pv)))
	if int32(r) < 0 {
		return f, hresultErr("GetValue(DeviceFormat)", r)
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(pv)))
	if pv.vt != vtBlob || pv.blobData == nil || pv.blobSize < 16 || pv.blobSize > 4096 {
		return f, fmt.Errorf("формат не задан (vt=0x%X, %d байт)", pv.vt, pv.blobSize)
	}
	blob := append([]byte(nil), unsafe.Slice(pv.blobData, pv.blobSize)...)
	if f, ok := parseWaveFormat(blob[:16]); ok {
		return f, nil
	}
	return f, fmt.Errorf("непонятный формат: %s", HexBytes(blob, 24))
}
