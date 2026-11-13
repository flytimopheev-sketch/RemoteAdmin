package main

import (
        "fmt"
        "os"
        "path/filepath"

        "github.com/gotk3/gotk3/cairo"
        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

type application struct {
        window      *gtk.Window
        log         *LogArea
        execUI      *ExecPanel
        statusLabel *gtk.Label
        statusDot   *gtk.Label
        dotClass    string
        progress    *gtk.ProgressBar
        headerSub   *gtk.Label
        imgTheme    *gtk.Image
        pages       *gtk.Stack
        navBtns     map[string]*gtk.Button
        navIcons    map[string]*gtk.Image
        active      string
        maximized   bool
        hostsUI     *HostsUI
        pbUI        *PlaybookUI
        tplUI       *TemplatesUI
        chainUI     *ChainsUI
        schedUI     *SchedUI
        monUI       *MonitorUI
        sftpUI      *SFTPUI
        mtUI        *MultiTermUI
        repUI       *ReportsUI
        auditUI     *AuditUI
}

var app *application

// ---------- helpers ----------

func boxNew(orient gtk.Orientation, spacing int) *gtk.Box {
        b, _ := gtk.BoxNew(orient, spacing)
        return b
}

func labelNew(text string) *gtk.Label {
        l, _ := gtk.LabelNew(text)
        return l
}

func buttonNew(label string) *gtk.Button {
        b, _ := gtk.ButtonNewWithLabel(label)
        return b
}

func addClass(w gtk.IWidget, cls string) {
        if sc, err := w.ToWidget().GetStyleContext(); err == nil {
                sc.AddClass(cls)
        }
}

func removeClass(w gtk.IWidget, cls string) {
        if sc, err := w.ToWidget().GetStyleContext(); err == nil {
                sc.RemoveClass(cls)
        }
}

func cardBox(title string) (*gtk.Frame, *gtk.Box) {
        f, _ := gtk.FrameNew("")
        f.SetLabel("")
        addClass(f, "racard")
        b := boxNew(gtk.ORIENTATION_VERTICAL, 6)
        if title != "" {
                t := labelNew(title)
                addClass(t, "ratitle")
                t.SetXAlign(0)
                b.PackStart(t, false, false, 0)
        }
        f.Add(b)
        return f, b
}

// drawRoundedRect paints a filled rounded rectangle.
func drawRoundedRect(ctx *cairo.Context, x, y, w, h, r float64, color string) {
        ctx.SetSourceRGBA(hexToRGBA(color))
        ctx.MoveTo(x+r, y)
        ctx.Arc(x+w-r, y+r, r, -0.5*pi, 0)
        ctx.Arc(x+w-r, y+h-r, r, 0, 0.5*pi)
        ctx.Arc(x+r, y+h-r, r, 0.5*pi, pi)
        ctx.Arc(x+r, y+r, r, pi, 1.5*pi)
        ctx.ClosePath()
        ctx.Fill()
}

const pi = 3.141592653589793

func hexToRGBA(hex string) (float64, float64, float64, float64) {
        var r, g, b int
        a := 1.0
        if len(hex) > 7 && hex[:4] == "rgba" {
                var rr, gg, bb int
                var aa float64
                if _, err := fmt.Sscanf(hex, "rgba(%d,%d,%d,%f)", &rr, &gg, &bb, &aa); err == nil {
                        return float64(rr) / 255.0, float64(gg) / 255.0, float64(bb) / 255.0, aa
                }
        }
        n, _ := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
        if n != 3 {
                return 0.14, 0.15, 0.18, a
        }
        return float64(r) / 255.0, float64(g) / 255.0, float64(b) / 255.0, a
}

// ---------- frameless window behavior ----------

type edgeZone int

const (
        edgeNone edgeZone = iota
        edgeTop
        edgeBottom
        edgeLeft
        edgeRight
        edgeTL
        edgeTR
        edgeBL
        edgeBR
)

func detectEdge(w *gtk.Window, x, y int) edgeZone {
        width, height := w.GetAllocatedWidth(), w.GetAllocatedHeight()
        const m = 8
        var top, bottom, left, right bool
        if y <= m {
                top = true
        }
        if y >= height-m {
                bottom = true
        }
        if x <= m {
                left = true
        }
        if x >= width-m {
                right = true
        }
        switch {
        case top && left:
                return edgeTL
        case top && right:
                return edgeTR
        case bottom && left:
                return edgeBL
        case bottom && right:
                return edgeBR
        case top:
                return edgeTop
        case bottom:
                return edgeBottom
        case left:
                return edgeLeft
        case right:
                return edgeRight
        }
        return edgeNone
}

func gdkEdge(z edgeZone) gdk.WindowEdge {
        switch z {
        case edgeTop:
                return gdk.WINDOW_EDGE_NORTH
        case edgeBottom:
                return gdk.WINDOW_EDGE_SOUTH
        case edgeLeft:
                return gdk.WINDOW_EDGE_WEST
        case edgeRight:
                return gdk.WINDOW_EDGE_EAST
        case edgeTL:
                return gdk.WINDOW_EDGE_NORTH_WEST
        case edgeTR:
                return gdk.WINDOW_EDGE_NORTH_EAST
        case edgeBL:
                return gdk.WINDOW_EDGE_SOUTH_WEST
        case edgeBR:
                return gdk.WINDOW_EDGE_SOUTH_EAST
        }
        return gdk.WINDOW_EDGE_NORTH
}

func setupFramelessBehavior(w *gtk.Window) {
        w.Connect("button-press-event", func(_ *gtk.Window, ev *gdk.Event) bool {
                be := gdk.EventButtonNewFromEvent(ev)
                if be.Button() != gdk.BUTTON_PRIMARY {
                        return false
                }
                x, y := int(be.X()), int(be.Y())
                z := detectEdge(w, x, y)
                if z != edgeNone {
                        w.BeginResizeDrag(gdkEdge(z), gdk.BUTTON_PRIMARY, int(be.XRoot()), int(be.YRoot()), be.Time())
                        return true
                }
                return false
        })
}

// windowDragBox wraps a windowless container into an EventBox and turns it
// into a move-drag zone. Plain GtkBox/GtkLabel never receive button events
// in GTK3 (they have no GdkWindow), so the whole header previously ignored
// press-and-drag. Interactive children listed in skip are excluded by
// geometry so buttons remain clickable.
//
// NB: before starting a MOVE we check whether the press is within 8 px of
// a window EDGE (translated to window coordinates) — the header (top edge)
// and the status bar (bottom edge) cover the whole window border, and
// without this the window could not be resized vertically at all.
func windowDragBox(content gtk.IWidget, skip []gtk.IWidget) *gtk.EventBox {
        eb, err := gtk.EventBoxNew()
        if err != nil {
                return nil
        }
        eb.Add(content)
        eb.Connect("button-press-event", func(_ *gtk.EventBox, ev *gdk.Event) bool {
                be := gdk.EventButtonNewFromEvent(ev)
                if be.Button() != gdk.BUTTON_PRIMARY {
                        return false
                }
                px, py := int(be.X()), int(be.Y())
                for _, s := range skip {
                        if s == nil {
                                continue
                        }
                        al := s.ToWidget().GetAllocation()
                        ax, ay, aw, ah := al.GetRectangleInt()
                        if px >= ax && px <= ax+aw && py >= ay && py <= ay+ah {
                                return false // click belongs to an interactive child
                        }
                }
                // edge detection: translate the press point to window coordinates
                if app != nil && app.window != nil {
                        if wx, wy, terr := eb.ToWidget().TranslateCoordinates(app.window, px, py); terr == nil {
                                z := detectEdge(app.window, wx, wy)
                                if z != edgeNone {
                                        app.window.BeginResizeDrag(gdkEdge(z), gdk.BUTTON_PRIMARY, int(be.XRoot()), int(be.YRoot()), be.Time())
                                        return true
                                }
                        }
                }
                if be.Type() == gdk.EVENT_2BUTTON_PRESS {
                        app.toggleMax()
                        return true
                }
                app.window.BeginMoveDrag(gdk.BUTTON_PRIMARY, int(be.XRoot()), int(be.YRoot()), be.Time())
                return true
        })
        return eb
}

// ---------- контур изменения размера (edge strips) ----------
//
// Окно без рамки (SetDecorated(false)): системного контура, за который
// можно тянуть мышью, не существует. Раньше тянуть за края удавалось
// только в зонах шапки и статус-бара — боковые края перекрывались
// содержимым вкладок, и окно нельзя было ни сжать, ни растянуть.
// Теперь по периметру лежат тонкие (5 px) невидимые EventBox-полосы:
// они перехватывают нажатия даже над деревьями и списками, запускают
// стандартный BeginResizeDrag и показывают курсор-стрелку при наведении.

const edgePx = 5 // толщина полосы, за которую тянем

func setResizeCursor(w *gtk.EventBox, name string) {
        gw, err := w.GetWindow()
        if err != nil || gw == nil {
                return // окно ещё не реализовано — курсор не критичен
        }
        disp, derr := gdk.DisplayGetDefault()
        if derr != nil {
                return
        }
        if cur, cerr := gdk.CursorNewFromName(disp, name); cerr == nil && cur != nil {
                gw.SetCursor(cur)
        }
}

// edgeStrip создаёт невидимую полосу: side — top|bottom|left|right.
// Угловые зоны (20 px) дают диагональное растягивание.
func edgeStrip(side string) *gtk.EventBox {
        eb, err := gtk.EventBoxNew()
        if err != nil {
                return nil
        }
        addClass(eb, "raedge")
        eb.AddEvents(int(gdk.POINTER_MOTION_MASK))
        switch side {
        case "top", "bottom":
                eb.SetSizeRequest(-1, edgePx)
        default:
                eb.SetSizeRequest(edgePx, -1)
        }
        const corner = 20
        zoneOf := func(x, y int) gdk.WindowEdge {
                w, h := eb.GetAllocatedWidth(), eb.GetAllocatedHeight()
                switch side {
                case "top":
                        if x <= corner {
                                return gdk.WINDOW_EDGE_NORTH_WEST
                        }
                        if x >= w-corner {
                                return gdk.WINDOW_EDGE_NORTH_EAST
                        }
                        return gdk.WINDOW_EDGE_NORTH
                case "bottom":
                        if x <= corner {
                                return gdk.WINDOW_EDGE_SOUTH_WEST
                        }
                        if x >= w-corner {
                                return gdk.WINDOW_EDGE_SOUTH_EAST
                        }
                        return gdk.WINDOW_EDGE_SOUTH
                case "left":
                        if y <= corner {
                                return gdk.WINDOW_EDGE_NORTH_WEST
                        }
                        if y >= h-corner {
                                return gdk.WINDOW_EDGE_SOUTH_WEST
                        }
                        return gdk.WINDOW_EDGE_WEST
                default: // right
                        if y <= corner {
                                return gdk.WINDOW_EDGE_NORTH_EAST
                        }
                        if y >= h-corner {
                                return gdk.WINDOW_EDGE_SOUTH_EAST
                        }
                        return gdk.WINDOW_EDGE_EAST
                }
        }
        cursorOf := func(e gdk.WindowEdge) string {
                switch e {
                case gdk.WINDOW_EDGE_NORTH:
                        return "n-resize"
                case gdk.WINDOW_EDGE_SOUTH:
                        return "s-resize"
                case gdk.WINDOW_EDGE_WEST:
                        return "w-resize"
                case gdk.WINDOW_EDGE_EAST:
                        return "e-resize"
                case gdk.WINDOW_EDGE_NORTH_WEST:
                        return "nw-resize"
                case gdk.WINDOW_EDGE_NORTH_EAST:
                        return "ne-resize"
                case gdk.WINDOW_EDGE_SOUTH_WEST:
                        return "sw-resize"
                default:
                        return "se-resize"
                }
        }
        eb.Connect("button-press-event", func(_ *gtk.EventBox, ev *gdk.Event) bool {
                be := gdk.EventButtonNewFromEvent(ev)
                if be.Button() != gdk.BUTTON_PRIMARY {
                        return false
                }
                e := zoneOf(int(be.X()), int(be.Y()))
                if app != nil && app.window != nil {
                        app.window.BeginResizeDrag(e, gdk.BUTTON_PRIMARY, int(be.XRoot()), int(be.YRoot()), be.Time())
                }
                return true
        })
        eb.Connect("motion-notify-event", func(_ *gtk.EventBox, ev *gdk.Event) bool {
                me := gdk.EventMotionNewFromEvent(ev)
                mx, my := me.MotionVal()
                setResizeCursor(eb, cursorOf(zoneOf(int(mx), int(my))))
                return false
        })
        return eb
}

// ---------- раздвижные панели (GtkPaned) ----------
//
// Границы «группы | хосты | действия», панелей плейбука и SFTP можно
// таскать мышью влево-вправо; положения запоминаются между сеансами.

var panedRegistry []panedRef

type panedRef struct {
        name string
        p    *gtk.Paned
        def  int
}

// makeHPaned — горизонтальный разделитель с сохранением положения.
func makeHPaned(name string, left, right gtk.IWidget, defPos int) *gtk.Paned {
        p, err := gtk.PanedNew(gtk.ORIENTATION_HORIZONTAL)
        if err != nil {
                return nil
        }
        addClass(p, "rapaned")
        p.Pack1(left, true, false)
        p.Pack2(right, true, false)
        panedRegistry = append(panedRegistry, panedRef{name: name, p: p, def: defPos})
        return p
}

// restorePanedPositions применяется после ShowAll: до первого выделения
// GTK ограничивает позицию выделением детей.
func restorePanedPositions() {
        for _, r := range panedRegistry {
                pos := store.State.Session.panedPos(r.name)
                if pos <= 0 {
                        pos = r.def
                }
                if pos > 0 {
                        r.p.SetPosition(pos)
                }
        }
}

// savePanedPositions запоминает текущие положения разделителей в сессию.
func savePanedPositions() {
        pm := map[string]int{}
        for _, r := range panedRegistry {
                pm[r.name] = r.p.GetPosition()
        }
        store.State.Session.Paned = pm
}

func (a *application) toggleMax() {
        a.maximized = !a.maximized
        if a.maximized {
                a.window.Maximize()
        } else {
                a.window.Unmaximize()
        }
}

// ---------- header bar ----------

func buildHeader() gtk.IWidget {
        h := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        addClass(h, "raheader")

        // traffic lights
        var interactive []gtk.IWidget
        mkTL := func(cls, tip string, fn func()) *gtk.Button {
                b, _ := gtk.ButtonNew()
                addClass(b, "tlbtn")
                addClass(b, cls)
                b.SetSizeRequest(14, 14)
                b.SetTooltipText(tip)
                b.Connect("clicked", fn)
                interactive = append(interactive, b)
                return b
        }
        h.PackStart(mkTL("tl-red", "Закрыть", func() { appQuit() }), false, false, 0)
        h.PackStart(mkTL("tl-yel", "Свернуть", func() { app.window.Iconify() }), false, false, 0)
        h.PackStart(mkTL("tl-grn", "Развернуть", func() { app.toggleMax() }), false, false, 0)

        logo := labelNew("RA")
        addClass(logo, "logochip")
        title := labelNew(AppName)
        addClass(title, "title")
        ver := labelNew("v" + AppVersion)
        addClass(ver, "verchip")
        app.headerSub = labelNew("")
        addClass(app.headerSub, "subtitle")

        left := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        left.PackStart(logo, false, false, 0)
        left.PackStart(title, false, false, 0)
        left.PackStart(ver, false, false, 0)
        left.PackStart(app.headerSub, false, false, 0)
        h.PackStart(left, false, false, 2)

        // right: theme switch (настройки — в боковой панели, «шестерёнку» убрали
        // по просьбе пользователя: она занимала место и дублировала сайдбар)
        p := currentPalette()
        app.imgTheme, _ = gtk.ImageNew()
        setIcon(app.imgTheme, "moon", p.Dim, 18)
        btnTheme, _ := gtk.ButtonNew()
        addClass(btnTheme, "flat")
        btnTheme.SetImage(app.imgTheme)
        btnTheme.SetTooltipText("Переключить тему (тёмная/светлая)")
        btnTheme.Connect("clicked", func() {
                if store.State.Settings.Theme == "dark" {
                        store.State.Settings.Theme = "light"
                } else {
                        store.State.Settings.Theme = "dark"
                }
                _ = store.Save()
                applyTheme(app.window)
                app.window.QueueDraw()
                logf("info", "🎨 Тема: %s", store.State.Settings.Theme)
        })

        interactive = append(interactive, btnTheme)

        right := boxNew(gtk.ORIENTATION_HORIZONTAL, 2)
        right.PackStart(btnTheme, false, false, 0)
        h.PackEnd(right, false, false, 0)

        // NB: the header GtkBox has no GdkWindow, so without an EventBox
        // press-and-drag never reaches us and the window cannot be moved.
        eb := windowDragBox(h, interactive)
        addClass(eb, "radrag")
        return eb
}

// ---------- sidebar navigation ----------

type navItem struct {
        id    string
        label string
        icon  string
}

var navItems = []navItem{
        {"hosts", "Хосты", "server"},
        {"playbook", "Плейбук", "play"},
        {"templates", "Шаблоны", "copy"},
        {"chains", "Цепочки", "chain"},
        {"sched", "Планировщик", "clock"},
        {"monitor", "Мониторинг", "pulse"},
        {"sftp", "SFTP", "folder"},
        {"multiterm", "Терминалы", "term"},
        {"reports", "Журнал", "doc"},
        {"audit", "Аудит", "shield"},
}

// navColors — фирменный градиент каждой вкладки (сверху → снизу):
// каждый раздел получает свой цвет, панель перестаёт быть монохромной.
var navColors = map[string][2]string{
        "hosts":     {"#3b82f6", "#22d3ee"}, // синий → циан
        "playbook":  {"#818cf8", "#c084fc"}, // индиго → фиолет
        "templates": {"#f472b6", "#fb923c"}, // роза → оранж
        "chains":    {"#f59e0b", "#fbbf24"}, // янтарь
        "sched":     {"#10b981", "#34d399"}, // изумруд
        "monitor":   {"#ef4444", "#fb7185"}, // красный → роза
        "sftp":      {"#14b8a6", "#2dd4bf"}, // бирюза
        "multiterm": {"#8b5cf6", "#e879f9"}, // фиалка → фуксия
        "reports":   {"#38bdf8", "#818cf8"}, // небо → индиго
        "audit":     {"#a3e635", "#22c55e"}, // лайм — безопасность/аудит
}

// navColorOf возвращает пару градиентных цветов вкладки.
func navColorOf(id string) (string, string) {
        if c, ok := navColors[id]; ok {
                return c[0], c[1]
        }
        return "#7c8cff", "#5f6ff0"
}

func navIconKind(id string) string {
        for _, it := range navItems {
                if it.id == id {
                        return it.icon
                }
        }
        return "server"
}

func buildSidebar() gtk.IWidget {
        sb := boxNew(gtk.ORIENTATION_VERTICAL, 0)
        addClass(sb, "raside")
        sb.SetSizeRequest(200, -1)

        p := currentPalette()
        app.navBtns = map[string]*gtk.Button{}
        app.navIcons = map[string]*gtk.Image{}

        sec := labelNew("УПРАВЛЕНИЕ")
        addClass(sec, "navsec")
        sec.SetXAlign(0)
        sb.PackStart(sec, false, false, 0)

        for i, it := range navItems {
                it := it
                btn, _ := gtk.ButtonNew()
                row := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
                // цветная «плитка» вокруг значка — современный вид боковой панели
                chip := boxNew(gtk.ORIENTATION_HORIZONTAL, 0)
                // NB: GTK-класс не может содержать пробел — добавляем по одному
                addClass(chip, "navchip")
                addClass(chip, fmt.Sprintf("c%03d", i))
                chip.SetSizeRequest(27, 24)
                img, _ := gtk.ImageNew()
                chip.PackStart(img, true, true, 0)
                lbl := labelNew(it.label)
                row.PackStart(chip, false, false, 0)
                row.PackStart(lbl, false, false, 0)
                btn.Add(row)
                addClass(btn, "navbtn")
                btn.SetTooltipText(it.label)
                btn.Connect("clicked", func() { app.activateNav(it.id) })
                app.navBtns[it.id] = btn
                app.navIcons[it.id] = img
                sb.PackStart(btn, false, false, 0)
        }

        // stretch filler
        fill, _ := gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0)
        sb.PackStart(fill, true, true, 0)

        sep, _ := gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL)
        sb.PackStart(sep, false, false, 4)

        sec2 := labelNew("СИСТЕМА")
        addClass(sec2, "navsec")
        sec2.SetXAlign(0)
        sb.PackStart(sec2, false, false, 0)

        imgS, _ := gtk.ImageNew()
        setIcon(imgS, "sliders", p.Dim, 18)
        btnS, _ := gtk.ButtonNew()
        rowS := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        rowS.PackStart(imgS, false, false, 0)
        rowS.PackStart(labelNew("Настройки"), false, false, 0)
        btnS.Add(rowS)
        addClass(btnS, "navbtn")
        btnS.Connect("clicked", func() { showSettingsDialog() })
        sb.PackStart(btnS, false, false, 0)

        imgA, _ := gtk.ImageNew()
        setIcon(imgA, "info", p.Dim, 18)
        btnA, _ := gtk.ButtonNew()
        rowA := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        rowA.PackStart(imgA, false, false, 0)
        rowA.PackStart(labelNew("О программе"), false, false, 0)
        btnA.Add(rowA)
        addClass(btnA, "navbtn")
        btnA.SetTooltipText("О программе RemoteAdmin")
        btnA.Connect("clicked", func() { showAboutDialog() })
        sb.PackStart(btnA, false, false, 0)

        vlbl := labelNew("v" + AppVersion + " · РЕД ОС")
        addClass(vlbl, "navsec")
        vlbl.SetXAlign(0)
        vlbl.SetMarginTop(6)
        sb.PackStart(vlbl, false, false, 0)

        return sb
}

