// Package hid opens HID collections on Windows (enumeration, input/output and
// feature reports). The byte helpers here are platform-free.
package hid

import (
	"encoding/hex"
	"strings"
)

// Hex renders bytes as upper-case hex for the log.
func Hex(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }

// TrimZeros drops trailing zero bytes (report padding).
func TrimZeros(b []byte) []byte {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	return b[:n]
}
