package main

// Protocol of the Razer BlackShark V2 Pro (2023) HyperSpeed dongle (USB 1532:0555),
// reconstructed from Razer Synapse 4 logs ("AudioMxicDevice.sendCommandOut").
//
// Host -> device (HID output report 2, 64 bytes):
//   02 80 <frameLen> 00 00 'P' 'A' <target> 00 <blockLen> <block...>
//   frameLen = 5 + blockLen (bytes from 'P' to the end of the block)
//   target   = 0x08 headset (through the radio link), 0x0E dongle itself
//   block    = <cmd> <lenHi> <lenLo> <data...>   (remote mode: <0xE1> <mode>)
//
// Device -> host (HID input report 2):
//   02 <frameLen> 'P' 'I' <target> xx xx xx xx xx <blockLen lo> <blockLen hi> <op0> <op1> <dataLen> <data...>
//   For one-byte commands op0 = command, op1 = 1. For the firmware read op0 op1 = 01 C2.

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	reportID      = 0x02
	targetHeadset = 0x08
	targetDongle  = 0x0E

	cmdSerial      = 0x00 // get serial number (ASCII)
	cmdEdition     = 0x01 // get edition id
	cmdEQStatusGet = 0x1E // get "speaker preset EQ" status
	cmdConnection  = 0x20 // get wireless connection status: 1 connected, 0 disconnected
	cmdBattery     = 0x21 // get battery level, %
	cmdCharging    = 0x2A // get charging status
	cmdAutoOffGet  = 0x2C // get auto power off
	cmdMicMute     = 0x55 // get mic mute state; the mic button sends the same code as a notification
	cmdPresetIndex = 0x93 // set EQ preset index: 255 custom, 250..254 esports presets
	cmdCustomEQ    = 0x95 // set 10-band EQ, signed dB per band
	cmdMicPresetG  = 0x16 // get mic EQ preset index
	cmdMicEQGet    = 0x17 // get mic custom EQ, 10 bands
	cmdMicPreset   = 0x96 // set mic EQ preset index: 0 default, 1 boost, 2 broadcast, 3 conference, 255 custom
	cmdMicCustomEQ = 0x97 // set mic custom EQ, 10 bands, signed dB: acked, but the sound does not change
	cmdSidetoneGet = 0x18 // get sidetone on/off
	cmdSidetoneVG  = 0x19 // get sidetone volume, 0..15
	cmdSidetone    = 0x98 // set sidetone on/off
	cmdSidetoneVol = 0x99 // set sidetone volume, 0..15 (Synapse: percent*15/100)
	cmdEQGroup     = 0x9D // set EQ preset group: 1 standard, 2 esports
	cmdEQStatusSet = 0x9E // set "speaker preset EQ" status
	cmdAutoOffSet  = 0xAC // set auto power off, minutes (0 = never)
	cmdRemoteMode  = 0xE1 // dongle: 1 remote (PC controls the headset), 0 local
)

// buildPacket returns a 64-byte output report.
func buildPacket(target byte, block []byte) []byte {
	p := make([]byte, 64)
	p[0] = reportID
	p[1] = 0x80
	p[2] = byte(5 + len(block))
	p[5], p[6] = 'P', 'A'
	p[7] = target
	p[9] = byte(len(block))
	copy(p[10:], block)
	return p
}

func getBlock(cmd byte) []byte         { return []byte{cmd, 0, 0} }
func setBlock(cmd, value byte) []byte  { return []byte{cmd, 0, 1, value} }
func remoteBlock(remote bool) []byte   { return []byte{cmdRemoteMode, boolByte(remote)} }
func firmwareBlock() []byte            { return []byte{0x01, 0xC2, 0x03, 0xF8, 0x5F, 0x04} }
func customEQBlock(bands []int) []byte { return bandsBlock(cmdCustomEQ, bands) }

func bandsBlock(cmd byte, bands []int) []byte {
	b := []byte{cmd, 0, 10}
	for _, v := range bands {
		b = append(b, byte(int8(v)))
	}
	return b
}

// sidetoneRaw converts percent to the headset's 0..15 scale exactly like Synapse.
func sidetoneRaw(percent int) byte { return byte(max(0, min(100, percent)) * 15 / 100) }