func (a *application) activateNav(id string) {
        if a.pages == nil {
                return
        }
        a.active = id
        a.pages.SetVisibleChildName(id)
        for _, it := range navItems {
                btn := a.navBtns[it.id]
                if btn == nil {
                        continue
                }
                if it.id == id {
                        addClass(btn, "navbtn-active")
                } else {
                        removeClass(btn, "navbtn-active")
                }
        }
        a.refreshNavIcons()
        if a.headerSub != nil {
                for _, it := range navItems {
                        if it.id == id {
                                a.headerSub.SetText("— " + it.label)
                        }
                }
        }
        if id == "sftp" && a.sftpUI != nil {
                a.sftpUI.refreshHosts()
        }
        // keep data-driven pages fresh on every visit
        switch id {
        case "reports":
                if a.repUI != nil {
                        a.repUI.refresh()
                }
        case "templates":
                if a.tplUI != nil {
                        a.tplUI.refresh()
                }
        case "chains":
                if a.chainUI != nil {
                        a.chainUI.refresh()
                }
        case "sched":
                if a.schedUI != nil {
                        a.schedUI.refresh()
                }
        case "hosts":
                if a.hostsUI != nil {
                        a.hostsUI.refreshAll()
                }
        case "playbook":
                if a.pbUI != nil {
                        a.pbUI.refreshTargetLine()
                }
        case "audit":
                if a.auditUI != nil {
                        a.auditUI.refresh()
                }
        }
}

