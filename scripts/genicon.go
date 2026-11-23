//go:build ignore

package main

// genicon генерирует icon.png — фирменный логотип RemoteAdmin (v1.12.0):
// скруглённый квадрат с диагональным градиентом индиго→циан, белый
// монитор с тёмным экраном, зелёное приглашение и три узла кластера.
// Тот же рисунок встроен в приложение (cmd/remoteadmin/logo.go).
//
// Запуск: go run scripts/genicon.go icon.png

import (
	"image"
	"image/png"
	"math"
	"os"
)

const D = 256.0 // дизайн-пространство

func lerp(a, b float64, t float64) float64 { return a + (b-a)*t }

func lerpHex(aR, aG, aB, bR, bG, bB float64, t float64) (float64, float64, float64) {
	return lerp(aR, bR, t), lerp(aG, bG, t), lerp(aB, bB, t)
}

func rrectDist(fx, fy, x, y, w, h, r float64) float64 {
	cx, cy := x+w/2, y+h/2
	hx, hy := w/2-r, h/2-r
	qx := math.Abs(fx-cx) - hx
	qy := math.Abs(fy-cy) - hy
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - r
}

type shapeFn func(fx, fy float64) bool

func rr(x, y, w, h, rad float64) shapeFn {
	return func(fx, fy float64) bool { return rrectDist(fx, fy, x, y, w, h, rad) <= 0 }
}

func circ(cx, cy, r float64) shapeFn {
	return func(fx, fy float64) bool { return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= r*r }
}

func tri(x1, y1, x2, y2, x3, y3 float64) shapeFn {
	return func(fx, fy float64) bool {
		d1 := (fx-x2)*(y1-y2) - (x1-x2)*(fy-y2)
		d2 := (fx-x3)*(y2-y3) - (x2-x3)*(fy-y3)
		d3 := (fx-x1)*(y3-y1) - (x3-x1)*(fy-y1)
		neg := d1 < 0 || d2 < 0 || d3 < 0
		pos := d1 > 0 || d2 > 0 || d3 > 0
		return !(neg && pos)
	}
}

func seg(x1, y1, x2, y2, hw float64) shapeFn {
	return func(fx, fy float64) bool {
		dx, dy := x2-x1, y2-y1
		l2 := dx*dx + dy*dy
		var d float64
		if l2 == 0 {
			d = math.Hypot(fx-x1, fy-y1)
		} else {
			t := ((fx-x1)*dx + (fy-y1)*dy) / l2
			t = math.Max(0, math.Min(1, t))
			d = math.Hypot(fx-(x1+t*dx), fy-(y1+t*dy))
		}
		return d <= hw
	}
}

type shape struct {
	inside     shapeFn
	r, g, b, a float64
}

func main() {
	size := 256
	ss := 3 // суперсэмплинг 3×3 на пиксель

	shapes := []shape{
		{inside: rr(0, 0, D, D, 60), r: -1}, // фон с градиентом
		{inside: rr(44, 44, 168, 116, 18), r: 245, g: 247, b: 252, a: 255},
		{inside: rr(56, 56, 144, 92, 10), r: 13, g: 18, b: 34, a: 255},
		{inside: tri(76, 86, 96, 102, 76, 118), r: 52, g: 211, b: 153, a: 255},
		{inside: rr(106, 96, 52, 13, 4), r: 52, g: 211, b: 153, a: 235},
		{inside: rr(116, 160, 24, 16, 3), r: 245, g: 247, b: 252, a: 230},
		{inside: rr(92, 176, 72, 14, 7), r: 245, g: 247, b: 252, a: 255},
		{inside: seg(78, 222, 110, 226, 2.6), r: 255, g: 255, b: 255, a: 90},
		{inside: seg(146, 226, 178, 222, 2.6), r: 255, g: 255, b: 255, a: 90},
		{inside: circ(60, 222, 17), r: 255, g: 255, b: 255, a: 255},
		{inside: circ(128, 226, 17), r: 255, g: 255, b: 255, a: 185},
		{inside: circ(196, 222, 17), r: 255, g: 255, b: 255, a: 130},
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	set := func(x, y int, r, g, b, a float64) {
		if x < 0 || y < 0 || x >= size || y >= size {
			return
		}
		i := img.PixOffset(x, y)
		img.Pix[i] = clampB(r)
		img.Pix[i+1] = clampB(g)
		img.Pix[i+2] = clampB(b)
		img.Pix[i+3] = clampB(a)
	}

	s := float64(size) / D
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var accR, accG, accB, accA float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := (float64(px) + (float64(sx)+0.5)/float64(ss)) / s
					fy := (float64(py) + (float64(sy)+0.5)/float64(ss)) / s
					var cr, cg, cb, ca float64
					for _, sh := range shapes {
						if !sh.inside(fx, fy) {
							continue
						}
						if sh.r < 0 {
							// градиент фона #7C8CFF → #5F6FF0 → #22D3EE
							t := (fx + fy) / (D * 1.6)
							t = math.Max(0, math.Min(1, t))
							if t < 0.5 {
								cr, cg, cb = lerpHex(124, 140, 255, 95, 111, 240, t*2)
							} else {
								cr, cg, cb = lerpHex(95, 111, 240, 34, 211, 238, (t-0.5)*2)
							}
							ca = 255
						} else {
							cr, cg, cb, ca = sh.r, sh.g, sh.b, sh.a
						}
					}
					accR += cr
					accG += cg
					accB += cb
					accA += ca
				}
			}
			n := float64(ss * ss)
			set(px, py, accR/n, accG/n, accB/n, accA/n)
		}
	}

	out := os.Args[1]
	if out == "" {
		out = "icon.png"
	}
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func clampB(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}