// sidetonePercent converts the headset's 0..15 value back to percent (Synapse: ceil).
func sidetonePercent(raw byte) int { return (int(raw)*100 + 14) / 15 }

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// Response is a parsed frame. Answers to our commands come from the headset
// (Src 0x08). Unsolicited events (buttons, volume wheel, playback state) use
// Src 0x01 / 0x11 with the same layout, e.g. "20 E9 00" = consumer key Volume Up.
type Response struct {
	Src      byte
	Op0, Op1 byte
	Data     []byte
	Block    []byte // raw block, used to decode events
}

// parseFrames splits an input report into frames. r[1] is the total length of
// all frames packed into this report (one report can carry several events plus
// an answer); bytes after that are stale buffer content.
func parseFrames(r []byte) []*Response {
	if len(r) < 4 || r[0] != reportID {
		return nil
	}
	end := 2 + int(r[1])
	if end > len(r) {
		end = len(r)
	}
	var out []*Response
	for off := 2; off+13 <= end; {
		f := r[off:end]
		if f[0] != 'P' || f[1] != 'I' {
			break
		}
		blk := int(f[8]) | int(f[9])<<8
		if blk < 3 || 10+blk > len(f) {
			break
		}
		b := f[10 : 10+blk]
		n := int(b[2])
		if 3+n > len(b) {
			n = len(b) - 3
		}
		out = append(out, &Response{Src: f[2], Op0: b[0], Op1: b[1],
			Data: append([]byte(nil), b[3:3+n]...), Block: append([]byte(nil), b...)})
		off += 10 + blk
	}
	return out
}

// parseResponse returns the first frame of a report.
func parseResponse(r []byte) (*Response, bool) {
	fr := parseFrames(r)
	if len(fr) == 0 {
		return nil, false
	}
	return fr[0], true
}

// matchOp matches answers to one-byte commands. 01 C2 is the firmware read, not "edition".
func matchOp(cmd byte) func(*Response) bool {
	return func(r *Response) bool {
		return r.Src == targetHeadset && r.Op0 == cmd && !(cmd == cmdEdition && r.Op1 == 0xC2)
	}
}

func matchFirmware(r *Response) bool { return r.Src == targetHeadset && r.Op0 == 0x01 && r.Op1 == 0xC2 }

// isNotify reports unsolicited state changes from the headset: they reuse the
// "get" command code with op1 = 2, e.g. "55 02 01 01" = mic muted by its button.
func isNotify(r *Response) bool { return r.Src == targetHeadset && r.Op1 == 0x02 && len(r.Data) >= 1 }

// describeEvent gives a human name to known unsolicited frames (for the monitor log).
func describeEvent(r *Response) string {
	switch {
	case isNotify(r) && r.Op0 == cmdMicMute:
		return fmt.Sprintf("кнопка микрофона: muted=%d", r.Data[0])
	case isNotify(r) && r.Op0 == cmdBattery:
		return fmt.Sprintf("уведомление: заряд %d%%", r.Data[0])
	case isNotify(r) && r.Op0 == cmdCharging:
		return fmt.Sprintf("уведомление: зарядка=%d", r.Data[0])
	case isNotify(r) && r.Op0 == cmdConnection:
		return fmt.Sprintf("уведомление: подключение=%d", r.Data[0])
	case r.Src != targetHeadset && r.Op0 == 0x20:
		names := map[byte]string{0xE9: "Volume+", 0xEA: "Volume-", 0xCD: "Play/Pause", 0x00: "release"}
		if n, ok := names[r.Op1]; ok {
			return "клавиша: " + n
		}
		return fmt.Sprintf("клавиша: usage %02X", r.Op1)
	case r.Src != targetHeadset && r.Op0 == 0x04 && len(r.Block) >= 3:
		return fmt.Sprintf("громкость гарнитуры: %d", r.Block[2])
	case r.Src != targetHeadset && r.Op0 == 0x11 && len(r.Block) >= 3:
		return fmt.Sprintf("уведомление %02X", r.Block[2])
	case r.Src == targetHeadset:
		return fmt.Sprintf("ответ op=%02X data=%v", r.Op0, r.Data)
	}
	return fmt.Sprintf("событие src=%02X op=%02X/%02X data=%v", r.Src, r.Op0, r.Op1, r.Data)
}