// refreshNavIcons re-renders sidebar icons: глиф — БЕЛЫЙ сплошной силуэт,
// цвет несёт плитка-чип (CSS-градиент). Активная вкладка — полный цвет
// плитки и яркий глиф, остальные — приглушённая плитка.
func (a *application) refreshNavIcons() {
        if a == nil || a.navIcons == nil {
                return
        }
        glibIdle(func() {
                for id, img := range a.navIcons {
                        alpha := 0.82
                        if id == a.active {
                                alpha = 1.0
                        }
                        setIconGrad(img, navIconKind(id), "#ffffff", "#dfe7ff", alpha, 18)
                }
                if a.imgTheme != nil {
                        kind := "moon"
                        if isLight() {
                                kind = "sun"
                        }
                        p := darkPalette
                        if isLight() {
                                p = lightPalette
                        }
                        setIcon(a.imgTheme, kind, p.Dim, 18)
                }
        })
}

// ---------- status bar ----------

func buildStatusBar() gtk.IWidget {
        sb := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        addClass(sb, "rastatus")

        dot := labelNew("●")
        addClass(dot, "stdot")
        addClass(dot, "stdot-idle")
        app.statusDot = dot
        app.dotClass = "stdot-idle"

        lbl := labelNew("Готово")
        lbl.SetXAlign(0)
        app.statusLabel = lbl

        pb, _ := gtk.ProgressBarNew()
        pb.SetShowText(true)
        pb.SetText("")
        pb.SetHExpand(false)
        app.progress = pb
        pb.SetSizeRequest(240, 10)

        sb.PackStart(dot, false, false, 0)
        sb.PackStart(lbl, true, true, 4)
        sb.PackEnd(pb, false, false, 0)

        // the status bar is also a convenient grab zone for moving the window
        eb := windowDragBox(sb, nil)
        addClass(eb, "radrag")
        return eb
}

