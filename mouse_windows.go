//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// rzLink is one HID collection of a Razer device that accepts 90-byte feature reports.
// Through the Mouse Dock Pro both the dock and the mouse are reached on the same
// collection; the transaction id tells the dock who the command is for.
type rzLink struct {
	dev     *Device
	pid     uint16
	log     *log.Logger
	Verbose bool

	mu  sync.Mutex
	seq byte
}

var errRzOffline = errors.New("устройство не отвечает")

type rzError struct {
	cmd    rzCmd
	status byte
}

func (e *rzError) Error() string { return fmt.Sprintf("%s: %s", e.cmd.Name, rzStatusText(e.status)) }

// mouseOffline: the dock answered for the mouse that it could not reach it.
func mouseOffline(err error) bool {
	var re *rzError
	if errors.As(err, &re) {
		return re.status == rzStatusTimeout || re.status == rzStatusFailed
	}
	return errors.Is(err, errRzOffline)
}

func pidName(pid uint16) string {
	switch pid {
	case pidMouseDock:
		return "Mouse Dock Pro"
	case pidBasiliskW:
		return "Basilisk V3 Pro (кабель)"
	case pidBasiliskRx:
		return "Basilisk V3 Pro (свой донгл)"
	}
	return fmt.Sprintf("1532:%04X", pid)
}

// cmd sends one command and polls for its answer. toDock picks the dock's own
// transaction ids (0xE0|n); otherwise the command goes to the mouse. If the
// device keeps answering with someone else's transaction id, the command is
// sent once more (Synapse does the same).
func (l *rzLink) cmd(toDock bool, c rzCmd, size byte, args []byte, timeout time.Duration) (rzResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := max(1+razerReportLen, l.dev.Info.FeatLen)
	out := make([]byte, n) // report ID 0 + report
	in := make([]byte, n)
	var last rzResponse
	for attempt := 1; attempt <= 2; attempt++ {
		tid := l.seq & 0x1F
		l.seq++
		if toDock {
			tid |= tidDockFlag
		}
		pkt := rzPacket(tid, c, size, args)
		clear(out)
		copy(out[1:], pkt)
		if l.Verbose {
			l.log.Printf("  SET %s", hexs(trimZeros(pkt[:12])))
		}
		if err := l.dev.SetFeature(out); err != nil {
			return rzResponse{}, err
		}
		deadline := time.Now().Add(timeout)
		strangers := 0
		for time.Now().Before(deadline) && strangers < 4 {
			time.Sleep(8 * time.Millisecond)
			clear(in)
			if err := l.dev.GetFeature(in); err != nil {
				return rzResponse{}, err
			}
			r, _ := parseRz(in[1:])
			last = r
			if l.Verbose {
				l.log.Printf("  GET %s", hexs(in[1:9]))
			}
			if r.TID != tid || r.Class != c.Class || r.ID != c.ID {
				strangers++ // an old answer or someone else's: wait a bit, then resend
				continue
			}
			switch r.Status {
			case rzStatusOK:
				return r, nil
			case rzStatusBusy, rzStatusNew:
				continue
			}
			return r, &rzError{c, r.Status}
		}
		if strangers < 4 {
			break // ran out of time with our command still busy
		}
	}
	return last, fmt.Errorf("%s: %w", c.Name, errRzOffline)
}

func (l *rzLink) Close() { l.dev.Close() }

func (l *rzLink) Firmware(toDock bool) (string, error) {
	r, err := l.cmd(toDock, rzGetFirmware, 0, nil, 400*time.Millisecond)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d.%d", r.Args[0], r.Args[1]), nil
}

func (l *rzLink) Serial(toDock bool) (string, error) {
	r, err := l.cmd(toDock, rzGetSerial, 0, nil, 600*time.Millisecond)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(string(r.Args[:22])), "\x00"), nil
}

// BatteryLevel asks the mouse battery, percent.
func (l *rzLink) BatteryLevel() (int, error) {
	r, err := l.cmd(false, rzGetBattery, 0, nil, 900*time.Millisecond)
	if err != nil {
		return -1, err
	}
	return batteryPercent(r.Args[1]), nil
}

