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

const (
	vendorRazer = 0x1532
	productBSV2 = 0x0555 // BlackShark V2 Pro (2023) HyperSpeed dongle
)

var (
	errNoResponse = errors.New("гарнитура не ответила")
	errClosed     = errors.New("донгл закрыт")
)

// Headset talks to the dongle. One goroutine reads input reports all the time:
// answers go to the transaction that waits for them, notifications (mic button,
// battery, connection) go to Events. So nothing is lost between polls.
type Headset struct {
	dev     *Device
	log     *log.Logger
	Verbose bool
	// Remote: put the dongle into PC-controlled mode before every headset command,
	// exactly like Synapse does. In "local" mode the dongle ignores commands.
	Remote bool

	Events   chan *Response               // notifications and events; dropped when full
	onReport atomic.Pointer[func([]byte)] // raw reports (monitor)

	txMu    sync.Mutex // one transaction at a time
	wMu     sync.Mutex
	waiter  *hsWaiter
	closing atomic.Bool
	done    chan struct{}
	readErr error
}

type hsWaiter struct {
	match func(*Response) bool
	ch    chan *Response
}

func newHeadset(dev *Device, lg *log.Logger, remote bool) *Headset {
	h := &Headset{dev: dev, log: lg, Remote: remote, Events: make(chan *Response, 64), done: make(chan struct{})}
	go h.readLoop()
	return h
}

func (h *Headset) readLoop() {
	defer close(h.done)
	for {
		r, err := h.dev.Read(2 * time.Second)
		if h.closing.Load() {
			return
		}
		if errors.Is(err, errTimeout) {
			continue
		}
		if err != nil {
			h.readErr = err
			return
		}
		if f := h.onReport.Load(); f != nil {
			(*f)(r)
		}
		for _, fr := range parseFrames(r) {
			h.wMu.Lock()
			w := h.waiter
			h.wMu.Unlock()
			if w != nil && w.match(fr) {
				select {
				case w.ch <- fr:
				default:
				}
			}
			if isNotify(fr) || fr.Src != targetHeadset {
				select {
				case h.Events <- fr:
				default:
				}
			}
		}
	}
}

// SetReportHook sets a function that sees every raw input report.
func (h *Headset) SetReportHook(f func([]byte)) { h.onReport.Store(&f) }

// Alive is false once the reader stopped (dongle unplugged or closed).
func (h *Headset) Alive() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

func (h *Headset) Close() {
	h.closing.Store(true)
	h.dev.CancelIO()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
	}
	h.dev.Close()
}

// candidates returns collections that can carry 64-byte vendor reports, best first.
func candidates(infos []HIDInfo, cachedPath string) []HIDInfo {
	var first, rest []HIDInfo
	for _, i := range infos {
		if i.Err != "" || i.OutLen < 64 || i.InLen < 16 {
			continue
		}
		if strings.EqualFold(i.Path, cachedPath) || i.UsagePage >= 0xFF00 {
			first = append(first, i)
		} else {
			rest = append(rest, i)
		}
	}
	return append(first, rest...)
}

// openHeadset finds the collection that answers our protocol and records it in cfg.
func openHeadset(cfg *Config, lg *log.Logger) (*Headset, error) {
	infos, err := enumerateHID(vendorRazer, productBSV2)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, errors.New("донгл BlackShark (1532:0555) не найден — он воткнут?")
	}
	for _, info := range candidates(infos, cfg.DevicePath) {
		for _, setOut := range []bool{cfg.UseSetOutput, !cfg.UseSetOutput} {
			dev, err := openDevice(info)
			if err != nil {
				lg.Printf("open %s: %v", info.Path, err)
				break
			}
			dev.UseSetOutput = setOut
			hs := newHeadset(dev, lg, cfg.RemoteMode)
			if _, err := hs.tx(targetHeadset, getBlock(cmdConnection), matchOp(cmdConnection), 700*time.Millisecond, 1); err == nil {
				cfg.DevicePath, cfg.UseSetOutput = info.Path, setOut // the caller saves it
				return hs, nil
			}
			hs.Close()
		}
	}
	return nil, errors.New("донгл не отвечает (гарнитура выключена или донгл занят)")
}

func (h *Headset) setWaiter(w *hsWaiter) {
	h.wMu.Lock()
	h.waiter = w
	h.wMu.Unlock()
}