// setDot switches the status dot color class.
func (a *application) setDot(kind string) {
        glibIdle(func() {
                if a.statusDot == nil {
                        return
                }
                removeClass(a.statusDot, a.dotClass)
                cls := "stdot-" + kind
                addClass(a.statusDot, cls)
                a.dotClass = cls
        })
}

// setProgress updates the global progress display; pass (0,0) to clear.
func (a *application) setProgress(done, total int) {
        glibIdle(func() {
                if a.progress == nil {
                        return
                }
                if total <= 0 {
                        a.progress.SetFraction(0)
                        a.progress.SetText("")
                        a.setDot("idle")
                        return
                }
                if done > total {
                        done = total
                }
                a.progress.SetFraction(float64(done) / float64(total))
                a.progress.SetText(fmt.Sprintf("Выполнено %d/%d", done, total))
                a.setDot("run")
        })
}

func (a *application) setStatus(text string) {
        glibIdle(func() {
                if a.statusLabel != nil {
                        a.statusLabel.SetText(text)
                }
        })
}

// ---------- нижняя панель: Выполнение / Вывод / События (v1.9.0) ----------

// buildExecBar создаёт нижнюю панель приложения. Реализация — execpanel.go.
func buildExecBar() gtk.IWidget {
        w := buildExecPanel()
        app.execUI = execPanel
        return w
}

