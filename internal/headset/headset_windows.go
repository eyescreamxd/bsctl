//go:build windows

package headset

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eyescreamxd/bsctl/internal/hid"
)

const (
	VendorRazer = 0x1532
	ProductBSV2 = 0x0555 // BlackShark V2 Pro (2023) HyperSpeed dongle
)

var (
	ErrNoResponse = errors.New("гарнитура не ответила")
	errClosed     = errors.New("донгл закрыт")
)

// Headset talks to the dongle. One goroutine reads input reports all the time:
// answers go to the transaction that waits for them, notifications (mic button,
// battery, connection) go to Events. So nothing is lost between polls.
type Headset struct {
	dev     *hid.Device
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

// New starts the reader on an open dongle collection.
func New(dev *hid.Device, lg *log.Logger, remote bool) *Headset {
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
		if errors.Is(err, hid.ErrTimeout) {
			continue
		}
		if err != nil {
			h.readErr = err
			return
		}
		if f := h.onReport.Load(); f != nil {
			(*f)(r)
		}
		for _, fr := range ParseFrames(r) {
			h.wMu.Lock()
			w := h.waiter
			h.wMu.Unlock()
			if w != nil && w.match(fr) {
				select {
				case w.ch <- fr:
				default:
				}
			}
			if IsNotify(fr) || fr.Src != targetHeadset {
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
func candidates(infos []hid.HIDInfo, cachedPath string) []hid.HIDInfo {
	var first, rest []hid.HIDInfo
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

// Open finds the collection that answers our protocol and records it in cfg.
func Open(cfg *Conn, lg *log.Logger) (*Headset, error) {
	infos, err := hid.Enumerate(VendorRazer, ProductBSV2)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, errors.New("донгл BlackShark (1532:0555) не найден — он воткнут?")
	}
	for _, info := range candidates(infos, cfg.DevicePath) {
		for _, setOut := range []bool{cfg.UseSetOutput, !cfg.UseSetOutput} {
			dev, err := hid.OpenDevice(info)
			if err != nil {
				lg.Printf("open %s: %v", info.Path, err)
				break
			}
			dev.UseSetOutput = setOut
			hs := New(dev, lg, cfg.Remote)
			if err := hs.Ping(700*time.Millisecond, 1); err == nil {
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
			h.log.Printf("OUT %s", hid.Hex(pkt[:10+len(block)]))
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
				h.log.Printf("IN  op=%02X/%02X data=%s", r.Op0, r.Op1, hid.Hex(r.Data))
			}
			return r, nil
		case <-timer.C:
		case <-h.done:
			timer.Stop()
		}
		h.setWaiter(nil)
		time.Sleep(time.Duration(40*a) * time.Millisecond)
	}
	return nil, ErrNoResponse
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

// Ping asks the connection status and only checks that the dongle answers.
func (h *Headset) Ping(wait time.Duration, attempts int) error {
	_, err := h.tx(targetHeadset, getBlock(CmdConnection), matchOp(CmdConnection), wait, attempts)
	return err
}

// AutoPowerOff reads the raw auto power off answer (diagnostics).
func (h *Headset) AutoPowerOff() ([]byte, error) { return h.get(cmdAutoOffGet) }

// ClearEQStatus sets the "speaker preset EQ" flag to 0, as Apply does first.
func (h *Headset) ClearEQStatus() error { return h.set(cmdEQStatusSet, 0) }

// Path is the HID path of the open dongle collection.
func (h *Headset) Path() string { return h.dev.Info.Path }

// UsesSetOutput reports whether writes go through HidD_SetOutputReport.
func (h *Headset) UsesSetOutput() bool { return h.dev.UseSetOutput }

// ReadErr is why the reader stopped (nil while it runs or after Close).
func (h *Headset) ReadErr() error { return h.readErr }

// SetRemote switches the dongle mode. The dongle never answers this packet.
func (h *Headset) SetRemote(on bool) error {
	h.txMu.Lock()
	defer h.txMu.Unlock()
	return h.writeRemote(on)
}

func (h *Headset) writeRemote(on bool) error {
	pkt := buildPacket(targetDongle, remoteBlock(on))
	if h.Verbose {
		h.log.Printf("OUT %s (remote=%v)", hid.Hex(pkt[:12]), on)
	}
	err := h.dev.Write(pkt)
	time.Sleep(20 * time.Millisecond)
	return err
}

func (h *Headset) Connected() (bool, error) {
	d, err := h.get(CmdConnection)
	if err != nil {
		return false, err
	}
	return len(d) > 0 && d[0] == 1, nil
}

func (h *Headset) Battery() (int, error) {
	d, err := h.get(CmdBattery)
	if err != nil || len(d) == 0 {
		return -1, err
	}
	return int(d[0]), nil
}

func (h *Headset) Charging() (bool, error) {
	d, err := h.get(CmdCharging)
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
	r, err := h.tx(targetHeadset, getBlock(CmdMicMute), matchOp(CmdMicMute), 600*time.Millisecond, 1)
	if err != nil {
		return false, err
	}
	if len(r.Data) == 0 {
		return false, ErrNoResponse
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
		return -1, ErrNoResponse
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
	if err := ValidateBands(bands); err != nil {
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

// Apply sends the whole config in the same order Synapse does.
func (h *Headset) Apply(c Settings) error {
	var errs []string
	step := func(name string, f func() error) {
		if err := f(); err != nil {
			errs = append(errs, name+": "+err.Error())
		} else if h.Verbose {
			h.log.Printf("  ok: %s", name)
		}
	}
	group, index, bands := c.Group, c.Index, c.Bands
	step("EQ off-board preset", func() error { return h.set(cmdEQStatusSet, 0) })
	// The headset acks its mic EQ (presets and 0x97 bands) but the sound does not
	// change; keep it neutral, the mic EQ is done by Equalizer APO.
	step("микрофон", func() error { return h.set(cmdMicPreset, 0) })
	step("самопрослушивание", func() error { return h.SetSidetone(c.Sidetone, c.SidetoneLevel) })
	step("автовыключение", func() error { return h.set(cmdAutoOffSet, byte(c.AutoPowerOffMin)) })
	step("эквалайзер", func() error { return h.SetEQ(group, index, bands) })
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	h.log.Printf("applied: preset=%s eq=%v sidetone=%v/%d%% autooff=%d", c.Preset, bands, c.Sidetone, c.SidetoneLevel, c.AutoPowerOffMin)
	return nil
}
