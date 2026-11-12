package main

// icons.go — programmatic UI icons for RemoteAdmin.
//
// Icons are drawn in a 24×24 design space, rasterized at 4× supersampling
// (binary coverage + box downsample gives free anti-aliasing) and cached as
// PNG files in a per-user temp dir. No external assets needed.
//
// Style (v1.7.0): sidebar glyphs are SOLID white shapes with carved-out
// details, sitting on colored gradient chips. Filled silhouettes read far
// better at 17–18 px than thin line art — the icons stay crisp and
// unambiguous ("мутные иконки" fix).

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
)

const iconDesign = 24 // design space size
const iconSS = 4      // supersampling factor

type icoBuf struct {
	px int
	a  []float64
}

func newIco() *icoBuf {
	n := iconDesign * iconSS
	return &icoBuf{px: n, a: make([]float64, n*n)}
}

func ic01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// cov sets alpha of a stroke/fill given distance-to-shape in design units.
func (b *icoBuf) cov(x0, y0, x1, y1, halfW float64) {
	b.paint(x0, y0, x1, y1, halfW, 1)
}

// covCut carves a stroke OUT of the existing shape (sets alpha 0).
func (b *icoBuf) covCut(x0, y0, x1, y1, halfW float64) {
	b.paint(x0, y0, x1, y1, halfW, 0)
}

func (b *icoBuf) paint(x0, y0, x1, y1, halfW float64, val float64) {
	x0 *= iconSS
	y0 *= iconSS
	x1 *= iconSS
	y1 *= iconSS
	hw := halfW * iconSS
	px0 := int(math.Max(0, math.Min(x0, x1)-hw-2))
	px1 := int(math.Min(float64(b.px), math.Max(x0, x1)+hw+2))
	py0 := int(math.Max(0, math.Min(y0, y1)-hw-2))
	py1 := int(math.Min(float64(b.px), math.Max(y0, y1)+hw+2))
	for py := py0; py < py1; py++ {
		for px := px0; px < px1; px++ {
			fx, fy := float64(px)+0.5, float64(py)+0.5
			// distance to segment
			dx, dy := x1-x0, y1-y0
			l2 := dx*dx + dy*dy
			var d float64
			if l2 == 0 {
				d = math.Hypot(fx-x0, fy-y0)
			} else {
				t := ic01((fx-x0)*dx + (fy-y0)*dy)
				t /= l2
				d = math.Hypot(fx-(x0+t*dx), fy-(y0+t*dy))
			}
			if d <= hw {
				b.a[py*b.px+px] = val
			}
		}
	}
}

func rrectDist(fx, fy, x, y, w, h, r float64) float64 {
	cx, cy := x+w/2, y+h/2
	hx, hy := w/2-r, h/2-r
	qx := math.Abs(fx-cx) - hx
	qy := math.Abs(fy-cy) - hy
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - r
}

func (b *icoBuf) rrect(x, y, w, h, r, sw float64, val float64, mode int) {
	// mode 0: fill inside; 1: stroke ring; 2: cut inside; 3: cut ring
	hw := sw / 2 * iconSS
	px0 := int(math.Max(0, (x-hw)*iconSS-2))
	px1 := int(math.Min(float64(b.px), (x+w+hw)*iconSS+2))
	py0 := int(math.Max(0, (y-hw)*iconSS-2))
	py1 := int(math.Min(float64(b.px), (y+h+hw)*iconSS+2))
	for py := py0; py < py1; py++ {
		for px := px0; px < px1; px++ {
			fx, fy := (float64(px)+0.5)/iconSS, (float64(py)+0.5)/iconSS
			d := rrectDist(fx, fy, x, y, w, h, r)
			var inside bool
			switch mode {
			case 0, 2:
				inside = d <= 0
			default:
				inside = math.Abs(d)*iconSS <= hw
			}
			if inside {
				if mode <= 1 {
					b.a[py*b.px+px] = 1
				} else {
					b.a[py*b.px+px] = 0
				}
			}
		}
	}
}