// ---------- main assembly ----------

func buildApp(selftest bool) error {
        win, err := gtk.WindowNew(gtk.WINDOW_TOPLEVEL)
        if err != nil {
                return err
        }
        app = &application{window: win, navBtns: map[string]*gtk.Button{}, navIcons: map[string]*gtk.Image{}}
        win.SetTitle(AppName + " " + AppVersion)
        win.SetDefaultSize(1180, 700)
        win.SetSizeRequest(960, 560) // можно уменьшать, но не до бесформенности
        win.SetDecorated(false)
        win.SetAppPaintable(true)
        win.SetPosition(gtk.WIN_POS_CENTER)

        screen := win.GetScreen()
        if visual, err := screen.GetRGBAVisual(); err == nil && visual != nil {
                win.SetVisual(visual)
        }
        applyTheme(win)

        win.Connect("draw", func(_ *gtk.Window, ctx *cairo.Context) bool {
                p := currentPalette()
                drawRoundedRect(ctx, 0, 0, float64(win.GetAllocatedWidth()), float64(win.GetAllocatedHeight()), 14, p.Bg)
                return false
        })
        setupFramelessBehavior(win)

        win.Connect("destroy", func() {
                app.captureSession()
                if store != nil {
                        _ = store.Save()
                }
                gtk.MainQuit()
        })
        win.Connect("key-press-event", func(_ *gtk.Window, ev *gdk.Event) bool {
                ke := gdk.EventKeyNewFromEvent(ev)
                state := ke.State()
                if state&gdk.CONTROL_MASK != 0 {
                        if ke.KeyVal() == gdk.KEY_f && app.hostsUI != nil {
                                app.hostsUI.search.GrabFocus()
                                return true
                        }
                        // Ctrl+1..9 — switch pages
                        kv := uint(ke.KeyVal())
                        pageKeys := []uint{gdk.KEY_1, gdk.KEY_2, gdk.KEY_3, gdk.KEY_4, gdk.KEY_5, gdk.KEY_6, gdk.KEY_7, gdk.KEY_8, gdk.KEY_9}
                        for i := 0; i < len(navItems) && i < len(pageKeys); i++ {
                                if kv == pageKeys[i] {
                                        app.activateNav(navItems[i].id)
                                        return true
                                }
                        }
                }
                return false
        })

        root := boxNew(gtk.ORIENTATION_VERTICAL, 0)
        win.Add(root)
        // контур изменения размера: полосы по периметру позволяют тянуть окно
        // мышью за любой край (см. edgeStrip)
        root.PackStart(edgeStrip("top"), false, false, 0)

        mid := boxNew(gtk.ORIENTATION_HORIZONTAL, 0)
        root.PackStart(mid, true, true, 0)
        mid.PackStart(edgeStrip("left"), false, false, 0)

        content := boxNew(gtk.ORIENTATION_VERTICAL, 0)
        mid.PackStart(content, true, true, 0)
        content.PackStart(buildHeader(), false, false, 0)

        main := boxNew(gtk.ORIENTATION_HORIZONTAL, 0)
        content.PackStart(main, true, true, 0)
        main.PackStart(buildSidebar(), false, false, 0)

        app.pages, _ = gtk.StackNew()
        app.pages.SetTransitionType(gtk.STACK_TRANSITION_TYPE_CROSSFADE)
        app.pages.SetTransitionDuration(160)
        app.pages.SetMarginStart(10)
        app.pages.SetMarginEnd(10)
        app.pages.SetMarginTop(10)
        app.pages.SetMarginBottom(6)
        main.PackStart(app.pages, true, true, 0)

        content.PackStart(buildExecBar(), false, false, 0)
        content.PackStart(buildStatusBar(), false, false, 0)

        mid.PackEnd(edgeStrip("right"), false, false, 0)
        root.PackEnd(edgeStrip("bottom"), false, false, 0)

        // --- pages ---
        app.hostsUI = newHostsUI()
        app.pages.AddNamed(app.hostsUI.widget, "hosts")
        app.pbUI = newPlaybookUI()
        app.pages.AddNamed(app.pbUI.widget, "playbook")
        app.tplUI = newTemplatesUI()
        app.pages.AddNamed(app.tplUI.widget, "templates")
        app.chainUI = newChainsUI()
        app.pages.AddNamed(app.chainUI.widget, "chains")
        app.schedUI = newSchedUI()
        app.pages.AddNamed(app.schedUI.widget, "sched")
        app.monUI = newMonitorUI()
        app.pages.AddNamed(app.monUI.widget, "monitor")
        app.sftpUI = newSFTPUI()
        app.pages.AddNamed(app.sftpUI.widget, "sftp")
        app.mtUI = newMultiTermUI()
        app.pages.AddNamed(app.mtUI.widget, "multiterm")
        app.repUI = newReportsUI()
        app.pages.AddNamed(app.repUI.widget, "reports")
        app.auditUI = newAuditUI()
        app.pages.AddNamed(app.auditUI.widget, "audit")

        app.activateNav("hosts")
        // восстановление последней сессии: геометрия окна, активная вкладка,
        // выбор хостов и группы (сохраняются при выходе — appQuit/destroy)
        app.restoreSession(selftest)
        win.ShowAll()
        // положения разделителей восстанавливаем после первого выделения
        restorePanedPositions()

        if selftest {
                glib.TimeoutAdd(900, func() bool {
                        gtk.MainQuit()
                        return false
                })
        }
        return nil
}

