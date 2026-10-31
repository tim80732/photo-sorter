// mkicon draws the app icon (the same camera as static/icon.svg) as a PNG
// for the Windows exe resource: go run ./tools/mkicon winres/icon.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	const size, ss = 256, 4 // supersample for smooth edges
	blue := color.NRGBA{0x2f, 0x6f, 0xdc, 0xff}
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	// Shapes in the SVG's 64x64 space.
	at := func(x, y float64) color.NRGBA {
		in := func(x0, y0, x1, y1, r float64) bool { // rounded rectangle
			cx := math.Max(x0+r, math.Min(x, x1-r))
			cy := math.Max(y0+r, math.Min(y, y1-r))
			return x >= x0 && x <= x1 && y >= y0 && y <= y1 && (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
		}
		d := math.Hypot(x-32, y-34)
		switch {
		case d <= 8:
			return blue
		case d <= 13:
			return white
		case in(46, 18, 53, 22, 2):
			return white
		case in(4, 12, 60, 56, 8):
			return blue
		case y >= 6 && y <= 12 && x >= 26-(12-y)*4/6 && x <= 38+(12-y)*4/6 && x >= 22 && x <= 42:
			return blue
		}
		return color.NRGBA{}
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := at((float64(px)+(float64(sx)+.5)/ss)*64/size, (float64(py)+(float64(sy)+.5)/ss)*64/size)
					af := float64(c.A) / 255
					r, g, b, a = r+float64(c.R)*af, g+float64(c.G)*af, b+float64(c.B)*af, a+af
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / n * 255)})
			}
		}
	}
	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	png.Encode(f, img)
}
