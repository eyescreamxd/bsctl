package main

import (
	"bytes"
	"encoding/binary"
	"math"
)

// Tray icons are drawn in code: 32x32, 4x4 supersampling, straight alpha,
// encoded as a classic 32-bit BMP .ico (CreateIconFromResourceEx / LoadImage).

const iconSize = 32

type rgba struct{ R, G, B, A float64 } // 0..1, straight alpha

type canvas struct{ px [iconSize * iconSize]rgba }

type shape func(x, y float64) bool

func (c *canvas) paint(s shape, col rgba) {
	const ss = 4
	for py := 0; py < iconSize; py++ {
		for px := 0; px < iconSize; px++ {
			n := 0
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					if s(float64(px)+(float64(i)+0.5)/ss, float64(py)+(float64(j)+0.5)/ss) {
						n++
					}
				}
			}
			if n == 0 {
				continue
			}
			a := col.A * float64(n) / (ss * ss)
			d := &c.px[py*iconSize+px]
			outA := a + d.A*(1-a)
			if outA <= 0 {
				continue
			}
			mix := func(s, t float64) float64 { return (s*a + t*d.A*(1-a)) / outA }
			*d = rgba{mix(col.R, d.R), mix(col.G, d.G), mix(col.B, d.B), outA}
		}
	}
}

func rgb(r, g, b uint8) rgba        { return rgba{float64(r) / 255, float64(g) / 255, float64(b) / 255, 1} }
func (c rgba) alpha(a float64) rgba { c.A = a; return c }

var (
	icGreen  = rgb(70, 205, 95)
	icYellow = rgb(245, 205, 40)
	icOrange = rgb(250, 135, 40)
	icRed    = rgb(235, 55, 50)
	icGrey   = rgb(150, 150, 150)
	icLight  = rgb(238, 238, 238)
	icShadow = rgb(20, 20, 20).alpha(0.8)
	icWhite  = rgb(255, 255, 255)
	icMute   = rgb(230, 35, 35)
)

// Same ranges as the dock (Synapse): 75+ green, 50+ yellow, 25+ orange, below red.
func levelTone(level int) rgba {
	switch {
	case level >= 75:
		return icGreen
	case level >= 50:
		return icYellow
	case level >= 25:
		return icOrange
	}
	return icRed
}

func circle(cx, cy, r float64) shape {
	return func(x, y float64) bool { return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r }
}

func ring(cx, cy, r0, r1 float64) shape {
	return func(x, y float64) bool {
		d := (x-cx)*(x-cx) + (y-cy)*(y-cy)
		return d >= r0*r0 && d <= r1*r1
	}
}

func rrect(x0, y0, x1, y1, r float64) shape {
	return func(x, y float64) bool {
		if x < x0 || x > x1 || y < y0 || y > y1 {
			return false
		}
		cx := math.Max(x0+r, math.Min(x, x1-r))
		cy := math.Max(y0+r, math.Min(y, y1-r))
		return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
	}
}

func seg(x0, y0, x1, y1, w float64) shape {
	return func(x, y float64) bool {
		dx, dy := x1-x0, y1-y0
		t := ((x-x0)*dx + (y-y0)*dy) / (dx*dx + dy*dy)
		t = math.Max(0, math.Min(1, t))
		ex, ey := x0+t*dx-x, y0+t*dy-y
		return ex*ex+ey*ey <= w*w/4
	}
}

func poly(pts [][2]float64) shape {
	return func(x, y float64) bool {
		in := false
		for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
			xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
			if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				in = !in
			}
		}
		return in
	}
}

func and(a, b shape) shape   { return func(x, y float64) bool { return a(x, y) && b(x, y) } }
func or(a, b shape) shape    { return func(x, y float64) bool { return a(x, y) || b(x, y) } }
func below(y0 float64) shape { return func(x, y float64) bool { return y >= y0 } }
func above(y0 float64) shape { return func(x, y float64) bool { return y <= y0 } }

func bolt(cx, cy, s float64) shape {
	pts := [][2]float64{{1.4, -7.5}, {-4.8, 1.4}, {-0.7, 1.4}, {-2.4, 8}, {4.8, -1.8}, {0.8, -1.8}, {3.4, -7.5}}
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{cx + p[0]*s, cy + p[1]*s}
	}
	return poly(out)
}

