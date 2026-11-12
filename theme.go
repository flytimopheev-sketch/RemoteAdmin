package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
)

// ---------------------------------------------------------------------------
// "Aurora" design system — layered dark/light surfaces, indigo accent,
// soft borders, rounded geometry (libadwaita-inspired, GTK3-compatible).
// ---------------------------------------------------------------------------

type palette struct {
	Bg        string // window base
	Side      string // chrome: sidebar, header, status bar
	Panel     string // cards
	PanelAlt  string // inputs, rows
	Hover     string // hover fill
	Text      string
	Dim       string
	Faint     string
	Accent    string
	AccentDim string
	OnAccent  string // text on accent fills
	Ok        string
	Danger    string
	Warn      string
	Info      string
	Border    string
	Shadow    string
}

var darkPalette = palette{
	Bg:        "#14161c",
	Side:      "#181b22",
	Panel:     "#1e222a",
	PanelAlt:  "#252a34",
	Hover:     "#2c3240",
	Text:      "#e8ebf4",
	Dim:       "#9aa3b8",
	Faint:     "#667089",
	Accent:    "#7c8cff",
	AccentDim: "#5f6ff0",
	OnAccent:  "#0d1126",
	Ok:        "#34d399",
	Danger:    "#f87171",
	Warn:      "#fbbf24",
	Info:      "#60a5fa",
	Border:    "rgba(255,255,255,0.07)",
	Shadow:    "rgba(0,0,0,0.60)",
}

var lightPalette = palette{
	Bg:        "#eef0f6",
	Side:      "#e6e9f1",
	Panel:     "#ffffff",
	PanelAlt:  "#eceef5",
	Hover:     "#dde1ec",
	Text:      "#1c2230",
	Dim:       "#5c6577",
	Faint:     "#8b93a5",
	Accent:    "#5b6cf0",
	AccentDim: "#4353d8",
	OnAccent:  "#ffffff",
	Ok:        "#0e9f6e",
	Danger:    "#dc4b3f",
	Warn:      "#d9820b",
	Info:      "#3574f0",
	Border:    "rgba(20,26,44,0.10)",
	Shadow:    "rgba(20,26,44,0.22)",
}

// currentPalette returns the palette for the active theme with opacity applied
// to the main surfaces.
func currentPalette() palette {
	base := darkPalette
	if isLight() {
		base = lightPalette
	}
	op := 0.85
	if store.State.Settings.Opacity > 0.05 {
		op = store.State.Settings.Opacity
	}
	hexRGB := func(h string) string {
		var r, g, b int
		_, _ = fmt.Sscanf(h, "#%02x%02x%02x", &r, &g, &b)
		return fmt.Sprintf("%d,%d,%d", r, g, b)
	}
	p := base
	p.Bg = fmt.Sprintf("rgba(%s,%.3f)", hexRGB(base.Bg), op)
	p.Side = fmt.Sprintf("rgba(%s,%.3f)", hexRGB(base.Side), op)
	p.Panel = fmt.Sprintf("rgba(%s,%.3f)", hexRGB(base.Panel), op)
	p.PanelAlt = fmt.Sprintf("rgba(%s,%.3f)", hexRGB(base.PanelAlt), op)
	return p
}

// isLight reports whether the light theme is active.
func isLight() bool { return store.State.Settings.Theme == "light" }

// a returns an rgba() string of a hex color with the given alpha.
func a(hex string, alpha float64) string {
	var r, g, b int
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("rgba(%d,%d,%d,%.3f)", r, g, b, alpha)
}