// Charging asks whether the mouse is charging.
func (l *rzLink) Charging() (bool, error) {
	r, err := l.cmd(false, rzGetCharging, 0, nil, 900*time.Millisecond)
	if err != nil {
		return false, err
	}
	return r.Args[1] != 0, nil
}

func (l *rzLink) DockEffect(effect byte, colors ...[3]byte) error {
	args := effectArgs(effect, colors...)
	_, err := l.cmd(true, rzSetEffect, byte(len(args)), args, 400*time.Millisecond)
	return err
}

func (l *rzLink) DockBrightness(percent int) error {
	percent = max(0, min(100, percent))
	_, err := l.cmd(true, rzSetBrightness, 0, []byte{0, 0, byte(percent * 255 / 100)}, 400*time.Millisecond)
	return err
}

func (l *rzLink) DockBrightnessGet() (int, error) {
	r, err := l.cmd(true, rzGetBrightness, 0, []byte{0, 0}, 400*time.Millisecond)
	if err != nil {
		return -1, err
	}
	return (int(r.Args[2])*100 + 127) / 255, nil
}

// SetDeviceMode: 0 normal, 3 "driver" (Synapse keeps the dock in 3 while it runs).
func (l *rzLink) SetDeviceMode(toDock bool, mode byte) error {
	_, err := l.cmd(toDock, rzSetDeviceMode, 0, []byte{mode, 0}, 400*time.Millisecond)
	return err
}

func (l *rzLink) DeviceMode(toDock bool) (string, error) {
	r, err := l.cmd(toDock, rzGetDeviceMode, 0, nil, 400*time.Millisecond)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%d", r.Args[0], r.Args[1]), nil
}

// openRzLink finds the collection of 1532:pid that answers a firmware request.
// For the dock the request goes to the dock itself, otherwise to the mouse.
func openRzLink(pid uint16, lg *log.Logger, verbose bool) (*rzLink, error) {
	infos, err := enumerateHID(razerVID, pid)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, errNotFound
	}
	var tried []string
	for _, info := range infos {
		if info.FeatLen < 1+razerReportLen {
			continue
		}
		dev, err := openFeature(info)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%04X:%04X open: %v", info.UsagePage, info.Usage, err))
			continue
		}
		l := &rzLink{dev: dev, pid: pid, log: lg, Verbose: verbose}
		if _, err := l.Firmware(pid == pidMouseDock); err == nil {
			return l, nil
		} else {
			tried = append(tried, fmt.Sprintf("%04X:%04X: %v", info.UsagePage, info.Usage, err))
		}
		dev.Close()
	}
	if len(tried) == 0 {
		return nil, fmt.Errorf("%s: нет коллекции с feature-отчётом 91 байт", pidName(pid))
	}
	return nil, fmt.Errorf("%s не отвечает: %s", pidName(pid), strings.Join(tried, "; "))
}

var errNotFound = errors.New("не найдено")

// dockEvents reads input report 5 (battery, charging, radio events) from every
// collection of the dock that Windows lets us open. Returns a stop function.
func dockEvents(lg *log.Logger, verbose bool, out chan<- dockEvent) (func(), int) {
	infos, err := enumerateHID(razerVID, pidMouseDock)
	if err != nil {
		return func() {}, 0
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	var devs []*Device
	for _, info := range infos {
		if info.InLen < 16 || info.InLen > 64 {
			continue
		}
		dev, err := openReadOnly(info)
		if err != nil {
			if verbose {
				lg.Printf("  события: %04X:%04X не открыть: %v", info.UsagePage, info.Usage, err)
			}
			continue
		}
		devs = append(devs, dev)
		wg.Add(1)
		go func(d *Device) {
			defer wg.Done()
			for !stop.Load() {
				r, err := d.Read(2 * time.Second)
				if errors.Is(err, errTimeout) {
					continue
				}
				if err != nil {
					return
				}
				if verbose {
					lg.Printf("  IN %04X:%04X %s", d.Info.UsagePage, d.Info.Usage, hexs(trimZeros(r)))
				}
				if ev, ok := parseDockEvent(r); ok {
					select {
					case out <- ev:
					default:
					}
				}
			}
		}(dev)
	}
	return func() {
		stop.Store(true)
		for _, d := range devs {
			d.CancelIO()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		for _, d := range devs {
			d.Close()
		}
	}, len(devs)
}