// devLook is what an icon shows.
type devLook struct {
	Level    int  // battery %, -1 unknown
	Online   bool // device answers
	Charging bool
	Muted    bool // headset: mic muted
}

func (l devLook) tone() rgba {
	if !l.Online || l.Level < 0 {
		return icGrey
	}
	return levelTone(l.Level)
}

// headsetIcon: headphones in the battery color, a red "no" badge when the mic is muted.
func headsetIcon(l devLook) []byte {
	var c canvas
	band := and(ring(16, 16.5, 9, 12.2), above(16.5))
	cupL := rrect(3, 15, 10.5, 28.5, 2.6)
	cupR := rrect(21.5, 15, 29, 28.5, 2.6)
	c.paint(or(and(ring(16, 16.5, 7.8, 13.4), above(17)), or(rrect(1.8, 13.8, 11.7, 29.7, 3.4), rrect(20.3, 13.8, 30.2, 29.7, 3.4))), icShadow)
	tone := l.tone()
	c.paint(or(band, or(cupL, cupR)), tone)
	if l.Online && l.Charging {
		c.paint(bolt(16, 21.5, 1.18), icShadow)
		c.paint(bolt(16, 21.5, 0.9), icWhite)
	}
	if l.Online && l.Muted {
		c.paint(circle(24, 24, 8.2), icShadow)
		c.paint(circle(24, 24, 7), icMute)
		c.paint(seg(20.2, 20.2, 27.8, 27.8, 2.4), icWhite)
	}
	return encodeICO(&c)
}

// mouseIcon: a mouse whose body fills up with the battery level.
func mouseIcon(l devLook) []byte {
	var c canvas
	outline := icLight
	if !l.Online {
		outline = icGrey
	}
	c.paint(rrect(5.8, 0.8, 26.2, 31.2, 10), icShadow)
	c.paint(rrect(7, 2, 25, 30, 9), outline)
	inner := rrect(9.2, 4.2, 22.8, 27.8, 6.8)
	c.paint(inner, rgb(30, 30, 30).alpha(0.9))
	if l.Level >= 0 {
		top := 27.8 - 23.6*float64(l.Level)/100
		tone := levelTone(l.Level)
		if !l.Online {
			tone = tone.alpha(0.45)
		}
		c.paint(and(inner, below(top)), tone)
	}
	c.paint(seg(16, 4.2, 16, 12, 1.6), outline)
	c.paint(rrect(14.4, 6, 17.6, 11.2, 1.5), outline)
	if l.Online && l.Charging {
		c.paint(bolt(16, 19.5, 0.95), icShadow)
		c.paint(bolt(16, 19.5, 0.72), icWhite)
	}
	return encodeICO(&c)
}

func encodeICO(c *canvas) []byte {
	const S = iconSize
	var bmp bytes.Buffer
	// BITMAPINFOHEADER: height is doubled (XOR + AND masks)
	_ = binary.Write(&bmp, binary.LittleEndian, struct {
		Size                          uint32
		Width, Height                 int32
		Planes, BitCount              uint16
		Compression, SizeImage        uint32
		XPels, YPels, ClrUsed, ClrImp int32
	}{40, S, 2 * S, 1, 32, 0, S * S * 4, 0, 0, 0, 0})
	b8 := func(v float64) byte { return byte(math.Round(math.Max(0, math.Min(1, v)) * 255)) }
	for y := S - 1; y >= 0; y-- { // bottom-up BGRA, straight alpha
		for x := 0; x < S; x++ {
			p := c.px[y*S+x]
			bmp.Write([]byte{b8(p.B), b8(p.G), b8(p.R), b8(p.A)})
		}
	}
	bmp.Write(make([]byte, S*S/8)) // AND mask: all zero, alpha channel decides

	var ico bytes.Buffer
	_ = binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1}) // reserved, type=icon, count
	ico.Write([]byte{S, S, 0, 0})                                  // width, height, colors, reserved
	_ = binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})   // planes, bpp
	_ = binary.Write(&ico, binary.LittleEndian, []uint32{uint32(bmp.Len()), 22})
	ico.Write(bmp.Bytes())
	return ico.Bytes()
}
