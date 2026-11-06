// Package razer speaks Razer protocol 2.5 to the Basilisk V3 Pro and the Mouse Dock Pro.
package razer

// Razer "protocol 2.5" feature reports, used by the Basilisk V3 Pro and the
// Mouse Dock Pro. Reconstructed from Synapse 4 (rzDevice25 / rzDevice25Linker /
// rzDevice25DualLinkMouse) and its logs. The report is 90 bytes, sent and read
// with HID feature report ID 0:
//
//   [0]     status: 0 new, 1 busy, 2 ok, 3 failed, 4 timeout, 5 not supported
//   [1]     transaction id. The dock routes by it: 0xE0|n is for the dock itself,
//           0x00..0x1F goes over the radio to the paired mouse (n = 0..31 counter)
//   [2..4]  remaining packets (2 bytes), protocol type - all zero
//   [5]     data size, [6] command class, [7] command id
//   [8..87] arguments
//   [88]    XOR of bytes 2..87, [89] reserved

import (
	"fmt"
	"strings"

	"github.com/eyescreamxd/bsctl/internal/hid"
)

const (
	VID            = 0x1532
	PIDMouseDock   = 0x00A4 // Razer Mouse Dock Pro (also the radio receiver for the mouse)
	PIDBasiliskW   = 0x00AA // Basilisk V3 Pro on a cable
	PIDBasiliskRx  = 0x00AB // Basilisk V3 Pro through its own HyperSpeed dongle
	razerReportLen = 90

	rzStatusNew          = 0
	rzStatusBusy         = 1
	rzStatusOK           = 2
	rzStatusFailed       = 3
	rzStatusTimeout      = 4
	rzStatusNotSupported = 5

	tidDockFlag = 0xE0
)

type rzCmd struct {
	Size, Class, ID byte
	Name            string
}

var (
	rzGetFirmware   = rzCmd{0x02, 0x00, 0x81, "firmware"}
	rzGetSerial     = rzCmd{0x16, 0x00, 0x82, "serial"}
	rzGetDeviceMode = rzCmd{0x02, 0x00, 0x84, "device mode"}
	rzSetDeviceMode = rzCmd{0x02, 0x00, 0x04, "set device mode"}
	rzGetBattery    = rzCmd{0x02, 0x07, 0x80, "battery"}
	rzGetCharging   = rzCmd{0x02, 0x07, 0x84, "charging"}
	rzSetEffect     = rzCmd{0x06, 0x0F, 0x02, "set effect"}
	rzSetBrightness = rzCmd{0x03, 0x0F, 0x04, "set brightness"}
	rzGetBrightness = rzCmd{0x03, 0x0F, 0x84, "get brightness"}
)

// Hardware lighting effects ("NEW_CHROMA_EFFECT_ID" in Synapse).
const (
	FxOff       = 0
	FxStatic    = 1
	FxBreathing = 2
	FxSpectrum  = 3
)

// rzPacket builds the 90-byte report. size 0 means c.Size.
func rzPacket(tid byte, c rzCmd, size byte, args []byte) []byte {
	p := make([]byte, razerReportLen)
	if size == 0 {
		size = c.Size
	}
	p[1] = tid
	p[5], p[6], p[7] = size, c.Class, c.ID
	copy(p[8:88], args)
	p[88] = rzChecksum(p)
	return p
}

func rzChecksum(p []byte) byte {
	var x byte
	for i := 2; i < 88 && i < len(p); i++ {
		x ^= p[i]
	}
	return x
}

type rzResponse struct {
	Status, TID, Size, Class, ID byte
	Args                         []byte
}

func parseRz(p []byte) (rzResponse, bool) {
	if len(p) < razerReportLen {
		return rzResponse{}, false
	}
	r := rzResponse{Status: p[0], TID: p[1], Size: p[5], Class: p[6], ID: p[7]}
	r.Args = append([]byte(nil), p[8:88]...)
	return r, true
}

