package main

import "testing"

func eqSeg(t *testing.T, seg ansiSeg, text, color string, bold bool) {
        t.Helper()
        if seg.Text != text || seg.Color != color || seg.Bold != bold {
                t.Fatalf("segment mismatch: got (%q, %q, %v), want (%q, %q, %v)",
                        seg.Text, seg.Color, seg.Bold, text, color, bold)
        }
}

func TestANSIBasic(t *testing.T) {
        segs, rest := parseANSIStream("\x1b[31mОшибка!\x1b[0m всё ок")
        if rest != "" {
                t.Fatalf("rest=%q", rest)
        }
        if len(segs) != 2 {
                t.Fatalf("len=%d", len(segs))
        }
        eqSeg(t, segs[0], "Ошибка!", "#f87171", false)
        eqSeg(t, segs[1], " всё ок", "", false)
}

func TestANSIBoldBright(t *testing.T) {
        segs, _ := parseANSIStream("\x1b[1;92mOK\x1b[m tail")
        if len(segs) != 2 {
                t.Fatalf("len=%d", len(segs))
        }
        eqSeg(t, segs[0], "OK", "#6ee7b7", true)
        eqSeg(t, segs[1], " tail", "", false)
}

func TestANSIChunkSplit(t *testing.T) {
        // последовательность, разрезанная посередине сетевым чанком
        segs1, rest := parseANSIStream("prefix \x1b[3")
        if rest != "\x1b[3" {
                t.Fatalf("rest=%q", rest)
        }
        if len(segs1) != 1 {
                t.Fatalf("len1=%d", len(segs1))
        }
        eqSeg(t, segs1[0], "prefix ", "", false)
        segs2, rest2 := parseANSIStream(rest + "4mцвет")
        if rest2 != "" {
                t.Fatalf("rest2=%q", rest2)
        }
        if len(segs2) != 1 {
                t.Fatalf("len2=%d", len(segs2))
        }
        // продолжение \x1b[3 + 4m = \x1b[34m — синий
        eqSeg(t, segs2[0], "цвет", "#60a5fa", false)
}

func TestANSIOscDropped(t *testing.T) {
        // заголовок окна OSC и CSI-перемещения курсора должны исчезнуть
        segs, _ := parseANSIStream("\x1b]0;title\x07текст\x1b[2Jчисто")
        if len(segs) != 2 {
                t.Fatalf("len=%d", len(segs))
        }
        eqSeg(t, segs[0], "текст", "", false)
        eqSeg(t, segs[1], "чисто", "", false)
}

func TestANSI256(t *testing.T) {
        segs, _ := parseANSIStream("\x1b[38;5;196mкрасный")
        if len(segs) != 1 {
                t.Fatalf("len=%d", len(segs))
        }
        if segs[0].Color != "#f87171" {
                t.Fatalf("color=%q", segs[0].Color)
        }
}

func TestStripANSI(t *testing.T) {
        s := stripANSI("\x1b[31mabc\x1b[0m\x1b]0;t\x07def")
        if s != "abcdef" {
                t.Fatalf("got %q", s)
        }
}
