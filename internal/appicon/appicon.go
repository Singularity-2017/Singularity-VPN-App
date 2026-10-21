// Package appicon draws the Singularity VPN mark -- a ring around a dot --
// for the tray and for the icon the executables carry. Drawing it keeps a
// binary asset out of the repository and every size and state from
// drifting apart.
package appicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Look is what the mark says about the connection.
type Look int

const (
	Look_idle    Look = iota // grey: nothing is up
	Look_up                  // the accent: a tunnel carries traffic
	Look_trouble             // red: the connection is failing or got worse
)

var ring_colors = map[Look]color.NRGBA{
	Look_idle:    {R: 0x8a, G: 0x96, B: 0xa0, A: 0xff},
	Look_up:      {R: 0x4b, G: 0xe3, B: 0xdf, A: 0xff},
	Look_trouble: {R: 0xff, G: 0x5f, B: 0x5f, A: 0xff},
}

// ground is the disc the application icon sits on, the window's own
// background, so the mark reads on a light taskbar as on a dark one.
var ground = color.NRGBA{R: 0x1d, G: 0x22, B: 0x27, A: 0xff}

// Draw renders the mark at size pixels. With a ground the ring sits on a
// dark disc, as the application icon; without one it is drawn alone, as the
// tray takes it.
func Draw(size int, look Look, with_ground bool) *image.NRGBA {
	ring := ring_colors[look]
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := float64(size) / 32
	center := float64(size-1) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-center, float64(y)-center) / scale
			var c color.NRGBA
			if with_ground {
				// The disc spans the square; the ring is pulled in a
				// little so it does not touch the disc's edge.
				if a := coverage(d*scale, -1, float64(size)/2, 1); a > 0 {
					c = ground
					c.A = uint8(a * 255)
				}
				mark := math.Max(coverage(d, 8, 12.5, 1/scale), coverage(d, -1, 3.6, 1/scale))
				c = over(c, ring, mark)
			} else {
				// A ring from radius 9.5 to 14, and a dot under 4.
				mark := math.Max(coverage(d, 9.5, 14, 1/scale), coverage(d, -1, 4, 1/scale))
				c = over(c, ring, mark)
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// coverage is how much of the pixel at distance d lies between inner and
// outer, with a one-pixel ramp at each edge; pixel is one pixel in the
// units d is measured in.
func coverage(d, inner, outer, pixel float64) float64 {
	a := (math.Min(d-inner, outer-d))/pixel + 0.5
	return math.Max(0, math.Min(1, a))
}

// over lays top at the given opacity over bottom.
func over(bottom, top color.NRGBA, alpha float64) color.NRGBA {
	if alpha <= 0 {
		return bottom
	}
	ba := float64(bottom.A) / 255
	out_a := alpha + ba*(1-alpha)
	mix := func(t, b uint8) uint8 {
		return uint8((float64(t)*alpha + float64(b)*ba*(1-alpha)) / out_a)
	}
	return color.NRGBA{R: mix(top.R, bottom.R), G: mix(top.G, bottom.G), B: mix(top.B, bottom.B), A: uint8(out_a * 255)}
}

// Tray_ico is the 32-pixel tray mark as a .ico, which is what the Windows
// tray takes.
func Tray_ico(look Look) []byte {
	const size = 32
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, Draw(size, look, false)); err != nil {
		return nil
	}
	// ICONDIR, one ICONDIRENTRY, then the PNG: Windows Vista and later read
	// PNG-compressed icon images directly.
	var ico bytes.Buffer
	write := func(v any) { _ = binary.Write(&ico, binary.LittleEndian, v) }
	write(uint16(0)) // reserved
	write(uint16(1)) // type: icon
	write(uint16(1)) // one image
	ico.WriteByte(size)
	ico.WriteByte(size)
	ico.WriteByte(0)  // no palette
	ico.WriteByte(0)  // reserved
	write(uint16(1))  // colour planes
	write(uint16(32)) // bits per pixel
	write(uint32(encoded.Len()))
	write(uint32(6 + 16)) // the image starts after the header and the entry
	ico.Write(encoded.Bytes())
	return ico.Bytes()
}
