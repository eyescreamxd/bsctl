package icon

import "testing"

func TestIcon(t *testing.T) {
	for _, ico := range [][]byte{Headset(Look{Level: 57, Online: true, Muted: true}), Mouse(Look{Level: -1})} {
		if len(ico) != 6+16+40+32*32*4+32*32/8 {
			t.Fatalf("ico size %d", len(ico))
		}
	}
}
