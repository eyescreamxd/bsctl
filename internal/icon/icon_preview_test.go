package icon

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// go test -run TestIconPreview with PREVIEW_DIR set renders all icon states to a PNG sheet.
func TestIconPreview(t *testing.T) {
	dir := os.Getenv("PREVIEW_DIR")
	if dir == "" {
		t.Skip("PREVIEW_DIR not set")
	}
	looks := []Look{
		{Level: 90, Online: true}, {Level: 60, Online: true}, {Level: 30, Online: true}, {Level: 10, Online: true},
		{Level: 60, Online: true, Charging: true}, {Level: 90, Online: true, Muted: true}, {Level: 20, Online: true, Muted: true, Charging: true},
		{Level: 70, Online: false}, {Level: -1, Online: false},
	}
	decode := func(ico []byte) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		bmp := ico[22+40:]
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				o := ((31-y)*32 + x) * 4
				img.SetNRGBA(x, y, color.NRGBA{bmp[o+2], bmp[o+1], bmp[o], bmp[o+3]})
			}
		}
		return img
	}
	// downscale by box filter to n px, then magnify by m
	scaled := func(src *image.NRGBA, n int) [][]color.NRGBA {
		out := make([][]color.NRGBA, n)
		f := 32.0 / float64(n)
		for y := 0; y < n; y++ {
			out[y] = make([]color.NRGBA, n)
			for x := 0; x < n; x++ {
				var r, g, b, a, cnt float64
				for sy := int(float64(y) * f); sy < int(float64(y+1)*f); sy++ {
					for sx := int(float64(x) * f); sx < int(float64(x+1)*f); sx++ {
						p := src.NRGBAAt(sx, sy)
						al := float64(p.A) / 255
						r += float64(p.R) * al
						g += float64(p.G) * al
						b += float64(p.B) * al
						a += al
						cnt++
					}
				}
				if a > 0 {
					out[y][x] = color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / cnt * 255)}
				}
			}
		}
		return out
	}
	sizes := []int{16, 24, 32}
	cell := 32*4 + 8
	sheet := image.NewNRGBA(image.Rect(0, 0, len(looks)*cell*2, len(sizes)*2*cell))
	bgs := []color.NRGBA{{32, 32, 32, 255}, {235, 235, 235, 255}}
	for gi, gen := range []func(Look) []byte{Headset, Mouse} {
		for li, l := range looks {
			src := decode(gen(l))
			for si, n := range sizes {
				px := scaled(src, n)
				m := (32 * 4) / n
				for bi, bg := range bgs {
					ox := (gi*len(looks) + li) * cell
					oy := (si*2 + bi) * cell
					for y := 0; y < cell; y++ {
						for x := 0; x < cell; x++ {
							sheet.SetNRGBA(ox+x, oy+y, bg)
						}
					}
					for y := 0; y < n*m; y++ {
						for x := 0; x < n*m; x++ {
							p := px[y/m][x/m]
							a := float64(p.A) / 255
							c := color.NRGBA{uint8(float64(p.R)*a + float64(bg.R)*(1-a)), uint8(float64(p.G)*a + float64(bg.G)*(1-a)), uint8(float64(p.B)*a + float64(bg.B)*(1-a)), 255}
							sheet.SetNRGBA(ox+4+x, oy+4+y, c)
						}
					}
				}
			}
		}
	}
	f, err := os.Create(filepath.Join(dir, "icons.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, sheet)
}
