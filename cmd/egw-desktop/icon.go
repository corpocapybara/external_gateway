//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"math"
)

var iconColors = map[string][3]byte{
	"ok":      {0x2e, 0xa0, 0x5a},
	"warn":    {0xe0, 0x9a, 0x1a},
	"bad":     {0xd6, 0x3b, 0x2f},
	"unknown": {0x8a, 0x94, 0xa0},
}

// statusIcon draws a 32x32 ICO: a coloured disc with a white ring-and-dot gateway mark.
func statusIcon(state string) []byte {
	col, ok := iconColors[state]
	if !ok {
		col = iconColors["unknown"]
	}
	const size = 32
	pixels := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-size/2, float64(y)+0.5-size/2
			d := math.Hypot(dx, dy)
			var r, g, b, alpha float64
			if d <= 15 {
				r, g, b, alpha = float64(col[0]), float64(col[1]), float64(col[2]), math.Min(1, 15.5-d)
				if (d >= 7 && d <= 10) || d <= 3 {
					r, g, b = 255, 255, 255
				}
			}
			i := ((size-1-y)*size + x) * 4
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = byte(b), byte(g), byte(r), byte(alpha*255)
		}
	}
	maskRow := ((size + 31) / 32) * 4
	mask := make([]byte, maskRow*size)

	var bmp bytes.Buffer
	binary.Write(&bmp, binary.LittleEndian, struct {
		Size, Width, Height              int32
		Planes, BitCount                 uint16
		Compression, SizeImage           uint32
		XPels, YPels, ClrUsed, ClrImport int32
	}{40, size, size * 2, 1, 32, 0, uint32(len(pixels) + len(mask)), 0, 0, 0, 0})
	bmp.Write(pixels)
	bmp.Write(mask)

	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, struct{ Reserved, Type, Count uint16 }{0, 1, 1})
	binary.Write(&ico, binary.LittleEndian, struct {
		W, H, Colors, Reserved byte
		Planes, BitCount       uint16
		Bytes, Offset          uint32
	}{size, size, 0, 0, 1, 32, uint32(bmp.Len()), 22})
	ico.Write(bmp.Bytes())
	return ico.Bytes()
}
