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

// ---- v1.14.0: современные темы (проверенные фирменные схемы) ----

var nordPalette = palette{
        Bg:        "#2e3440",
        Side:      "#3b4252",
        Panel:     "#333a47",
        PanelAlt:  "#3b4252",
        Hover:     "#434c5e",
        Text:      "#eceff4",
        Dim:       "#d8dee9",
        Faint:     "#7b88a1",
        Accent:    "#88c0d0",
        AccentDim: "#5e81ac",
        OnAccent:  "#2e3440",
        Ok:        "#a3be8c",
        Danger:    "#bf616a",
        Warn:      "#ebcb8b",
        Info:      "#81a1c1",
        Border:    "rgba(236,239,244,0.09)",
        Shadow:    "rgba(0,0,0,0.55)",
}

var catppuccinPalette = palette{
        Bg:        "#1e1e2e",
        Side:      "#181825",
        Panel:     "#262637",
        PanelAlt:  "#313244",
        Hover:     "#45475a",
        Text:      "#cdd6f4",
        Dim:       "#a6adc8",
        Faint:     "#6c7086",
        Accent:    "#cba6f7",
        AccentDim: "#b4befe",
        OnAccent:  "#11111b",
        Ok:        "#a6e3a1",
        Danger:    "#f38ba8",
        Warn:      "#f9e2af",
        Info:      "#89b4fa",
        Border:    "rgba(205,214,244,0.09)",
        Shadow:    "rgba(0,0,0,0.60)",
}

var oneDarkPalette = palette{
        Bg:        "#282c34",
        Side:      "#21252b",
        Panel:     "#2c313a",
        PanelAlt:  "#333842",
        Hover:     "#3e4451",
        Text:      "#dcdfe4",
        Dim:       "#9da5b4",
        Faint:     "#5c6370",
        Accent:    "#61afef",
        AccentDim: "#528bff",
        OnAccent:  "#1b1f27",
        Ok:        "#98c379",
        Danger:    "#e06c75",
        Warn:      "#e5c07b",
        Info:      "#56b6c2",
        Border:    "rgba(220,223,228,0.09)",
        Shadow:    "rgba(0,0,0,0.55)",
}

var tokyoNightPalette = palette{
        Bg:        "#1a1b26",
        Side:      "#16161e",
        Panel:     "#1f2233",
        PanelAlt:  "#24283b",
        Hover:     "#2f334d",
        Text:      "#c0caf5",
        Dim:       "#a9b1d6",
        Faint:     "#565f89",
        Accent:    "#7aa2f7",
        AccentDim: "#6183bb",
        OnAccent:  "#10131e",
        Ok:        "#9ece6a",
        Danger:    "#f7768e",
        Warn:      "#e0af68",
        Info:      "#7dcfff",
        Border:    "rgba(192,202,245,0.09)",
        Shadow:    "rgba(0,0,0,0.62)",
}

var gruvboxPalette = palette{
        Bg:        "#282828",
        Side:      "#1d2021",
        Panel:     "#32302f",
        PanelAlt:  "#3c3836",
        Hover:     "#504945",
        Text:      "#ebdbb2",
        Dim:       "#bdae93",
        Faint:     "#928374",
        Accent:    "#8ec07c",
        AccentDim: "#83a598",
        OnAccent:  "#1d2021",
        Ok:        "#b8bb26",
        Danger:    "#fb4934",
        Warn:      "#fabd2f",
        Info:      "#83a598",
        Border:    "rgba(235,219,178,0.10)",
        Shadow:    "rgba(0,0,0,0.58)",
}

var draculaPalette = palette{
        Bg:        "#282a36",
        Side:      "#21222c",
        Panel:     "#2f3141",
        PanelAlt:  "#343746",
        Hover:     "#44475a",
        Text:      "#f8f8f2",
        Dim:       "#c3c6d4",
        Faint:     "#6272a4",
        Accent:    "#bd93f9",
        AccentDim: "#a78bfa",
        OnAccent:  "#191a21",
        Ok:        "#50fa7b",
        Danger:    "#ff5555",
        Warn:      "#f1fa8c",
        Info:      "#8be9fd",
        Border:    "rgba(248,248,242,0.09)",
        Shadow:    "rgba(0,0,0,0.60)",
}

var everforestPalette = palette{
        Bg:        "#2d353b",
        Side:      "#242d31",
        Panel:     "#343f44",
        PanelAlt:  "#3d484d",
        Hover:     "#475258",
        Text:      "#d3c6aa",
        Dim:       "#9da9a0",
        Faint:     "#7a8478",
        Accent:    "#83c092",
        AccentDim: "#7fbbb3",
        OnAccent:  "#232a2e",
        Ok:        "#a7c080",
        Danger:    "#e67e80",
        Warn:      "#dbbc7f",
        Info:      "#7fbbb3",
        Border:    "rgba(211,198,170,0.10)",
        Shadow:    "rgba(0,0,0,0.55)",
}