func themeCSS() string {
	p := darkPalette
	if isLight() {
		p = lightPalette
	}
	op := 0.85
	if store.State.Settings.Opacity > 0.05 {
		op = store.State.Settings.Opacity
	}
	opCSS := func(h string) string { // hex color with window opacity applied
		return a(h, op)
	}
	bg := opCSS(p.Bg)
	side := opCSS(p.Side)
	panel := opCSS(p.Panel)
	panelAlt := opCSS(p.PanelAlt)

	var b strings.Builder
	w := func(f string, args ...interface{}) { fmt.Fprintf(&b, f+"\n", args...) }

	w(`
* { -GtkTextView-error-underline-color: %s; outline-width: 0; -GtkToolButton-icon-spacing: 4; }
window { background-color: %s; color: %s; border-radius: 14px; }
dialog, messagedialog { background-color: %s; color: %s; border-radius: 13px; }
dialog .titlebar, messagedialog .titlebar { background-color: %s; color: %s; border: none; }

decoration { border-radius: 14px; box-shadow: 0 16px 52px 6px %s; background-color: transparent; }
`, p.Danger, bg, p.Text, bg, p.Text, side, p.Text, p.Shadow)

	// ---- header ----
	w(`
.radrag { background-color: transparent; }
.raheader { background-color: %s; border-radius: 14px 14px 0 0; padding: 8px 12px 7px 12px; }
.raheader label.title { font-size: 14px; font-weight: 700; color: %s; }
.raheader label.subtitle { color: %s; font-size: 10px; font-weight: 600; padding-top: 2px; }
.raheader button { background-color: transparent; background-image: none; border-color: transparent; padding: 4px 8px; }
.raheader button:hover { background-color: %s; }
.logochip { background-image: linear-gradient(to bottom right, %s, %s); color: #ffffff; border-radius: 7px; font-weight: 800; font-size: 11px; padding: 3px 7px; text-shadow: none; }
.verchip { background-color: %s; color: %s; border-radius: 6px; padding: 1px 7px; font-size: 10px; font-weight: 700; border: 1px solid %s; }
`, side, p.Text, p.Dim, p.Hover, p.Accent, p.AccentDim, panelAlt, p.Dim, p.Border)

	// ---- traffic lights ----
	w(`
.raheader button.tlbtn { min-width: 14px; min-height: 14px; padding: 0; border-radius: 12px; border: none; background-image: none; box-shadow: none; }
.raheader button.tl-red { background-color: #ff5f57; }
.raheader button.tl-red:hover { background-color: #ff837c; }
.raheader button.tl-yel { background-color: #febc2e; }
.raheader button.tl-yel:hover { background-color: #ffd471; }
.raheader button.tl-grn { background-color: #28c840; }
.raheader button.tl-grn:hover { background-color: #55dd6c; }
`)

	// ---- sidebar ----
	w(`
.raside { background-color: %s; padding: 2px 8px 8px 8px; }
.raside separator { background-color: %s; min-height: 1px; }
.navsec { color: %s; font-size: 9px; font-weight: 700; padding: 8px 10px 4px 10px; }
.navbtn { background-image: none; background-color: transparent; color: %s; border: none; border-radius: 11px; padding: 6px 9px; margin: 2px 0; box-shadow: none; transition: all 130ms ease; }
.navbtn:hover { background-image: none; background-color: %s; color: %s; }
.navbtn:active { background-color: %s; }
.navbtn label { font-size: 12px; font-weight: 600; color: %s; padding-left: 3px; }
.navbtn:hover label { color: %s; }
.navbtn-active { background-image: linear-gradient(to right, %s, transparent); background-color: %s; border-left: none; }
.navbtn-active:hover { background-color: %s; }
.navbtn-active label { font-weight: 700; color: %s; }
.navchip { border-radius: 8px; padding: 0; margin: 0; }
.navchip image { padding: 0; }
`, side, p.Border, p.Faint, p.Dim, a(p.Accent, 0.10), p.Text, a(p.Accent, 0.18), p.Dim, p.Text,
		a(p.Accent, 0.14), a(p.Accent, 0.17), a(p.Accent, 0.20), p.Text)

	// цветные плитки-чипы значков: насыщенный градиент раздела + белый
	// глиф внутри — иконки читаются мгновенно (стиль macOS Settings)
	// NB: без box-shadow — GTK3 CSS его не знает и отбрасывает всё правило
	for i, it := range navItems {
		c1, c2 := navColorOf(it.id)
		w(".navchip.c%03d { background-image: linear-gradient(to bottom, %s, %s); background-color: %s; border: 1px solid %s; }",
			i, a(c1, 0.80), a(c2, 0.80), a(c1, 0.80), a(c2, 0.35))
		w(".navbtn:hover .navchip.c%03d { background-image: linear-gradient(to bottom, %s, %s); }",
			i, a(c1, 0.92), a(c2, 0.92))
		w(".navbtn-active .navchip.c%03d { background-image: linear-gradient(to bottom, %s, %s); }",
			i, a(c1, 1.0), a(c2, 1.0))
	}

	// ---- status bar ----
	w(`
.rastatus { background-color: %s; border-radius: 0 0 14px 14px; padding: 3px 14px; }
.rastatus label { color: %s; font-size: 11px; }
.stdot { font-size: 11px; }
.stdot-ok { color: %s; } .stdot-fail { color: %s; } .stdot-run { color: %s; } .stdot-warn { color: %s; } .stdot-idle { color: %s; }
`, side, p.Dim, p.Ok, p.Danger, p.Info, p.Warn, p.Faint)

	// ---- cards ----
	w(`
.rabody { background-color: transparent; padding: 10px; }
.racard { background-color: %s; border-radius: 12px; border: 1px solid %s; padding: 12px; }
.ratitle { color: %s; font-weight: 700; font-size: 12px; }
.radim { color: %s; }
.ramuted { color: %s; font-size: 11px; }
frame > border { border: none; }
frame > label { color: %s; font-weight: 700; font-size: 11px; padding: 0 4px; }
`, panel, p.Border, p.Text, p.Dim, p.Dim, p.Dim)

	// ---- terminal cells (мульти-терминал) ----
	// фон ячейки темнее панели — как в настоящем терминале, вывод читается
	termBg, termTx := "#0e1219", "#d9e1f0"
	if isLight() {
		termBg, termTx = "#ffffff", "#232a36"
	}
	w(`
.termcell { background-color: %s; border-radius: 10px; border: 1px solid %s; padding: 6px 8px; }
.termhead { font-size: 11px; font-weight: 700; padding: 0 2px 3px 2px; }
.termcell textview, .termcell textview text { background-color: %s; color: %s; border-radius: 8px; }
.termcell checkbutton label { font-size: 10px; color: %s; }
.termclose { padding: 0 6px; margin-left: 4px; font-size: 11px; }
.termclose:hover { background-color: %s; }
.broadcast { background-color: %s; border: 1px solid %s; border-radius: 11px; padding: 6px 8px; }
.broadcast entry { font-size: 13px; font-weight: 600; border-color: %s; }
.broadcast label { color: %s; font-weight: 700; font-size: 12px; }
`, panel, p.Border, termBg, termTx, p.Dim, a(p.Danger, 0.25),
		a(p.Accent, 0.10), a(p.Accent, 0.45), a(p.Accent, 0.55), p.Text)

	// ---- мониторинг ----
	w(`
.monbar trough { min-height: 18px; border-radius: 6px; }
.monbar progress { min-height: 18px; border-radius: 6px; }
.monbar { font-size: 11px; font-weight: 600; }
.monbar text { font-size: 10px; }
.monhead { font-size: 11px; }
.monchart { background-color: transparent; }
treeview.monrow { font-size: 12px; }
.fkeybtn { padding: 3px 8px; font-size: 11px; border-radius: 8px; }
`)

	// ---- buttons ----
	w(`
button { background-image: none; background-color: %s; color: %s; border-radius: 10px; padding: 6px 14px; border: 1px solid %s; text-shadow: none; box-shadow: none; min-height: 18px; font-weight: 600; transition: all 120ms ease; }
button:hover { background-image: none; background-color: %s; border-color: %s; }
button:active { background-image: none; background-color: %s; }
button:disabled { opacity: 0.40; }
button.suggested { background-image: linear-gradient(to bottom, %s, %s); background-color: %s; color: %s; border-color: %s; font-weight: 700; box-shadow: 0 2px 8px %s; }
button.suggested:hover { background-image: linear-gradient(to bottom, %s, %s); }
button.suggested:active { background-image: none; background-color: %s; }
button.danger { background-image: none; background-color: %s; color: %s; border-color: %s; font-weight: 600; }
button.danger:hover { background-color: %s; }
button.flat { background-image: none; background-color: transparent; border-color: transparent; }
button.flat:hover { background-image: none; background-color: %s; }
`, panelAlt, p.Text, p.Border, p.Hover, a(p.Accent, 0.45), a(p.Accent, 0.20),
		p.Accent, p.AccentDim, p.Accent, p.OnAccent, p.AccentDim, a(p.Accent, 0.35),
		a(p.Accent, 0.85), p.AccentDim, p.AccentDim,
		a(p.Danger, 0.12), p.Danger, a(p.Danger, 0.35), a(p.Danger, 0.20),
		p.Hover)

	// ---- inputs ----
	w(`
entry { background-image: none; background-color: %s; color: %s; border-radius: 9px; padding: 5px 10px; border: 1px solid %s; caret-color: %s; box-shadow: none; }
entry:focus { border-color: %s; }
entry image { color: %s; min-width: 16px; }
textview text { background-image: none; background-color: %s; color: %s; caret-color: %s; border-radius: 9px; }
textview { background-color: transparent; border-radius: 9px; }
spinbutton { background-image: none; background-color: %s; color: %s; border-radius: 9px; border: 1px solid %s; padding: 2px; }
spinbutton entry { background-color: transparent; border: none; }
spinbutton button { padding: 1px 6px; min-height: 12px; }
searchbar entry { background-color: %s; }
`, panelAlt, p.Text, p.Border, p.Accent, a(p.Accent, 0.65), p.Dim,
		panelAlt, p.Text, p.Accent,
		panelAlt, p.Text, p.Border, panelAlt)

	// ---- combobox / menu / popover ----
	w(`
combobox button { background-image: none; background-color: %s; color: %s; border-radius: 9px; border: 1px solid %s; padding: 4px 8px; }
combobox button:hover { background-color: %s; }
combobox arrow { color: %s; min-width: 14px; }
menu { background-color: %s; border-radius: 11px; border: 1px solid %s; padding: 4px; }
menuitem { color: %s; padding: 5px 12px; border-radius: 7px; font-size: 12px; }
menuitem:hover { background-color: %s; }
popover { background-color: %s; border-radius: 11px; border: 1px solid %s; }
popover list { background-color: transparent; }
`, panelAlt, p.Text, p.Border, p.Hover, p.Dim,
		panel, a(p.Text, 0.14), p.Text, a(p.Accent, 0.20), panel, a(p.Text, 0.14))

	// ---- treeview ----
	w(`
treeview { background-image: none; background-color: transparent; color: %s; }
treeview:selected { background-color: %s; color: %s; }
treeview header button { background-image: none; background-color: transparent; color: %s; border: none; border-radius: 0; font-size: 10px; font-weight: 700; padding: 2px 4px; }
.rpdetail, .rpdetail text { background-color: %s; }
`, p.Text, a(p.Accent, 0.24), p.Text, p.Faint, panel)

	// ---- progress / bars ----
	w(`
progressbar trough { background-image: none; background-color: %s; border-radius: 5px; min-height: 6px; border: none; }
progressbar progress { background-image: linear-gradient(to right, %s, %s); border-radius: 5px; min-height: 6px; min-width: 6px; }
progressbar { color: %s; }
progressbar text { color: %s; font-size: 10px; }
.pb-ok progress { background-image: none; background-color: %s; }
.pb-warn progress { background-image: none; background-color: %s; }
.pb-danger progress { background-image: none; background-color: %s; }
`, panelAlt, p.Accent, p.AccentDim, p.Dim, p.Dim, p.Ok, p.Warn, p.Danger)

	// ---- switches / scales / misc ----
	w(`
checkbutton { color: %s; padding: 2px 0; }
checkbutton label { font-size: 12px; }
switch { background-image: none; background-color: %s; border-radius: 14px; border: 1px solid %s; }
switch slider { background-color: %s; border-radius: 13px; }
switch:checked { background-color: %s; border-color: transparent; }
switch:checked slider { background-color: #ffffff; }
scale trough { background-image: none; background-color: %s; border-radius: 4px; min-height: 5px; border: none; }
scale highlight { background-image: none; background-color: %s; border-radius: 4px; }
scale slider { background-color: %s; border-radius: 10px; min-height: 16px; min-width: 16px; border: 1px solid %s; }
separator { background-color: %s; min-height: 1px; min-width: 1px; }
scrollbar { background-color: transparent; border: none; }
scrollbar slider { background-color: %s; border-radius: 8px; min-width: 6px; min-height: 26px; border: none; }
scrollbar slider:hover { background-color: %s; }
tooltip { background-color: %s; color: %s; border-radius: 9px; border: 1px solid %s; }
tooltip label { font-size: 11px; }
levelbar block { border-radius: 3px; min-height: 10px; }
`, p.Text, a(p.Text, 0.14), p.Border, p.Text, p.Accent,
		panelAlt, p.Accent, p.Text, p.Border,
		a(p.Text, 0.08), a(p.Text, 0.22), a(p.Text, 0.36), p.Text, a(p.Text, 0.14), p.Border)

	// ---- notebook (dialogs may still use it) ----
	w(`
notebook > header { background-color: transparent; border: none; padding: 0 4px; }
notebook > header > tabs > tab { background-color: transparent; color: %s; padding: 5px 12px; margin: 2px; border-radius: 8px; font-weight: 600; }
notebook > header > tabs > tab:hover { background-color: %s; color: %s; }
notebook > header > tabs > tab:checked { background-color: %s; color: %s; }
notebook > stack { background-color: transparent; }
`, p.Dim, panelAlt, p.Text, a(p.Accent, 0.18), p.Text)

	return b.String()
}

// applyTheme (re)loads the CSS provider for the whole screen.
var cssProvider *gtk.CssProvider

func applyTheme(w *gtk.Window) {
	var screen *gdk.Screen
	if w != nil {
		screen = w.GetScreen()
	} else {
		screen, _ = gdk.ScreenGetDefault()
	}
	if screen == nil {
		return
	}
	provider, err := gtk.CssProviderNew()
	if err != nil {
		return
	}
	if err := provider.LoadFromData(themeCSS()); err != nil {
		fmt.Fprintln(os.Stderr, "CSS PARSE ERROR:", err)
		return
	}
	gtk.AddProviderForScreen(screen, provider, uint(gtk.STYLE_PROVIDER_PRIORITY_APPLICATION))
	cssProvider = provider
	if app != nil {
		app.refreshNavIcons()
	}
}
