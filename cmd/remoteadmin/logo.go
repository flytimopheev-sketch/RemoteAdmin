package main

// logo.go — фирменный логотип RemoteAdmin (v1.12.0).
//
// Рисуется программно (как и иконки интерфейса): скруглённый квадрат с
// диагональным градиентом индиго→циан, белый монитор с тёмным экраном,
// зелёное приглашение «▸▮» и три узла-кружка снизу (кластер хостов).
// Один и тот же рисунок используется: в шапке окна, в «О программе»
// и в icon.png (генерируется scripts/genicon.go при сборке пакетов).

import (
        "fmt"
        "image"
        "image/png"
        "math"
        "os"
        "path/filepath"

        "github.com/gotk3/gotk3/gdk"
)

// lerpHex — линейная интерполяция двух #rrggbb цветов.
func lerpHex(a, b string, t float64) (float64, float64, float64) {
        var r1, g1, b1, r2, g2, b2 int
        _, _ = fmt.Sscanf(a, "#%02x%02x%02x", &r1, &g1, &b1)
        _, _ = fmt.Sscanf(b, "#%02x%02x%02x", &r2, &g2, &b2)
        return float64(r1)+(float64(r2)-float64(r1))*t,
                float64(g1)+(float64(g2)-float64(g1))*t,
                float64(b1)+(float64(b2)-float64(b1))*t
}

// logoImage рисует логотип в размере size×size (с 3× сглаживанием).
func logoImage(size int) *image.RGBA {
        const D = 256.0 // дизайн-пространство
        s := float64(size) / D
        ss := 3 // суперсэмплинг 3×3 на пиксель

        img := image.NewRGBA(image.Rect(0, 0, size, size))
        set := func(x, y int, r, g, b, a float64) {
                if x < 0 || y < 0 || x >= size || y >= size {
                        return
                }
                i := img.PixOffset(x, y)
                img.Pix[i] = clampByte(r)
                img.Pix[i+1] = clampByte(g)
                img.Pix[i+2] = clampByte(b)
                img.Pix[i+3] = clampByte(a)
        }
        // формы: (функция принадлежности, цвет, альфа)
        type shape struct {
                inside func(fx, fy float64) bool
                r, g, b, a float64
        }
        rr := func(x, y, w, h, rad float64) func(fx, fy float64) bool {
                return func(fx, fy float64) bool { return rrectDist(fx, fy, x, y, w, h, rad) <= 0 }
        }
        circ := func(cx, cy, r float64) func(fx, fy float64) bool {
                return func(fx, fy float64) bool {
                        return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= r*r
                }
        }
        tri := func(x1, y1, x2, y2, x3, y3 float64) func(fx, fy float64) bool {
                return func(fx, fy float64) bool {
                        d1 := (fx-x2)*(y1-y2) - (x1-x2)*(fy-y2)
                        d2 := (fx-x3)*(y2-y3) - (x2-x3)*(fy-y3)
                        d3 := (fx-x1)*(y3-y1) - (x3-x1)*(fy-y1)
                        neg := d1 < 0 || d2 < 0 || d3 < 0
                        pos := d1 > 0 || d2 > 0 || d3 > 0
                        return !(neg && pos)
                }
        }
        seg := func(x1, y1, x2, y2, hw float64) func(fx, fy float64) bool {
                return func(fx, fy float64) bool {
                        dx, dy := x2-x1, y2-y1
                        l2 := dx*dx + dy*dy
                        var d float64
                        if l2 == 0 {
                                d = math.Hypot(fx-x1, fy-y1)
                        } else {
                                t := ((fx-x1)*dx + (fy-y1)*dy) / l2
                                if t < 0 {
                                        t = 0
                                }
                                if t > 1 {
                                        t = 1
                                }
                                d = math.Hypot(fx-(x1+t*dx), fy-(y1+t*dy))
                        }
                        return d <= hw
                }
        }

        shapes := []shape{
                // фон: скруглённый квадрат, градиент по диагонали
                {inside: rr(0, 0, D, D, 60), r: -1}, // r=-1 → градиентный фон
                // монитор (белый корпус) и тёмный экран
                {inside: rr(44, 44, 168, 116, 18), r: 245, g: 247, b: 252, a: 255},
                {inside: rr(56, 56, 144, 92, 10), r: 13, g: 18, b: 34, a: 255},
                // приглашение: зелёный шеврон + курсор
                {inside: tri(76, 86, 96, 102, 76, 118), r: 52, g: 211, b: 153, a: 255},
                {inside: rr(106, 96, 52, 13, 4), r: 52, g: 211, b: 153, a: 235},
                // подставка
                {inside: rr(116, 160, 24, 16, 3), r: 245, g: 247, b: 252, a: 230},
                {inside: rr(92, 176, 72, 14, 7), r: 245, g: 247, b: 252, a: 255},
                // узлы кластера: три кружка + соединительные линии
                {inside: seg(78, 222, 110, 226, 2.6), r: 255, g: 255, b: 255, a: 90},
                {inside: seg(146, 226, 178, 222, 2.6), r: 255, g: 255, b: 255, a: 90},
                {inside: circ(60, 222, 17), r: 255, g: 255, b: 255, a: 255},
                {inside: circ(128, 226, 17), r: 255, g: 255, b: 255, a: 185},
                {inside: circ(196, 222, 17), r: 255, g: 255, b: 255, a: 130},
        }

        for py := 0; py < size; py++ {
                for px := 0; px < size; px++ {
                        var accR, accG, accB, accA float64
                        for sy := 0; sy < ss; sy++ {
                                for sx := 0; sx < ss; sx++ {
                                        fx := (float64(px)+(float64(sx)+0.5)/float64(ss)) / s
                                        fy := (float64(py)+(float64(sy)+0.5)/float64(ss)) / s
                                        // рисуем сверху вниз: последняя подходящая форма побеждает
                                        var cr, cg, cb, ca float64
                                        for _, sh := range shapes {
                                                if !sh.inside(fx, fy) {
                                                        continue
                                                }
                                                if sh.r < 0 {
                                                        // градиент фона: #7C8CFF → #5F6FF0 → #22D3EE
                                                        t := (fx + fy) / (D * 1.6)
                                                        if t > 1 {
                                                                t = 1
                                                        }
                                                        if t < 0.5 {
                                                                cr, cg, cb = lerpHex("#7C8CFF", "#5F6FF0", t*2)
                                                        } else {
                                                                cr, cg, cb = lerpHex("#5F6FF0", "#22D3EE", (t-0.5)*2)
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
        return img
}

func clampByte(v float64) uint8 {
        if v < 0 {
                return 0
        }
        if v > 255 {
                return 255
        }
        return uint8(v + 0.5)
}

// logoPixbuf — кэшированный GdkPixbuf логотипа нужного размера.
var logoPixbufs = map[int]*gdk.Pixbuf{}

func logoPixbuf(size int) *gdk.Pixbuf {
        if pb, ok := logoPixbufs[size]; ok {
                return pb
        }
        img := logoImage(size)
        fn := filepath.Join(iconCacheDir(), fmt.Sprintf("applogo_%d.png", size))
        f, err := os.Create(fn)
        if err != nil {
                return nil
        }
        err = png.Encode(f, img)
        f.Close()
        if err != nil {
                return nil
        }
        pb, err := gdk.PixbufNewFromFile(fn)
        if err != nil {
                return nil
        }
        logoPixbufs[size] = pb
        return pb
}