var solarizedLightPalette = palette{
        Bg:        "#fdf6e3",
        Side:      "#eee8d5",
        Panel:     "#fffbef",
        PanelAlt:  "#f2ecdb",
        Hover:     "#e4dcc3",
        Text:      "#073642",
        Dim:       "#586e75",
        Faint:     "#93a1a1",
        Accent:    "#268bd2",
        AccentDim: "#1f6fab",
        OnAccent:  "#ffffff",
        Ok:        "#859900",
        Danger:    "#dc322f",
        Warn:      "#b58900",
        Info:      "#2aa198",
        Border:    "rgba(7,54,66,0.12)",
        Shadow:    "rgba(7,54,66,0.20)",
}

// currentPalette returns the palette for the active theme with opacity applied
// to the main surfaces.
func currentPalette() palette {
        base, _ := paletteOf(store.State.Settings.Theme)
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

// ---------------------------------------------------------------------------
// v1.14.0: реестр тем. Раньше были только dark/light; жалоба — «добавить
// еще несколько тем современных актуальных». Добавлены популярные схемы
// редакторов/десктопов (Nord, Catppuccin, One Dark, Tokyo Night, Gruvbox,
// Dracula, Everforest, Solarized Light). Все поля палитры обязательны.
// ---------------------------------------------------------------------------

type themeDef struct {
        id    string
        title string
        p     palette
        light bool
}

var themeList = []themeDef{
        {"aurora-dark", "Aurora Тёмная (по умолчанию)", darkPalette, false},
        {"aurora-light", "Aurora Светлая", lightPalette, true},
        {"nord", "Nord", nordPalette, false},
        {"catppuccin", "Catppuccin Mocha", catppuccinPalette, false},
        {"onedark", "One Dark Pro", oneDarkPalette, false},
        {"tokyonight", "Tokyo Night", tokyoNightPalette, false},
        {"gruvbox", "Gruvbox Dark", gruvboxPalette, false},
        {"dracula", "Dracula", draculaPalette, false},
        {"everforest", "Everforest Dark", everforestPalette, false},
        {"solarized", "Solarized Light", solarizedLightPalette, true},
}

// paletteOf возвращает палитру и принадлежность к светлому семейству.
// Совместимость: старые значения state.json «dark»/«light»/пустое.
func paletteOf(id string) (palette, bool) {
        switch id {
        case "", "dark":
                id = "aurora-dark"
        case "light":
                id = "aurora-light"
        }
        for _, t := range themeList {
                if t.id == id {
                        return t.p, t.light
                }
        }
        return darkPalette, false
}

func activeThemeID() string {
        id := store.State.Settings.Theme
        switch id {
        case "", "dark":
                return "aurora-dark"
        case "light":
                return "aurora-light"
        }
        return id
}

// isLight reports whether a light-family theme is active.
func isLight() bool {
        _, light := paletteOf(store.State.Settings.Theme)
        return light
}

// a returns an rgba() string of a hex color with the given alpha.
func a(hex string, alpha float64) string {
        var r, g, b int
        _, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
        return fmt.Sprintf("rgba(%d,%d,%d,%.3f)", r, g, b, alpha)
}

func themeCSS() string {
        p, _ := paletteOf(store.State.Settings.Theme)
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
        // v1.12.0: кнопки «Управления» слева перерисованы — крупные пилюли
        // с цветными чипами-иконками, мягкая подсветка при наведении и
        // акцентная «активная» строка с рамкой
        w(`
.raside { background-color: %s; padding: 2px 8px 8px 8px; }
.raside separator { background-color: %s; min-height: 1px; }
.navsec { color: %s; font-size: 9px; font-weight: 700; padding: 8px 10px 4px 10px; letter-spacing: 1.6px; }
.navbtn { background-image: none; background-color: transparent; color: %s; border: 1px solid transparent; border-radius: 12px; padding: 5px 8px; margin: 2px 3px; box-shadow: none; transition: all 140ms ease; }
.navbtn:hover { background-image: none; background-color: %s; color: %s; border-color: %s; }
.navbtn:active { background-color: %s; }
.navbtn label { font-size: 12px; font-weight: 600; color: %s; padding-left: 4px; }
.navbtn:hover label { color: %s; }
.navbtn-active { background-image: linear-gradient(to right, %s, %s); background-color: %s; border-color: %s; }
.navbtn-active:hover { background-color: %s; border-color: %s; }
.navbtn-active label { font-weight: 700; color: %s; }
.navchip { border-radius: 9px; padding: 0; margin: 0; min-width: 30px; min-height: 26px; }
.navchip image { padding: 0; }
`, side, p.Border, p.Faint, p.Dim,
                a(p.Text, 0.06), p.Text, a(p.Text, 0.10), a(p.Text, 0.10), p.Dim, p.Text,
                a(p.Accent, 0.16), a(p.Accent, 0.05), a(p.Accent, 0.10), a(p.Accent, 0.38),
                a(p.Accent, 0.20), a(p.Accent, 0.45), p.Text)

        // цветные плитки-чипы значков: насыщенный градиент раздела + белый
        // глиф внутри — иконки читаются мгновенно (стиль macOS Settings)
        // NB: без box-shadow — GTK3 CSS его не знает и отбрасывает всё правило
        for i, it := range navItems {
                c1, c2 := navColorOf(it.id)
                w(".navchip.c%03d { background-image: linear-gradient(135deg, %s, %s); background-color: %s; border: 1px solid %s; }",
                        i, a(c1, 0.92), a(c2, 0.92), a(c1, 0.92), a(c2, 0.40))
                w(".navbtn:hover .navchip.c%03d { background-image: linear-gradient(135deg, %s, %s); }",
                        i, a(c1, 1.0), a(c2, 1.0))
                w(".navbtn-active .navchip.c%03d { background-image: linear-gradient(135deg, %s, %s); border-color: %s; }",
                        i, a(c1, 1.0), a(c2, 1.0), a(c2, 0.60))
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
button { background-image: none; background-color: %s; color: %s; border-radius: 10px; padding: 6px 10px; border: 1px solid %s; text-shadow: none; box-shadow: none; min-height: 18px; font-weight: 600; transition: all 120ms ease; }
button:hover { background-image: none; background-color: %s; border-color: %s; }
button:active { background-image: none; background-color: %s; }
button:disabled { opacity: 0.40; }
button.suggested { background-image: linear-gradient(to bottom, %s, %s); background-color: %s; color: %s; border-color: %s; font-weight: 700; box-shadow: 0 2px 8px %s; }
button.suggested:hover { background-image: linear-gradient(to bottom, %s, %s); }
button.suggested:active { background-image: none; background-color: %s; }
button.danger { background-image: none; background-color: %s; color: %s; border-color: %s; font-weight: 600; }
button.danger:hover { background-color: %s; }
button.destructive { background-image: none; background-color: transparent; color: %s; border-color: %s; font-weight: 600; padding: 5px 10px; }
button.destructive:hover { background-image: none; background-color: %s; color: %s; border-color: %s; }
button.flat { background-image: none; background-color: transparent; border-color: transparent; }
button.flat:hover { background-image: none; background-color: %s; }
`, panelAlt, p.Text, p.Border, p.Hover, a(p.Accent, 0.45), a(p.Accent, 0.20),
                p.Accent, p.AccentDim, p.Accent, p.OnAccent, p.AccentDim, a(p.Accent, 0.35),
                a(p.Accent, 0.85), p.AccentDim, p.AccentDim,
                a(p.Danger, 0.12), p.Danger, a(p.Danger, 0.35), a(p.Danger, 0.20),
                p.Danger, a(p.Danger, 0.45),
                a(p.Danger, 0.12), p.Danger, a(p.Danger, 0.45),
                p.Hover)

        // ---- v1.13.0: единые компактные кнопки (Действия/Шаблоны/Цепочки) ----
        // жалоба v1.12.0: кнопки растягивались при увеличении окна и были
        // «все разные». Фиксированная ширина + центрирование блоков.
        w(`
.actbtn { padding: 5px 12px; }
.tbtn { min-width: 104px; padding: 5px 8px; }
.tbtn label { padding: 0; }
`)

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

        // ---- раздвижные панели / контур изменения размера / пустые состояния ----
        // v1.13.2 (жалоба: «выполнение/вывод/события не перемещается вниз»):
        // ручка разделителя была 3 px — её невозможно было схватить мышью.
        // Теперь 9 px, видна на фоне карточек и подсвечивается при наведении;
        // при перетаскивании — акцентный цвет.
        w(`
.raedge { background-color: transparent; min-height: 5px; min-width: 5px; }
.rapaned > separator { background-image: none; background-color: %s; min-width: 9px; min-height: 9px; border: none; border-radius: 3px; }
.rapaned > separator:hover { background-color: %s; }
.rapaned > separator:active { background-color: %s; }
.raempty { color: %s; font-size: 13px; padding: 14px 6px; }
`, a(p.Text, 0.16), p.Accent, p.Accent, p.Dim)

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