func (b *icoBuf) rrectFill(x, y, w, h, r float64)          { b.rrect(x, y, w, h, r, 0, 1, 0) }
func (b *icoBuf) rrectCut(x, y, w, h, r float64)           { b.rrect(x, y, w, h, r, 0, 0, 2) }
func (b *icoBuf) rrectStroke(x, y, w, h, r, sw float64)    { b.rrect(x, y, w, h, r, sw, 1, 1) }
func (b *icoBuf) rrectCutStroke(x, y, w, h, r, sw float64) { b.rrect(x, y, w, h, r, sw, 0, 3) }

func (b *icoBuf) circle(cx, cy, r float64, val float64) {
	px0 := int(math.Max(0, (cx-r)*iconSS-1))
	px1 := int(math.Min(float64(b.px), (cx+r)*iconSS+1))
	py0 := int(math.Max(0, (cy-r)*iconSS-1))
	py1 := int(math.Min(float64(b.px), (cy+r)*iconSS+1))
	for py := py0; py < py1; py++ {
		for px := px0; px < px1; px++ {
			fx, fy := (float64(px)+0.5)/iconSS, (float64(py)+0.5)/iconSS
			if math.Hypot(fx-cx, fy-cy) <= r {
				b.a[py*b.px+px] = val
			}
		}
	}
}

func (b *icoBuf) circleFill(cx, cy, r float64) { b.circle(cx, cy, r, 1) }
func (b *icoBuf) circleCut(cx, cy, r float64)  { b.circle(cx, cy, r, 0) }

func (b *icoBuf) circleStroke(cx, cy, r, sw float64) {
	hw := sw / 2 * iconSS
	px0 := int(math.Max(0, (cx-r-sw)*iconSS-1))
	px1 := int(math.Min(float64(b.px), (cx+r+sw)*iconSS+1))
	py0 := int(math.Max(0, (cy-r-sw)*iconSS-1))
	py1 := int(math.Min(float64(b.px), (cy+r+sw)*iconSS+1))
	for py := py0; py < py1; py++ {
		for px := px0; px < px1; px++ {
			fx, fy := (float64(px)+0.5)/iconSS, (float64(py)+0.5)/iconSS
			d := math.Abs(math.Hypot(fx-cx, fy-cy)-r) * iconSS
			if d <= hw {
				b.a[py*b.px+px] = 1
			}
		}
	}
}

// triFill fills a triangle (barycentric test).
func (b *icoBuf) triFill(ax, ay, bx, by, cx, cy float64) { b.tri(ax, ay, bx, by, cx, cy, 1) }

// triCut carves a triangle out of the shape.
func (b *icoBuf) triCut(ax, ay, bx, by, cx, cy float64) { b.tri(ax, ay, bx, by, cx, cy, 0) }

func (b *icoBuf) tri(ax, ay, bx, by, cx, cy, val float64) {
	px0 := int(math.Max(0, math.Min(ax, math.Min(bx, cx))*iconSS-1))
	px1 := int(math.Min(float64(b.px), math.Max(ax, math.Max(bx, cx))*iconSS+1))
	py0 := int(math.Max(0, math.Min(ay, math.Min(by, cy))*iconSS-1))
	py1 := int(math.Min(float64(b.px), math.Max(ay, math.Max(by, cy))*iconSS+1))
	sgn := func(x1, y1, x2, y2, x3, y3 float64) float64 {
		return (x1-x3)*(y2-y3) - (x2-x3)*(y1-y3)
	}
	for py := py0; py < py1; py++ {
		for px := px0; px < px1; px++ {
			fx, fy := (float64(px)+0.5)/iconSS, (float64(py)+0.5)/iconSS
			d1 := sgn(fx, fy, ax, ay, bx, by)
			d2 := sgn(fx, fy, bx, by, cx, cy)
			d3 := sgn(fx, fy, cx, cy, ax, ay)
			neg := d1 < 0 || d2 < 0 || d3 < 0
			pos := d1 > 0 || d2 > 0 || d3 > 0
			if !(neg && pos) {
				b.a[py*b.px+px] = val
			}
		}
	}
}

