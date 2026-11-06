package config

import (
	"testing"

	"github.com/eyescreamxd/bsctl/internal/headset"
)

func TestConfigMicEQ(t *testing.T) {
	c := Default()
	c.MicPreset = 0
	if eq := c.MicEQ(); eq != nil {
		t.Errorf("default preset is flat, got %v", eq)
	}
	c.MicPreset = 3
	if eq := c.MicEQ(); len(eq) != 10 || eq[0] != -8 {
		t.Errorf("conference curve: %v", eq)
	}
	c.MicPreset, c.MicCustomEQ = headset.MicCustomIndex, []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if eq := c.MicEQ(); eq != nil {
		t.Errorf("flat custom must give nil, got %v", eq)
	}
	c.MicCustomEQ = []int{-12, 0, 0, 0, 0, 0, 0, 0, 0, 3}
	if eq := c.MicEQ(); len(eq) != 10 || eq[0] != -12 || eq[9] != 3 {
		t.Errorf("custom: %v", eq)
	}
}