// refreshAfterUnlock перезагружает все данные-вкладки после разблокировки
// зашифрованного хранилища.
func (a *application) refreshAfterUnlock() {
        if a.hostsUI != nil {
                a.hostsUI.refreshAll()
        }
        if a.pbUI != nil {
                a.pbUI.refresh()
        }
        if a.tplUI != nil {
                a.tplUI.refresh()
        }
        if a.chainUI != nil {
                a.chainUI.refresh()
        }
        if a.schedUI != nil {
                a.schedUI.refresh()
        }
        if a.repUI != nil {
                a.repUI.refresh()
        }
        if a.auditUI != nil {
                a.auditUI.refresh()
        }
        if a.sftpUI != nil {
                a.sftpUI.refreshHosts()
        }
        logf("ok", "📂 Данные загружены: хостов %d, шаблонов %d, расписаний %d",
                len(store.State.Hosts), len(store.State.Templates), len(store.State.Schedules))
}

// ---------- screenshots (self-documenting UI verification) ----------

// captureSeq switches pages one by one and saves window PNGs into dir.
func (a *application) captureSeq(dir string, i int) {
        if i >= len(navItems) {
                gtk.MainQuit()
                return
        }
        it := navItems[i]
        a.activateNav(it.id)
        glibTimeout(420, func() bool {
                w, h := a.window.GetAllocatedWidth(), a.window.GetAllocatedHeight()
                if gwin, err := a.window.GetWindow(); err == nil && gwin != nil {
                        if pb, err := gwin.PixbufGetFromWindow(0, 0, w, h); err == nil && pb != nil {
                                fn := filepath.Join(dir, fmt.Sprintf("page_%02d_%s.png", i+1, it.id))
                                if err := pb.SavePNG(fn, 4); err != nil {
                                        fmt.Fprintln(os.Stderr, "shot:", err)
                                } else {
                                        fmt.Println("shot:", fn)
                                }
                        } else if err != nil {
                                fmt.Fprintln(os.Stderr, "grab:", err)
                        }
                }
                glibTimeout(80, func() bool {
                        a.captureSeq(dir, i+1)
                        return false
                })
                return false
        })
}

