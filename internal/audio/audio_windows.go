//go:build windows

// Package audio keeps the headset microphone open (recording into nowhere) while
// sidetone is on. Synapse does the same (THX "StartCapture" when sidetone is enabled):
// the wireless headset only sends the mic signal when someone records it, and without
// that signal there is nothing to play back as sidetone.
package audio

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWinmm                  = windows.NewLazySystemDLL("winmm.dll")
	procWaveInGetNumDevs      = modWinmm.NewProc("waveInGetNumDevs")
	procWaveInGetDevCapsW     = modWinmm.NewProc("waveInGetDevCapsW")
	procWaveInOpen            = modWinmm.NewProc("waveInOpen")
	procWaveInPrepareHeader   = modWinmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader = modWinmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer       = modWinmm.NewProc("waveInAddBuffer")
	procWaveInStart           = modWinmm.NewProc("waveInStart")
	procWaveInReset           = modWinmm.NewProc("waveInReset")
	procWaveInClose           = modWinmm.NewProc("waveInClose")
)

const (
	callbackEvent = 0x00050000
	waveFormatPCM = 1
	whdrDone      = 0x1
	micBuffers    = 4
)

type waveInCaps struct {
	Mid, Pid      uint16
	DriverVersion uint32
	Pname         [32]uint16
	Formats       uint32
	Channels      uint16
	Reserved1     uint16
}

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

var (
	_ [80 - unsafe.Sizeof(waveInCaps{})]byte
	_ [unsafe.Sizeof(waveInCaps{}) - 80]byte
	_ [48 - unsafe.Sizeof(waveHdr{})]byte
	_ [unsafe.Sizeof(waveHdr{}) - 48]byte
)

// findHeadsetMic returns the waveIn device of the BlackShark microphone.
// Names are cut to 31 characters, e.g. "Микрофон (Razer BlackShark V2 P".
func findHeadsetMic() (int, string, error) {
	n, _, _ := procWaveInGetNumDevs.Call()
	var names []string
	for i := 0; i < int(n); i++ {
		var c waveInCaps
		if r, _, _ := procWaveInGetDevCapsW.Call(uintptr(i), uintptr(unsafe.Pointer(&c)), unsafe.Sizeof(c)); r != 0 {
			continue
		}
		name := windows.UTF16ToString(c.Pname[:])
		names = append(names, name)
		if strings.Contains(strings.ToLower(name), micMatch()) {
			return i, name, nil
		}
	}
	return -1, "", fmt.Errorf("микрофон BlackShark не найден среди: %s", strings.Join(names, "; "))
}

// micMatch: part of the device name to look for (BSCTL_MIC_NAME overrides, for odd names).
func micMatch() string {
	if v := strings.TrimSpace(os.Getenv("BSCTL_MIC_NAME")); v != "" {
		return strings.ToLower(v)
	}
	return "blackshark"
}

// MicHold is an open recording of the headset mic; Close releases it.
type MicHold struct {
	h    uintptr
	ev   windows.Handle
	hdrs [micBuffers]*waveHdr
	bufs [micBuffers][]byte
	stop chan struct{}
	done chan struct{}
	Name string
	Bufs int // filled buffers handed back (diagnostics)
}

// OpenMicHold starts recording from the headset mic and throws the sound away.
func OpenMicHold() (*MicHold, error) {
	id, name, err := findHeadsetMic()
	if err != nil {
		return nil, err
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	m := &MicHold{ev: ev, stop: make(chan struct{}), done: make(chan struct{}), Name: name}
	wf := waveFormatEx{FormatTag: waveFormatPCM, Channels: 1, SamplesPerSec: 48000, AvgBytesPerSec: 96000, BlockAlign: 2, BitsPerSample: 16}
	if r, _, _ := procWaveInOpen.Call(uintptr(unsafe.Pointer(&m.h)), uintptr(id), uintptr(unsafe.Pointer(&wf)), uintptr(ev), 0, callbackEvent); r != 0 {
		windows.CloseHandle(ev)
		return nil, fmt.Errorf("waveInOpen(%s): MMRESULT %d", name, r)
	}
	for i := range m.hdrs {
		m.bufs[i] = make([]byte, 9600) // 100 ms
		m.hdrs[i] = &waveHdr{Data: uintptr(unsafe.Pointer(&m.bufs[i][0])), BufferLength: uint32(len(m.bufs[i]))}
		procWaveInPrepareHeader.Call(m.h, uintptr(unsafe.Pointer(m.hdrs[i])), unsafe.Sizeof(waveHdr{}))
		procWaveInAddBuffer.Call(m.h, uintptr(unsafe.Pointer(m.hdrs[i])), unsafe.Sizeof(waveHdr{}))
	}
	if r, _, _ := procWaveInStart.Call(m.h); r != 0 {
		m.release()
		return nil, fmt.Errorf("waveInStart(%s): MMRESULT %d", name, r)
	}
	go m.loop()
	return m, nil
}

// loop hands filled buffers back to the driver.
func (m *MicHold) loop() {
	defer close(m.done)
	for {
		windows.WaitForSingleObject(m.ev, 500)
		select {
		case <-m.stop:
			return
		default:
		}
		for _, h := range m.hdrs {
			if h.Flags&whdrDone != 0 {
				h.Flags &^= whdrDone
				m.Bufs++
				procWaveInAddBuffer.Call(m.h, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(waveHdr{}))
			}
		}
	}
}

func (m *MicHold) release() {
	procWaveInReset.Call(m.h)
	for _, h := range m.hdrs {
		if h != nil {
			procWaveInUnprepareHeader.Call(m.h, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(waveHdr{}))
		}
	}
	procWaveInClose.Call(m.h)
	windows.CloseHandle(m.ev)
}

func (m *MicHold) Close() {
	close(m.stop)
	windows.SetEvent(m.ev)
	<-m.done
	m.release()
}
