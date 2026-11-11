package main

// ansi.go — минимальный парсер ANSI-кодов (SGR) для цветного вывода.
//
// SSH-PTY возвращает xterm-последовательности вида ESC[31;1m; раньше они
// попадали в TextView «как есть», из-за чего терминалы выглядели мусором
// из кракозябр, а журнал — однотонной массой. Парсер превращает
// последовательности в готовые сегменты (текст + цвет + жирность),
// которые вкладки рисуют через TextTag.

import (
	"strconv"
	"strings"
)

// ansiSeg — фрагмент текста в едином стиле.
type ansiSeg struct {
	Text  string
	Color string // "" — обычный цвет темы
	Bold  bool
	Dim   bool
}

// ansiFg — палитра xterm: 0-7 обычные, 8-15 яркие (подобрана под тему Aurora).
var ansiFg = [16]string{
	"#7c86a0", // 0 black   (читаемый серый вместо чёрной дыры)
	"#f87171", // 1 red
	"#34d399", // 2 green
	"#fbbf24", // 3 yellow
	"#60a5fa", // 4 blue
	"#c084fc", // 5 magenta
	"#22d3ee", // 6 cyan
	"#d9e1f0", // 7 white
	"#9aa3b8", // 8 bright black
	"#fb7185", // 9 bright red
	"#6ee7b7", // 10 bright green
	"#fde68a", // 11 bright yellow
	"#93c5fd", // 12 bright blue
	"#d8b4fe", // 13 bright magenta
	"#67e8f9", // 14 bright cyan
	"#ffffff", // 15 bright white
}

// sgrState — накопленное состояние стиля между последовательностями.
type sgrState struct {
	color string
	bold  bool
	dim   bool
}

func (st *sgrState) reset() {
	st.color = ""
	st.bold = false
	st.dim = false
}

// applySGR применяет параметры последовательности ESC[...m.
func (st *sgrState) applySGR(params string) {
	if params == "" {
		st.reset()
		return
	}
	codes := strings.Split(params, ";")
	for i := 0; i < len(codes); i++ {
		n, err := strconv.Atoi(strings.TrimSpace(codes[i]))
		if err != nil {
			if codes[i] == "" {
				st.reset()
			}
			continue
		}
		switch {
		case n == 0:
			st.reset()
		case n == 1:
			st.bold = true
			st.dim = false
		case n == 2:
			st.dim = true
		case n == 22:
			st.bold = false
			st.dim = false
		case n >= 30 && n <= 37:
			st.color = ansiFg[n-30]
		case n == 39:
			st.color = ""
		case n >= 90 && n <= 97:
			st.color = ansiFg[n-90+8]
		case n == 38 || n == 48:
			// 38;5;N (256 цветов) или 38;2;R;G;B — приводим 256-палитру
			// к ближайшему базовому цвету, RGB пропускаем.
			if n == 38 && i+1 < len(codes) {
				switch codes[i+1] {
				case "5":
					if i+2 < len(codes) {
						if v, err2 := strconv.Atoi(codes[i+2]); err2 == nil {
							st.color = ansi256Color(v)
						}
						i += 2
					}
				case "2":
					i += 4 // R;G;B пропускаем (базовые цвета тем и хороши)
				}
			}
		}
		// 40-47/49/100-107 (фон) игнорируем — фон задаёт тема.
	}
}

// ansi256Color сводит xterm-256 к 16-палитре (0-15 — базовые, 16-231 —
// куб 6x6x6, 232-255 — оттенки серого).
func ansi256Color(v int) string {
	if v < 0 {
		return ""
	}
	if v < 16 {
		return ansiFg[v]
	}
	if v >= 232 {
		// 24 уровня серого → 3 ближайших
		g := (v - 232) * 255 / 23
		switch {
		case g < 96:
			return ansiFg[0]
		case g < 160:
			return ansiFg[8]
		case g < 220:
			return ansiFg[7]
		default:
			return ansiFg[15]
		}
	}
	// цветовой куб: выбираем компонент, доминирующий в оттенке
	idx := v - 16
	r := idx / 36
	g := (idx % 36) / 6
	b := idx % 6
	max := r
	if g > max {
		max = g
	}
	if b > max {
		max = b
	}
	switch {
	case r == max && g == max && b == max:
		if max == 0 {
			return ansiFg[0]
		}
		return ansiFg[7]
	case r >= 3 && g >= 3 && b >= 3:
		return ansiFg[15]
	case r > g && r > b:
		return ansiFg[1] // красная доминанта
	case g > r && g > b:
		return ansiFg[2] // зелёная
	case b > r && b > g:
		return ansiFg[4] // синяя
	case r == g && g == b:
		return ansiFg[6]
	default:
		return ansiFg[3]
	}
}

// parseANSIStream разбирает s на стилевые сегменты. Незавершённая
// escape-последовательность (обрезана границей сетевого чанка) возвращается
// в rest — её нужно дописать к следующему куску вывода.
func parseANSIStream(s string) (segs []ansiSeg, rest string) {
	var st sgrState
	var cur ansiSeg
	cur.Color = st.color
	flush := func() {
		if cur.Text != "" {
			segs = append(segs, cur)
			cur = ansiSeg{Color: st.color, Bold: st.bold, Dim: st.dim}
		}
	}
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			flush()
			if i+1 >= len(s) {
				return segs, s[i:] // обрезанная ESC
			}
			switch s[i+1] {
			case '[': // CSI: params 20-3F, final 40-7E
				j := i + 2
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
					j++
				}
				if j >= len(s) {
					return segs, s[i:] // обрезанная CSI
				}
				if s[j] == 'm' {
					st.applySGR(s[i+2 : j])
				}
				i = j + 1
			case ']': // OSC: до BEL или ESC \
				j := i + 2
				done := false
				for ; j < len(s); j++ {
					if s[j] == 0x07 {
						j++
						done = true
						break
					}
					if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
						j += 2
						done = true
						break
					}
				}
				if !done {
					return segs, s[i:] // обрезанная OSC
				}
				i = j
			case '(', ')': // выбор кодовой страницы: ESC ( B
				if i+2 < len(s) {
					i += 3
				} else {
					return segs, s[i:]
				}
			default: // прочие двухсимвольные (ESC 7, ESC =, …)
				i += 2
			}
			cur = ansiSeg{Color: st.color, Bold: st.bold, Dim: st.dim}
			continue
		}
		// обычный текст: копируем до следующего ESC
		j := strings.IndexByte(s[i:], 0x1b)
		var chunk string
		if j < 0 {
			chunk = s[i:]
			i = len(s)
		} else {
			chunk = s[i : i+j]
			i += j
		}
		chunk = strings.ReplaceAll(chunk, "\r\n", "\n")
		chunk = strings.ReplaceAll(chunk, "\r", "\n")
		cur.Text += chunk
	}
	flush()
	return segs, ""
}

// stripANSI удаляет все escape-последовательности (для экспорта/логов).
func stripANSI(s string) string {
	segs, rest := parseANSIStream(s)
	var b strings.Builder
	for _, sg := range segs {
		b.WriteString(sg.Text)
	}
	b.WriteString(stripANSIPlain(rest))
	return b.String()
}

// stripANSIPlain дочищает остатки без стилей (линейный проход).
func stripANSIPlain(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && (s[j] == '[' || s[j] == ']') {
				j++
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
					j++
				}
				if j < len(s) {
					j++
				}
			} else if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