func rzStatusText(s byte) string {
	switch s {
	case rzStatusNew:
		return "нет ответа"
	case rzStatusBusy:
		return "занято"
	case rzStatusOK:
		return "ok"
	case rzStatusFailed:
		return "ошибка"
	case rzStatusTimeout:
		return "таймаут (мышь выключена или спит?)"
	case rzStatusNotSupported:
		return "команда не поддерживается"
	}
	return fmt.Sprintf("статус %d", s)
}

// effectArgs: [profile(0 = don't store), region(0), effect, flags, rate, colors, R, G, B...].
// Breathing with one color uses flags = 1 (as OpenRazer's breathing_single).
func effectArgs(effect byte, colors ...[3]byte) []byte {
	a := []byte{0, 0, effect, 0, 0, byte(len(colors))}
	if effect == FxBreathing && len(colors) == 1 {
		a[3] = 1
	}
	for _, c := range colors {
		a = append(a, c[0], c[1], c[2])
	}
	return a
}

// BatteryPercent converts the 0..255 battery value to percent (Synapse: 225 -> 88%).
func BatteryPercent(raw byte) int { return (int(raw)*100 + 127) / 255 }

// Dock colors from Synapse's DeviceManifest_164 (Mouse Dock Pro): battery level ranges,
// static while on battery, breathing while charging (100% is always static green).
var (
	ColGreen  = [3]byte{0x00, 0xFF, 0x00}
	ColYellow = [3]byte{0xFF, 0xFF, 0x00}
	ColOrange = [3]byte{0xFF, 0x45, 0x00}
	ColRed    = [3]byte{0xFF, 0x00, 0x00}
)

func levelColor(level int) [3]byte {
	switch {
	case level >= 75:
		return ColGreen
	case level >= 50:
		return ColYellow
	case level >= 25:
		return ColOrange
	}
	return ColRed
}

// DockLook returns the effect for the dock. level < 0 means "no mouse".
func DockLook(level int, charging bool) (byte, [3]byte) {
	if level < 0 {
		return FxStatic, ColRed
	}
	if charging && level < 100 {
		return FxBreathing, levelColor(level)
	}
	return FxStatic, levelColor(level)
}

func FxName(effect byte, c [3]byte) string {
	names := map[byte]string{FxOff: "выкл", FxStatic: "цвет", FxBreathing: "дыхание", FxSpectrum: "радуга"}
	if effect == FxOff || effect == FxSpectrum {
		return names[effect]
	}
	return fmt.Sprintf("%s #%02X%02X%02X", names[effect], c[0], c[1], c[2])
}

func ParseColor(s string) ([3]byte, error) {
	var c [3]byte
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return c, fmt.Errorf("цвет пишется как RRGGBB, например 00FF00")
	}
	_, err := fmt.Sscanf(s, "%02x%02x%02x", &c[0], &c[1], &c[2])
	return c, err
}

// Dock notifications: HID input report 5, 16 bytes: 05 <event> <data...>.
//
//	05 31 <battery 0..255> <charging> <power source> <device type 1 = mouse>
//	05 09 <state: 2 disconnected, 3 connected> <device type>
type DockEvent struct {
	ID   byte
	Data []byte
}

const (
	DockEvBattery  = 0x31
	DockEvWireless = 0x09
)

func parseDockEvent(r []byte) (DockEvent, bool) {
	if len(r) < 3 || r[0] != 0x05 {
		return DockEvent{}, false
	}
	return DockEvent{ID: r[1], Data: append([]byte(nil), r[2:]...)}, true
}

func (e DockEvent) String() string {
	switch {
	case e.ID == DockEvBattery && len(e.Data) >= 4:
		return fmt.Sprintf("заряд мыши %d%%, зарядка=%d, питание=%d, устройство=%d",
			BatteryPercent(e.Data[0]), e.Data[1], e.Data[2], e.Data[3])
	case e.ID == DockEvWireless && len(e.Data) >= 2:
		st := map[byte]string{2: "отключилась", 3: "подключилась"}[e.Data[0]]
		if st == "" {
			st = fmt.Sprintf("состояние %d", e.Data[0])
		}
		return fmt.Sprintf("радио: устройство %d %s", e.Data[1], st)
	}
	return fmt.Sprintf("событие %02X %s", e.ID, hid.Hex(hid.TrimZeros(e.Data)))
}