// Preset is an EQ preset. Curves are the Synapse defaults (31 Hz .. 16 kHz).
type Preset struct {
	Key   string
	Title string
	Group byte
	Index byte
	Bands []int // nil = take customEQ from the config
}

var presets = []Preset{
	{"custom", "Свой", 1, 255, nil},
	// AutoEQ (github.com/jaakkopasanen/AutoEq, MIT): Rtings measurement of the
	// BlackShark V2 Pro 2023, "FixedBandEQ" on the same 10 frequencies, rounded to 1 dB.
	{"autoeq", "AutoEQ (замер Rtings)", 1, 255, []int{7, 3, -2, 0, 4, -1, -2, 3, -5, -2}},
	{"game", "Игры", 1, 255, []int{-3, -3, -4, 0, 5, 5, 4, 1, 0, -1}},
	{"movie", "Кино", 1, 255, []int{4, 4, 3, 0, -3, -1, 3, 5, 2, 1}},
	{"music", "Музыка", 1, 255, []int{2, 2, 1, 1, 2, 3, 3, 3, 1, 0}},
	{"apex", "Apex Legends", 2, 250, []int{-1, 0, 0, 0, -1, 0, 2, 3, 2, 3}},
	{"csgo", "CS2", 2, 251, []int{-5, -4, 3, 5, 3, -2, 1, 5, 4, -4}},
	{"valorant", "Valorant", 2, 252, []int{0, 0, 0, 0, 0, 1, 4, 4, 4, -3}},
	{"fortnite", "Fortnite", 2, 253, []int{-4, 5, 5, 3, -2, 3, 4, 4, -1, 4}},
	{"cod", "Call of Duty", 2, 254, []int{-2, 0, 3, 3, 3, 3, 0, 0, 0, 0}},
}

var bandNames = []string{"31", "63", "125", "250", "500", "1k", "2k", "4k", "8k", "16k"}

// MicPreset is a mic EQ preset. Curves are from the Synapse UI; Equalizer APO
// applies them (the headset's own mic EQ does not change the sound).
type MicPreset struct {
	Key   string
	Title string
	Index byte
	Bands []int // nil = micCustomEQ from the config
}

const micCustomIndex = 255

var micPresets = []MicPreset{
	{"default", "Стандартный", 0, []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	{"micboost", "Усиление", 1, []int{0, 2, 3, 4, 5, 5, 5, 4, 3, 1}},
	{"broadcast", "Трансляция", 2, []int{4, 4, 4, 3, -2, -7, -4, -2, -3, -5}},
	{"conference", "Конференция", 3, []int{-8, -7, -5, -3, -1, 1, 3, 2, 1, 0}},
	{"custom", "Свой", micCustomIndex, nil},
}

func findMicPreset(index int) (MicPreset, bool) {
	for _, p := range micPresets {
		if int(p.Index) == index {
			return p, true
		}
	}
	return MicPreset{}, false
}

func findMicPresetKey(key string) (MicPreset, bool) {
	for _, p := range micPresets {
		if strings.EqualFold(p.Key, key) {
			return p, true
		}
	}
	return MicPreset{}, false
}

func findPreset(key string) (Preset, bool) {
	for _, p := range presets {
		if strings.EqualFold(p.Key, key) {
			return p, true
		}
	}
	return Preset{}, false
}

// parseBands parses "0,-2,3,..." (exactly 10 values, -12..12 dB).
func parseBands(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 10 {
		return nil, fmt.Errorf("нужно 10 значений через запятую (%s), а не %d", strings.Join(bandNames, ","), len(parts))
	}
	out := make([]int, 10)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("полоса %s: %v", bandNames[i], err)
		}
		out[i] = v
	}
	return out, validateBands(out)
}

func validateBands(b []int) error {
	if len(b) != 10 {
		return fmt.Errorf("EQ: нужно 10 полос, а не %d", len(b))
	}
	for i, v := range b {
		if v < -12 || v > 12 {
			return fmt.Errorf("EQ: полоса %s = %d дБ вне диапазона -12..12", bandNames[i], v)
		}
	}
	return nil
}

func hexs(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }

func trimZeros(b []byte) []byte {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	return b[:n]
}
