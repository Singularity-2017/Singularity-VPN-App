package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// icon_ico draws the tray icon -- a ring, accent-coloured while a tunnel is
// up, grey otherwise -- and wraps it as a .ico, which is what the Windows
// tray takes. Drawing it keeps a binary asset out of the repository and the
// two states from drifting apart.
func icon_ico(connected bool) []byte {
	const size = 32
	ring := color.NRGBA{R: 0x8a, G: 0x96, B: 0xa0, A: 0xff}
	if connected {
		ring = color.NRGBA{R: 0x4b, G: 0xe3, B: 0xdf, A: 0xff}
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	center := float64(size-1) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-center, float64(y)-center)
			// A ring from radius 9.5 to 14, and a dot under 4, each with a
			// one-pixel soft edge so it does not look jagged at 100%.
			alpha := math.Max(coverage(d, 9.5, 14), coverage(d, -1, 4))
			if alpha > 0 {
				c := ring
				c.A = uint8(alpha * 255)
				img.SetNRGBA(x, y, c)
			}
		}
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
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

// coverage is how much of the pixel at distance d lies between inner and
// outer, with a one-pixel ramp at each edge.
func coverage(d, inner, outer float64) float64 {
	a := math.Min(d-inner, outer-d) + 0.5
	return math.Max(0, math.Min(1, a))
}