// tx writes a packet and waits for the matching answer. wait=0 means fire-and-forget.
func (h *Headset) tx(target byte, block []byte, match func(*Response) bool, wait time.Duration, attempts int) (*Response, error) {
	h.txMu.Lock()
	defer h.txMu.Unlock()
	pkt := buildPacket(target, block)
	for a := 1; a <= attempts; a++ {
		if !h.Alive() {
			if h.readErr != nil {
				return nil, h.readErr
			}
			return nil, errClosed
		}
		if target == targetHeadset && h.Remote {
			_ = h.writeRemote(true)
		}
		var w *hsWaiter
		if wait > 0 {
			w = &hsWaiter{match: match, ch: make(chan *Response, 1)}
			h.setWaiter(w) // before writing: the answer can be fast
		}
		if h.Verbose {
			h.log.Printf("OUT %s", hexs(pkt[:10+len(block)]))
		}
		if err := h.dev.Write(pkt); err != nil {
			h.setWaiter(nil)
			return nil, err
		}
		if wait == 0 {
			return nil, nil
		}
		timer := time.NewTimer(wait)
		select {
		case r := <-w.ch:
			timer.Stop()
			h.setWaiter(nil)
			if h.Verbose {
				h.log.Printf("IN  op=%02X/%02X data=%s", r.Op0, r.Op1, hexs(r.Data))
			}
			return r, nil
		case <-timer.C:
		case <-h.done:
			timer.Stop()
		}
		h.setWaiter(nil)
		time.Sleep(time.Duration(40*a) * time.Millisecond)
	}
	return nil, errNoResponse
}

func (h *Headset) get(cmd byte) ([]byte, error) {
	r, err := h.tx(targetHeadset, getBlock(cmd), matchOp(cmd), 1200*time.Millisecond, 3)
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

func (h *Headset) set(cmd, value byte) error {
	_, err := h.tx(targetHeadset, setBlock(cmd, value), matchOp(cmd), 1200*time.Millisecond, 3)
	return err
}

// SetRemote switches the dongle mode. The dongle never answers this packet.
func (h *Headset) SetRemote(on bool) error {
	h.txMu.Lock()
	defer h.txMu.Unlock()
	return h.writeRemote(on)
}

func (h *Headset) writeRemote(on bool) error {
	pkt := buildPacket(targetDongle, remoteBlock(on))
	if h.Verbose {
		h.log.Printf("OUT %s (remote=%v)", hexs(pkt[:12]), on)
	}
	err := h.dev.Write(pkt)
	time.Sleep(20 * time.Millisecond)
	return err
}

func (h *Headset) Connected() (bool, error) {
	d, err := h.get(cmdConnection)
	if err != nil {
		return false, err
	}
	return len(d) > 0 && d[0] == 1, nil
}

func (h *Headset) Battery() (int, error) {
	d, err := h.get(cmdBattery)
	if err != nil || len(d) == 0 {
		return -1, err
	}
	return int(d[0]), nil
}

func (h *Headset) Charging() (bool, error) {
	d, err := h.get(cmdCharging)
	if err != nil {
		return false, err
	}
	return len(d) > 0 && d[0] != 0, nil
}

// SetSidetone switches sidetone and, when on, sets its volume. Synapse does both:
// the switch alone is silent if the volume in the headset is 0.
func (h *Headset) SetSidetone(on bool, percent int) error {
	if err := h.set(cmdSidetone, boolByte(on)); err != nil {
		return err
	}
	if on {
		if err := h.set(cmdSidetoneVol, sidetoneRaw(percent)); err != nil {
			return fmt.Errorf("громкость: %w", err)
		}
	}
	return nil
}

// Sidetone reads the sidetone switch and volume (percent).
func (h *Headset) Sidetone() (bool, int, error) {
	st, err := h.get(cmdSidetoneGet)
	if err != nil || len(st) == 0 {
		return false, -1, err
	}
	vol, err := h.get(cmdSidetoneVG)
	if err != nil || len(vol) == 0 {
		return st[0] != 0, -1, err
	}
	return st[0] != 0, sidetonePercent(vol[0]), nil
}

// MicMuted asks the mic state (same code as the mic button notification).
// Short timeout and one attempt: older firmware may not answer.
func (h *Headset) MicMuted() (bool, error) {
	r, err := h.tx(targetHeadset, getBlock(cmdMicMute), matchOp(cmdMicMute), 600*time.Millisecond, 1)
	if err != nil {
		return false, err
	}
	if len(r.Data) == 0 {
		return false, errNoResponse
	}
	return r.Data[0] != 0, nil
}

// EQStatus reads the "speaker preset EQ" flag. Apply sets it to 0; if it changes
// on its own, the dongle has dropped our settings and they must be sent again.
func (h *Headset) EQStatus() (int, error) {
	r, err := h.tx(targetHeadset, getBlock(cmdEQStatusGet), matchOp(cmdEQStatusGet), 800*time.Millisecond, 1)
	if err != nil {
		return -1, err
	}
	if len(r.Data) == 0 {
		return -1, errNoResponse
	}
	return int(r.Data[0]), nil
}

func (h *Headset) Serial() (string, error) {
	d, err := h.get(cmdSerial)
	return strings.TrimRight(string(d), "\x00"), err
}

func (h *Headset) DongleFirmware() (string, error) {
	r, err := h.tx(targetHeadset, firmwareBlock(), matchFirmware, 600*time.Millisecond, 1)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(r.Data))
	for i, b := range r.Data {
		parts[i] = fmt.Sprint(b)
	}
	return strings.Join(parts, "."), nil
}