func appQuit() {
        if app != nil {
                app.captureSession()
        }
        if store != nil {
                _ = store.Save()
        }
        gtk.MainQuit()
}

// ---------- сохранение и восстановление сессии ----------

// captureSession запоминает геометрию окна, активную вкладку, выбранных
// хостов и выбранную группу — чтобы при следующем запуске приложение
// выглядело так же, как при закрытии.
func (a *application) captureSession() {
        if a == nil || a.window == nil || store == nil {
                return
        }
        w, h := a.window.GetSize()
        x, y := a.window.GetPosition()
        savePanedPositions()
        store.State.Session = SessionState{
                W: w, H: h, X: x, Y: y,
                Maximized:     a.maximized,
                Nav:           a.active,
                SelectedHosts: a.hostsUI.selectedHosts(),
                Group:         a.hostsUI.lastGroup,
        }
}

// restoreSession применяет сохранённую сессию (если она есть).
func (a *application) restoreSession(selftest bool) {
        if selftest || store == nil {
                return
        }
        s := store.State.Session
        if s.W > 200 && s.H > 150 {
                // восстановленный размер ограничиваем текущим экраном: после смены
                // монитора окно не должно «вываться» за пределы рабочего стола
                if scr := a.window.GetScreen(); scr != nil {
                        if rw, rerr := scr.GetRootWindow(); rerr == nil && rw != nil {
                                sw, sh := rw.WindowGetWidth(), rw.WindowGetHeight()
                                if s.W > sw-40 {
                                        s.W = sw - 40
                                }
                                if s.H > sh-90 {
                                        s.H = sh - 90
                                }
                        }
                }
                if s.W < 960 {
                        s.W = 960
                }
                if s.H < 560 {
                        s.H = 560
                }
                a.window.Resize(s.W, s.H)
                if s.X > -2000 && s.Y > -2000 {
                        a.window.Move(s.X, s.Y)
                }
        }
        if s.Maximized {
                a.maximized = true
                a.window.Maximize()
        }
        if s.Nav != "" {
                for _, it := range navItems {
                        if it.id == s.Nav {
                                a.activateNav(s.Nav)
                                break
                        }
                }
        }
        if a.hostsUI != nil {
                a.hostsUI.restoreSession(s)
        }
}

func fileExists(p string) bool {
        _, err := os.Stat(p)
        return err == nil
}