// polyline strokes a chain of segments.
func (b *icoBuf) polyline(pts [][2]float64, sw float64) {
	for i := 0; i+1 < len(pts); i++ {
		b.cov(pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1], sw/2)
	}
}

// polylineCut carves a chain of segments out of the shape.
func (b *icoBuf) polylineCut(pts [][2]float64, sw float64) {
	for i := 0; i+1 < len(pts); i++ {
		b.covCut(pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1], sw/2)
	}
}

// ---------- icon painters (24×24 design space) ----------
//
// Навигационные значки — ЗАПОЛНЕННЫЕ силуэты с вырезанными деталями:
// на цветной плитке белый сплошной глиф читается мгновенно.

var iconPainters = map[string]func(b *icoBuf){
	// сервер: два блока с вырезанными индикатором и «лотком»
	"server": func(b *icoBuf) {
		b.rrectFill(2.6, 3.4, 18.8, 7.6, 2.8)
		b.circleCut(6.1, 7.2, 1.2)
		b.covCut(10.8, 7.2, 18.2, 7.2, 0.75)
		b.rrectFill(2.6, 13.0, 18.8, 7.6, 2.8)
		b.circleCut(6.1, 16.8, 1.2)
		b.covCut(10.8, 16.8, 18.2, 16.8, 0.75)
	},
	// плейбук: плитка с ВЫРЕЗАННЫМ треугольником «пуск»
	"play": func(b *icoBuf) {
		b.rrectFill(2.8, 2.8, 18.4, 18.4, 5.6)
		b.triCut(9.6, 7.8, 9.6, 16.2, 17.2, 12.0)
	},
	// шаблоны: две карточки, задняя просвечивает щелью между ними
	"copy": func(b *icoBuf) {
		b.rrectFill(3.0, 3.0, 12.6, 12.6, 3.2)
		b.rrectCutStroke(8.6, 8.6, 12.4, 12.4, 3.2, 1.7)
		b.rrectFill(8.6, 8.6, 12.4, 12.4, 3.2)
	},
	// цепочка: два звена-кольца, переплетённые в середине
	"chain": func(b *icoBuf) {
		b.rrectFill(2.4, 8.6, 12.0, 6.8, 3.4)
		b.rrectFill(9.6, 8.6, 12.0, 6.8, 3.4)
		b.rrectCut(3.7, 9.9, 9.4, 4.2, 2.1)
		b.rrectCut(10.9, 9.9, 9.4, 4.2, 2.1)
	},
	// планировщик: будильник с вырезанным циферблатом
	"clock": func(b *icoBuf) {
		b.circleFill(12, 12.4, 8.8)
		b.covCut(12, 7.4, 12, 12.6, 0.8)
		b.covCut(12.4, 12.6, 16.4, 15.2, 0.8)
		b.circleCut(12, 12.4, 0.85)
		b.covCut(3.9, 4.7, 6.6, 3.1, 0.8)
		b.covCut(20.1, 4.7, 17.4, 3.1, 0.8)
	},
	// мониторинг: экран с вырезанной пульсирующей линией
	"pulse": func(b *icoBuf) {
		b.rrectFill(2.2, 4.0, 19.6, 16.0, 4.0)
		b.polylineCut([][2]float64{{4.4, 12}, {7.6, 12}, {10.2, 7.2}, {14.0, 16.8}, {16.4, 12}, {19.6, 12}}, 2.1)
	},
	// SFTP: папка с вырезанной линией под ярлыком
	"folder": func(b *icoBuf) {
		b.rrectFill(2.4, 5.4, 8.6, 4.6, 1.9)
		b.rrectFill(2.4, 7.6, 19.2, 11.2, 2.8)
		b.covCut(2.4, 10.0, 21.6, 10.0, 0.6)
	},
	// терминалы: окно с вырезанным промптом ">_"
	"term": func(b *icoBuf) {
		b.rrectFill(2.2, 3.4, 19.6, 17.2, 4.0)
		b.polylineCut([][2]float64{{6.2, 8.6}, {10.0, 12.0}, {6.2, 15.4}}, 1.7)
		b.covCut(12.6, 15.6, 17.2, 15.6, 0.85)
	},
	// журнал: документ с вырезанными строками
	"doc": func(b *icoBuf) {
		b.rrectFill(4.4, 2.6, 15.2, 18.8, 3.2)
		b.covCut(7.6, 7.8, 16.4, 7.8, 0.95)
		b.covCut(7.6, 11.8, 16.4, 11.8, 0.95)
		b.covCut(7.6, 15.8, 12.8, 15.8, 0.95)
	},
	// ползунки: настройки
	"sliders": func(b *icoBuf) {
		b.polyline([][2]float64{{3, 7}, {21, 7}}, 1.7)
		b.polyline([][2]float64{{3, 12}, {21, 12}}, 1.7)
		b.polyline([][2]float64{{3, 17}, {21, 17}}, 1.7)
		b.circleFill(15, 7, 2.4)
		b.circleFill(8.6, 12, 2.4)
		b.circleFill(17.4, 17, 2.4)
	},
	// moon: dark theme active
	"moon": func(b *icoBuf) {
		b.circleFill(11.4, 12.6, 7.6)
		b.circleCut(16.6, 8.2, 6.8)
	},
	// sun: light theme active
	"sun": func(b *icoBuf) {
		b.circleStroke(12, 12, 4.2, 1.8)
		for i := 0; i < 8; i++ {
			a := float64(i) * math.Pi / 4
			b.cov(12+6.4*math.Cos(a), 12+6.4*math.Sin(a), 12+9.4*math.Cos(a), 12+9.4*math.Sin(a), 0.85)
		}
	},
	// broadcast/quick command
	"bolt": func(b *icoBuf) {
		b.rrectFill(3, 3, 18, 18, 5.2)
		b.polylineCut([][2]float64{{13.2, 6.6}, {10.4, 12.4}, {13.4, 12.4}, {11.0, 17.6}}, 1.7)
	},
	// «i» в круге: о программе
	"info": func(b *icoBuf) {
		b.circleFill(12, 12, 9.2)
		b.circleCut(12, 7.6, 1.35)
		b.rrectCut(10.9, 10.4, 2.2, 7.2, 1.1)
	},
	// звезда: избранное
	"star": func(b *icoBuf) {
		cx, cy, r1, r2 := 12.0, 12.4, 9.0, 3.8
		var pts [][2]float64
		for i := 0; i < 10; i++ {
			r := r1
			if i%2 == 1 {
				r = r2
			}
			a := -math.Pi/2 + float64(i)*math.Pi/5
			pts = append(pts, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		}
		pts = append(pts, pts[0])
		b.polyline(pts, 1.8)
	},
	// стрелки сравнения: diff панелей SFTP
	"diff": func(b *icoBuf) {
		b.polyline([][2]float64{{4.6, 8.4}, {19.4, 8.4}}, 1.8)
		b.polyline([][2]float64{{16.2, 5.2}, {19.4, 8.4}, {16.2, 11.6}}, 1.8)
		b.polyline([][2]float64{{19.4, 15.6}, {4.6, 15.6}}, 1.8)
		b.polyline([][2]float64{{7.8, 12.4}, {4.6, 15.6}, {7.8, 18.8}}, 1.8)
	},
}

// iconRGBA renders an icon kind in the given hex color at given pixel size.
func iconRGBA(kind, hexcolor string, size int) (*image.RGBA, bool) {
	return iconRGBA2(kind, hexcolor, hexcolor, 1.0, size)
}

// iconRGBA2 renders an icon kind with a vertical two-color gradient
// (hexA сверху → hexB снизу) и общим множителем прозрачности alpha.
func iconRGBA2(kind, hexA, hexB string, alpha float64, size int) (*image.RGBA, bool) {
	p, ok := iconPainters[kind]
	if !ok {
		return nil, false
	}
	b := newIco()
	p(b)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	ar, ag, ab, _ := hexToRGBA(hexA)
	br, bg, bb, _ := hexToRGBA(hexB)
	if os.Getenv("RA_ICON_DEBUG") != "" {
		fmt.Printf("[icondbg] kind=%s hexA=%q -> (%.3f,%.3f,%.3f) hexB=%q -> (%.3f,%.3f,%.3f) alpha=%.2f size=%d\n",
			kind, hexA, ar, ag, ab, hexB, br, bg, bb, alpha, size)
	}
	if alpha <= 0 {
		alpha = 1
	}
	ss := iconDesign * iconSS
	for y := 0; y < size; y++ {
		t := 0.0
		if size > 1 {
			t = float64(y) / float64(size-1)
		}
		r := ar + (br-ar)*t
		g := ag + (bg-ag)*t
		bl := ab + (bb-ab)*t
		for x := 0; x < size; x++ {
			var sum float64
			// map each destination pixel to its ss×ss footprint
			sx0 := x * ss / size
			sx1 := (x + 1) * ss / size
			sy0 := y * ss / size
			sy1 := (y + 1) * ss / size
			cnt := 0
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					sum += b.a[sy*ss+sx]
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			av := sum / float64(cnt)
			if av <= 0.02 {
				continue
			}
			i := img.PixOffset(x, y)
			// NB: image.RGBA хранит ПРЕМУЛЬТИПЛИЦИРОВАННЫЙ цвет. Раньше мы
			// писали straight-alpha — png-энкодер при обратном преобразовании
			// переполнял uint8 и «мутировал» цвета (жалоба «иконки мутные»).
			pm := ic01(av * alpha)
			img.Pix[i] = uint8(ic01(r*pm) * 255)
			img.Pix[i+1] = uint8(ic01(g*pm) * 255)
			img.Pix[i+2] = uint8(ic01(bl*pm) * 255)
			img.Pix[i+3] = uint8(pm * 255)
		}
	}
	return img, true
}

// iconCacheDir returns (and creates) the temp dir for rendered icons.
func iconCacheDir() string {
	d := filepath.Join(os.TempDir(), fmt.Sprintf("remoteadmin-icons-%d", os.Getuid()))
	_ = os.MkdirAll(d, 0700)
	return d
}

var iconPixbufs = map[string]*gdk.Pixbuf{}

// iconPixbufGrad returns a pixbuf for kind/colors/alpha/size, rendered lazily.
func iconPixbufGrad(kind, hexA, hexB string, alpha float64, size int) *gdk.Pixbuf {
	key := fmt.Sprintf("%s_%s_%s_%.2f_%d", kind, hexA, hexB, alpha, size)
	if p, ok := iconPixbufs[key]; ok && p != nil {
		return p
	}
	img, ok := iconRGBA2(kind, hexA, hexB, alpha, size)
	if !ok {
		return nil
	}
	fn := filepath.Join(iconCacheDir(), key+".png")
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
	iconPixbufs[key] = pb
	return pb
}

// iconPixbuf returns a pixbuf for kind/color/size, rendering+loading lazily.
func iconPixbuf(kind, hexcolor string, size int) *gdk.Pixbuf {
	return iconPixbufGrad(kind, hexcolor, hexcolor, 1.0, size)
}

// setIcon installs a rendered icon into a GtkImage.
func setIcon(img *gtk.Image, kind, hexcolor string, size int) {
	pb := iconPixbufGrad(kind, hexcolor, hexcolor, 1.0, size)
	if pb != nil {
		img.SetFromPixbuf(pb)
	}
}

// setIconGrad installs a gradient-rendered icon (современный duotone).
func setIconGrad(img *gtk.Image, kind, hexA, hexB string, alpha float64, size int) {
	pb := iconPixbufGrad(kind, hexA, hexB, alpha, size)
	if pb != nil {
		img.SetFromPixbuf(pb)
	}
}

var _ = fmt.Sprintf