func (h *Headset) SetEQ(group, index byte, bands []int) error {
	if err := validateBands(bands); err != nil {
		return err
	}
	if err := h.set(cmdEQGroup, group); err != nil {
		return fmt.Errorf("EQ group: %w", err)
	}
	if err := h.set(cmdPresetIndex, index); err != nil {
		return fmt.Errorf("EQ preset index: %w", err)
	}
	if _, err := h.tx(targetHeadset, customEQBlock(bands), matchOp(cmdCustomEQ), 1200*time.Millisecond, 3); err != nil {
		return fmt.Errorf("EQ bands: %w", err)
	}
	return nil
}

// errMicEQUnsupported: the headset did not answer the mic custom EQ command.
var errMicEQUnsupported = errors.New("гарнитура не принимает свой эквалайзер микрофона")

// SetMicEQ sets the mic EQ preset; for 255 (custom) also the 10 bands, like Synapse.
func (h *Headset) SetMicEQ(index int, bands []int) error {
	if err := h.set(cmdMicPreset, byte(index)); err != nil {
		return err
	}
	if index != micCustomIndex {
		return nil
	}
	if err := validateBands(bands); err != nil {
		return err
	}
	// One short try: older firmware may not know this command.
	if _, err := h.tx(targetHeadset, micEQBlock(bands), matchOp(cmdMicCustomEQ), 900*time.Millisecond, 1); err != nil {
		if errors.Is(err, errNoResponse) {
			return errMicEQUnsupported
		}
		return err
	}
	return nil
}

// MicEQ reads the mic preset index and the custom bands.
func (h *Headset) MicEQ() (int, []int, error) {
	d, err := h.get(cmdMicPresetG)
	if err != nil || len(d) == 0 {
		return -1, nil, err
	}
	r, err := h.tx(targetHeadset, getBlock(cmdMicEQGet), matchOp(cmdMicEQGet), 900*time.Millisecond, 1)
	if err != nil {
		return int(d[0]), nil, err
	}
	bands := make([]int, 0, 10)
	for _, b := range r.Data {
		bands = append(bands, int(int8(b)))
	}
	return int(d[0]), bands, nil
}

// Apply sends the whole config in the same order Synapse does.
func (h *Headset) Apply(c *Config) error {
	var errs []string
	step := func(name string, f func() error) {
		if err := f(); err != nil {
			errs = append(errs, name+": "+err.Error())
		} else if h.Verbose {
			h.log.Printf("  ok: %s", name)
		}
	}
	group, index, bands := c.effectiveEQ()
	step("EQ off-board preset", func() error { return h.set(cmdEQStatusSet, 0) })
	step("микрофон", func() error { return h.SetMicEQ(c.MicPreset, c.MicCustomEQ) })
	step("самопрослушивание", func() error { return h.SetSidetone(c.Sidetone, c.SidetoneLevel) })
	step("автовыключение", func() error { return h.set(cmdAutoOffSet, byte(c.AutoPowerOffMin)) })
	step("эквалайзер", func() error { return h.SetEQ(group, index, bands) })
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	mic := fmt.Sprint(c.MicPreset)
	if c.MicPreset == micCustomIndex {
		mic = fmt.Sprintf("custom%v", c.MicCustomEQ)
	}
	h.log.Printf("applied: preset=%s eq=%v mic=%s sidetone=%v/%d%% autooff=%d", c.Preset, bands, mic, c.Sidetone, c.SidetoneLevel, c.AutoPowerOffMin)
	return nil
}
