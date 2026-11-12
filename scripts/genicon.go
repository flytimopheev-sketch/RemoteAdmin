//go:build ignore

package main

// genicon generates the RemoteAdmin application icon (PNG 128x128)
// without external dependencies.

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
)

func main() {
	size := 128
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// rounded-rect background gradient (teal -> dark)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r := 24.0 + 16.0*float64(x)/float64(size)
			g := 30.0 + 120.0*float64(y)/float64(size) + 40.0*float64(x)/float64(size)
			b := 34.0 + 60.0*float64(x)/float64(size)
			// rounded corners mask
			radius := 26.0
			if !inRounded(float64(x), float64(y), float64(size), float64(size), radius) {
				continue
			}
			c := color.RGBA{uint8(r), uint8(g), uint8(b), 255}
			img.Set(x, y, c)
		}
	}

	// monitor frame
	drawRoundRect(img, 22, 26, 84, 54, 8, color.RGBA{232, 234, 237, 255})
	drawRoundRect(img, 28, 32, 72, 42, 5, color.RGBA{16, 20, 26, 255})
	// terminal prompt
	for i := 0; i < 5; i++ {
		w := 20.0 - float64(i)*2.5
		if i == 4 {
			w = 10
		}
		drawRect(img, 34, 40+i*7, int(w), 3, color.RGBA{52, 211, 153, 255})
	}
	// stand
	drawRect(img, 52, 80, 24, 6, color.RGBA{232, 234, 237, 255})
	drawRoundRect(img, 42, 86, 44, 8, 4, color.RGBA{232, 234, 237, 255})
	// nodes (cluster)
	drawCircle(img, 24, 100, 8, color.RGBA{52, 211, 153, 255})
	drawCircle(img, 64, 104, 8, color.RGBA{52, 211, 153, 160})
	drawCircle(img, 104, 100, 8, color.RGBA{52, 211, 153, 90})
	// links
	drawLine(img, 30, 99, 58, 103, color.RGBA{52, 211, 153, 140})
	drawLine(img, 70, 103, 98, 99, color.RGBA{52, 211, 153, 140})

	out, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		panic(err)
	}
}

func inRounded(x, y, w, h, r float64) bool {
	cx, cy := math.Max(r, math.Min(x, w-r)), math.Max(r, math.Min(y, h-r))
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func drawRoundRect(img *image.RGBA, x, y, w, h, r int, c color.RGBA) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			fx, fy := float64(xx-x), float64(yy-y)
			fw, fh, fr := float64(w), float64(h), float64(r)
			cx := math.Max(fr, math.Min(fx, fw-fr))
			cy := math.Max(fr, math.Min(fy, fh-fr))
			if (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= fr*fr {
				img.Set(xx, yy, c)
			}
		}
	}
}

func drawRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{c}, image.Point{}, draw.Src)
}

func drawCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				img.Set(x, y, c)
			}
		}
	}
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := math.Abs(float64(x1 - x0))
	dy := math.Abs(float64(y1 - y0))
	n := int(dx + dy)
	if n == 0 {
		return
	}
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		img.Set(int(float64(x0)+t*float64(x1-x0)), int(float64(y0)+t*float64(y1-y0)), c)
	}
}
