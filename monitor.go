package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// MonitorUI — per-host CPU / RAM / disk / load dashboard с историей
// и графиком за выбранный период.
type MonitorUI struct {
	widget   gtk.IWidget
	grid     *gtk.Grid
	rows     map[string]*monRow
	auto     bool
	alerted  map[string]bool
	mu       sync.Mutex
	lastCPU  map[string]cpuSample // ip -> предыдущий счётчик /proc/stat (для точного CPU)
	hist     map[string][]monHist // ip -> история замеров
	interval int

	// график
	hostCombo *gtk.ComboBoxText
	periodC   *gtk.ComboBoxText
	chart     *gtk.DrawingArea
}

type monRow struct {
	ip     *gtk.Label // имя + IP (markup)
	cpu    *gtk.ProgressBar
	ram    *gtk.ProgressBar
	disk   *gtk.ProgressBar
	load   *gtk.Label
	status *gtk.Label
}

type hostStat struct {
	CPUPct  float64
	MemPct  float64
	DiskPct float64
	Load1   float64
	Err     string
}

// cpuSample — сырые счётчики /proc/stat для расчёта загрузки по дельте.
type cpuSample struct {
	idle  float64 // idle + iowait
	total float64
}

// monHist — точка истории для графика.
type monHist struct {
	t    time.Time
	cpu  float64
	ram  float64
	disk float64
}

const monHistMax = 720 // при интервале 5 с это час истории

func newMonitorUI() *MonitorUI {
	u := &MonitorUI{
		rows:     map[string]*monRow{},
		alerted:  map[string]bool{},
		lastCPU:  map[string]cpuSample{},
		hist:     map[string][]monHist{},
		interval: monitorIntervalDef(),
	}
	root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

	toolbar := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	btnRefresh := buttonNew("Обновить")
	btnRefresh.Connect("clicked", func() { u.refreshOnce() })
	btnAuto, _ := gtk.CheckButtonNewWithLabel("Автообновление")
	btnAuto.SetActive(true) // включено по умолчанию: график наполняется сразу
	btnAuto.Connect("toggled", func() {
		u.auto = btnAuto.GetActive()
		if u.auto {
			u.scheduleNext()
			logf("info", "📊 Мониторинг: автообновление каждые %d с", u.interval)
		}
	})
	u.auto = true
	btnTh, _ := gtk.SpinButtonNewWithRange(1, 3600, 1)
	btnTh.SetValue(float64(u.interval))
	btnTh.SetTooltipText("Интервал автообновления, сек")
	btnTh.Connect("value-changed", func() {
		u.interval = int(btnTh.GetValue())
		store.State.Settings.MonitorInterval = u.interval
		_ = store.Save()
	})
	toolbar.PackStart(btnRefresh, false, false, 0)
	toolbar.PackStart(btnAuto, false, false, 0)
	toolbar.PackStart(labelNew("Интервал, с:"), false, false, 0)
	toolbar.PackStart(btnTh, false, false, 0)
	root.PackStart(toolbar, false, false, 0)

	sc := scrolled(nil, 0)
	u.grid, _ = gtk.GridNew()
	u.grid.SetRowSpacing(8)
	u.grid.SetColumnSpacing(8)
	u.grid.SetMarginTop(6)
	sc.Add(u.grid)
	sc.SetVExpand(true)
	root.PackStart(sc, true, true, 0)

	// ---- график за период ----
	card, cbox := cardBox("График за период")
	legend := boxNew(gtk.ORIENTATION_HORIZONTAL, 10)
	for _, it := range []struct{ col, txt string }{
		{"#7c8cff", "CPU"},
		{"#60a5fa", "RAM"},
		{"#fbbf24", "Диск"},
	} {
		mk := labelNew("● " + it.txt)
		mk.SetMarkup("<span foreground=\"" + it.col + "\">●</span> " + it.txt)
		legend.PackStart(mk, false, false, 0)
	}
	head := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
	head.PackStart(legend, true, true, 0)
	u.hostCombo, _ = gtk.ComboBoxTextNew()
	u.hostCombo.SetSizeRequest(200, -1)
	u.hostCombo.SetTooltipText("Хост для графика")
	head.PackStart(labelNew("Хост:"), false, false, 0)
	head.PackStart(u.hostCombo, false, false, 0)
	u.periodC, _ = gtk.ComboBoxTextNew()
	for _, p := range []string{"5 минут", "15 минут", "1 час"} {
		u.periodC.AppendText(p)
	}
	u.periodC.SetActive(1)
	u.periodC.SetTooltipText("Период графика")
	head.PackStart(labelNew("Период:"), false, false, 0)
	head.PackStart(u.periodC, false, false, 0)
	u.periodC.Connect("changed", func() { u.chart.QueueDraw() })
	u.hostCombo.Connect("changed", func() { u.chart.QueueDraw() })
	cbox.PackStart(head, false, false, 0)
	u.chart, _ = gtk.DrawingAreaNew()
	u.chart.SetSizeRequest(-1, 190)
	addClass(u.chart, "monchart")
	u.chart.Connect("draw", u.onDraw)
	cbox.PackStart(u.chart, true, true, 0)
	card.SetSizeRequest(-1, 250)
	root.PackStart(card, false, false, 0)

	u.widget = root
	// первый опрос, когда окно уже показано
	glibTimeout(600, func() bool {
		u.refreshOnce()
		u.scheduleNext()
		return false
	})
	return u
}

func glibTimeout(ms uint, fn func() bool) {
	glib.TimeoutAdd(ms, fn)
}
func monitorIntervalDef() int {
	v := store.State.Settings.MonitorInterval
	if v <= 0 {
		v = 5
	}
	return v
}

// refreshHostCombo синхронизирует селектор хостов графика со списком хостов.
func (u *MonitorUI) refreshHostCombo() {
	if u.hostCombo == nil {
		return
	}
	cur := u.hostCombo.GetActiveText()
	u.hostCombo.RemoveAll()
	ips := app.hostsUI.targetHosts()
	def := 0
	for i, ip := range ips {
		disp := ip
		if h := store.FindHost(ip); h != nil {
			disp = h.DisplayName()
		}
		u.hostCombo.AppendText(disp)
		if disp == cur {
			def = i
		}
	}
	if len(ips) > 0 {
		u.hostCombo.SetActive(def)
	}
}

func (u *MonitorUI) ensureRows() {
	ips := app.hostsUI.targetHosts()
	// header row
	if c0, _ := u.grid.GetChildAt(0, 0); c0 == nil {
		u.grid.Attach(mkMonHeader("Хост"), 0, 0, 1, 1)
		u.grid.Attach(mkMonHeader("CPU"), 1, 0, 1, 1)
		u.grid.Attach(mkMonHeader("RAM"), 2, 0, 1, 1)
		u.grid.Attach(mkMonHeader("Диск"), 3, 0, 1, 1)
		u.grid.Attach(mkMonHeader("Load"), 4, 0, 1, 1)
		u.grid.Attach(mkMonHeader("Статус"), 5, 0, 1, 1)
	}
	present := map[string]bool{}
	for _, ip := range ips {
		present[ip] = true
		if _, ok := u.rows[ip]; !ok {
			u.rows[ip] = u.newRow(ip)
		}
	}
	// remove stale rows
	for ip, r := range u.rows {
		if !present[ip] {
			u.grid.Remove(r.ip)
			u.grid.Remove(r.cpu)
			u.grid.Remove(r.ram)
			u.grid.Remove(r.disk)
			u.grid.Remove(r.load)
			u.grid.Remove(r.status)
			delete(u.rows, ip)
		}
	}
	// reorder rows: remove + attach. NB: gtk_grid_attach НЕ перемещает уже
	// добавленного ребёнка — без Remove метки хостов оставались на временной
	// строке 99 и были невидимы (жалоба «плохо видно хост»).
	row := 1
	for _, ip := range ips {
		if r, ok := u.rows[ip]; ok {
			u.grid.Remove(r.ip)
			u.grid.Remove(r.cpu)
			u.grid.Remove(r.ram)
			u.grid.Remove(r.disk)
			u.grid.Remove(r.load)
			u.grid.Remove(r.status)
			u.grid.Attach(r.ip, 0, row, 1, 1)
			u.grid.Attach(r.cpu, 1, row, 1, 1)
			u.grid.Attach(r.ram, 2, row, 1, 1)
			u.grid.Attach(r.disk, 3, row, 1, 1)
			u.grid.Attach(r.load, 4, row, 1, 1)
			u.grid.Attach(r.status, 5, row, 1, 1)
			row++
		}
	}
	u.grid.ShowAll()
	u.refreshHostCombo()
}

func mkMonHeader(t string) *gtk.Label {
	l := labelNew(t)
	sc, _ := l.GetStyleContext()
	sc.AddClass("ratitle")
	sc.AddClass("monhead")
	l.SetXAlign(0)
	return l
}

func (u *MonitorUI) newRow(ip string) *monRow {
	r := &monRow{}
	disp := ip
	if h := store.FindHost(ip); h != nil && strings.TrimSpace(h.Name) != "" {
		disp = "<b>" + xmlEscapeStr(h.Name) + "</b>  <span size=\"smaller\" foreground=\"#8b93a5\">" + xmlEscapeStr(ip) + "</span>"
	}
	r.ip = labelNew(disp)
	r.ip.SetMarkup(disp)
	r.ip.SetXAlign(0)
	r.ip.SetSizeRequest(190, -1)
	r.cpu, _ = gtk.ProgressBarNew()
	r.cpu.SetShowText(true)
	r.cpu.SetSizeRequest(150, -1)
	r.cpu.SetVAlign(gtk.ALIGN_CENTER)
	r.ram, _ = gtk.ProgressBarNew()
	r.ram.SetShowText(true)
	r.ram.SetSizeRequest(150, -1)
	r.ram.SetVAlign(gtk.ALIGN_CENTER)
	r.disk, _ = gtk.ProgressBarNew()
	r.disk.SetShowText(true)
	r.disk.SetSizeRequest(150, -1)
	r.disk.SetVAlign(gtk.ALIGN_CENTER)
	addClass(r.cpu, "monbar")
	addClass(r.ram, "monbar")
	addClass(r.disk, "monbar")
	r.load = labelNew("—")
	r.load.SetSizeRequest(52, -1)
	r.status = labelNew("○")
	// NB: временный Attach не нужен — ensureRows ставит все виджеты сам
	return r
}

func monBarClass(pb *gtk.ProgressBar, pct float64) {
	if sc, err := pb.GetStyleContext(); err == nil {
		sc.RemoveClass("pb-ok")
		sc.RemoveClass("pb-warn")
		sc.RemoveClass("pb-danger")
		switch {
		case pct >= 90:
			sc.AddClass("pb-danger")
		case pct >= 70:
			sc.AddClass("pb-warn")
		default:
			sc.AddClass("pb-ok")
		}
	}
}

func (u *MonitorUI) refreshOnce() {
	if app.hostsUI == nil {
		return
	}
	u.ensureRows()
	ips := app.hostsUI.targetHosts()
	if len(ips) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			st := u.collectStat(ip)
			glibIdle(func() { u.applyStat(ip, st) })
		}(ip)
	}
	// async: do not block UI
}

func (u *MonitorUI) collectStat(ip string) hostStat {
	st := hostStat{}
	out, err := execOnHost(ip,
		"echo CPU $(grep '^cpu ' /proc/stat); "+
			"echo MEM $(grep -E 'MemTotal|MemAvailable' /proc/meminfo | tr '\\n' ' '); "+
			"echo LOAD $(cat /proc/loadavg); "+
			"df -kP -x tmpfs -x devtmpfs -x iso9660 2>/dev/null | tail -n +2", false)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	var cpuLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "CPU ") {
			cpuLine = strings.TrimSpace(strings.TrimPrefix(line, "CPU "))
			st.CPUPct = u.cpuPctDelta(ip, cpuLine)
		}
		if strings.HasPrefix(line, "MEM ") {
			st.MemPct = parseMemPct(strings.TrimPrefix(line, "MEM "))
		}
		if strings.HasPrefix(line, "LOAD ") {
			fields := strings.Fields(strings.TrimPrefix(line, "LOAD "))
			if len(fields) > 0 {
				st.Load1, _ = strconv.ParseFloat(fields[0], 64)
			}
		}
		if strings.HasPrefix(line, "/") {
			// df -kP: fields: fs 1024-blocks used available capacity mounted
			f := strings.Fields(line)
			if len(f) >= 6 && !strings.HasPrefix(f[5], "/boot") {
				total, _ := strconv.ParseFloat(f[1], 64)
				used, _ := strconv.ParseFloat(f[2], 64)
				if total > 0 {
					pct := used / total * 100
					if pct > st.DiskPct {
						st.DiskPct = pct
					}
				}
			}
		}
	}
	return st
}

// cpuPctDelta считает загрузку CPU как долю занятого времени МЕЖДУ двумя
// опросами (точная величина), при первом опросе — оценку «с момента загрузки».
func (u *MonitorUI) cpuPctDelta(ip, line string) float64 {
	f := strings.Fields(line)
	if len(f) < 5 {
		return 0
	}
	var vals []float64
	for _, s := range f {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		vals = append(vals, v)
	}
	if len(vals) < 5 {
		return 0
	}
	idle := vals[3] + vals[4]
	total := 0.0
	for _, v := range vals {
		total += v
	}
	pct := 0.0
	if prev, ok := u.lastCPU[ip]; ok && total > prev.total {
		dTotal := total - prev.total
		dIdle := idle - prev.idle
		if dTotal > 0 {
			pct = (1 - dIdle/dTotal) * 100
		}
	} else if total > 0 {
		pct = (1 - idle/total) * 100
	}
	u.lastCPU[ip] = cpuSample{idle: idle, total: total}
	if pct < 0 {
		pct = 0
	}
	return pct
}

func parseMemPct(s string) float64 {
	var total, avail float64
	fields := strings.Fields(s)
	for i, f := range fields {
		if f == "MemTotal:" && i+1 < len(fields) {
			total, _ = strconv.ParseFloat(fields[i+1], 64)
		}
		if f == "MemAvailable:" && i+1 < len(fields) {
			avail, _ = strconv.ParseFloat(fields[i+1], 64)
		}
	}
	if total <= 0 {
		return 0
	}
	return (total - avail) / total * 100
}

func (u *MonitorUI) applyStat(ip string, st hostStat) {
	// история — даже для пустого ответа (ошибку видно разрывом линии)
	u.mu.Lock()
	u.hist[ip] = append(u.hist[ip], monHist{
		t: time.Now(), cpu: st.CPUPct, ram: st.MemPct, disk: st.DiskPct,
	})
	if len(u.hist[ip]) > monHistMax {
		u.hist[ip] = u.hist[ip][len(u.hist[ip])-monHistMax:]
	}
	u.mu.Unlock()

	r, ok := u.rows[ip]
	if !ok {
		return
	}
	if st.Err != "" {
		r.status.SetText("●")
		r.ip.SetMarkup("<span foreground=\"#f87171\">" + xmlEscapeStr(hostDisp(ip)) + "</span>")
		r.cpu.SetText("—")
		r.ram.SetText("—")
		r.disk.SetText("—")
		r.load.SetText("—")
		return
	}
	r.ip.SetMarkup(hostDispMarkup(ip))
	r.cpu.SetFraction(clamp01(st.CPUPct / 100))
	r.cpu.SetText(fmt.Sprintf("%.0f%%", st.CPUPct))
	r.ram.SetFraction(clamp01(st.MemPct / 100))
	r.ram.SetText(fmt.Sprintf("%.0f%%", st.MemPct))
	r.disk.SetFraction(clamp01(st.DiskPct / 100))
	r.disk.SetText(fmt.Sprintf("%.0f%%", st.DiskPct))
	r.load.SetText(fmt.Sprintf("%.2f", st.Load1))
	monBarClass(r.cpu, st.CPUPct)
	monBarClass(r.ram, st.MemPct)
	monBarClass(r.disk, st.DiskPct)
	if st.DiskPct >= 90 || st.MemPct >= 95 || st.Load1 > 8 {
		r.status.SetMarkup("<span foreground=\"#f44336\" weight=\"bold\">⚠</span>")
		if st.DiskPct >= 90 && !u.alerted["disk:"+ip] {
			u.alerted["disk:"+ip] = true
			Notify("Мониторинг: "+hostDisp(ip), fmt.Sprintf("Диск занят на %.0f%%!", st.DiskPct))
			logf("warn", "⚠ %s: диск %.0f%% — порог 90%%", hostDisp(ip), st.DiskPct)
		}
		if st.MemPct >= 95 && !u.alerted["mem:"+ip] {
			u.alerted["mem:"+ip] = true
			Notify("Мониторинг: "+hostDisp(ip), fmt.Sprintf("RAM занята на %.0f%%!", st.MemPct))
			logf("warn", "⚠ %s: RAM %.0f%% — порог 95%%", hostDisp(ip), st.MemPct)
		}
	} else {
		r.status.SetMarkup("<span foreground=\"#4CAF50\">●</span>")
	}
	if u.chart != nil {
		u.chart.QueueDraw()
	}
}

func hostDisp(ip string) string {
	if h := store.FindHost(ip); h != nil {
		return h.DisplayName()
	}
	return ip
}

func hostDispMarkup(ip string) string {
	d := hostDisp(ip)
	if d != ip {
		return "<b>" + xmlEscapeStr(d) + "</b>  <span size=\"smaller\" foreground=\"#8b93a5\">" + xmlEscapeStr(ip) + "</span>"
	}
	return xmlEscapeStr(ip)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (u *MonitorUI) scheduleNext() {
	if !u.auto {
		return
	}
	interval := u.interval
	if interval <= 0 {
		interval = 5
	}
	glibTimeout(uint(interval)*1000, func() bool {
		if !u.auto {
			return false
		}
		u.refreshOnce()
		u.scheduleNext()
		return false
	})
}

// ---------- график ----------

func (u *MonitorUI) chartHost() string {
	if u.hostCombo == nil {
		return ""
	}
	disp := u.hostCombo.GetActiveText()
	if disp == "" {
		return ""
	}
	// disp может быть именем — ищем IP
	for _, h := range store.State.Hosts {
		if h.DisplayName() == disp {
			return h.IP
		}
	}
	return disp
}

func (u *MonitorUI) chartWindow() time.Duration {
	switch u.periodC.GetActive() {
	case 0:
		return 5 * time.Minute
	case 2:
		return time.Hour
	}
	return 15 * time.Minute
}

// onDraw рисует график: сетка, линии CPU/RAM/диск, подписи времени.
func (u *MonitorUI) onDraw(_ *gtk.DrawingArea, ctx *cairo.Context) bool {
	w := float64(u.chart.GetAllocatedWidth())
	h := float64(u.chart.GetAllocatedHeight())
	if w < 40 || h < 30 {
		return false
	}
	p := currentPalette()
	// фон
	drawRoundedRect(ctx, 0, 0, w, h, 8, a(p.Bg, 0.45))

	ip := u.chartHost()
	u.mu.Lock()
	samples := append([]monHist(nil), u.hist[ip]...)
	u.mu.Unlock()

	left, right := 42.0, 10.0
	top, bottom := 10.0, 22.0
	pw, ph := w-left-right, h-top-bottom
	if pw < 10 || ph < 10 {
		return false
	}

	// сетка + подписи Y (0/50/100)
	ctx.SetFontSize(9)
	for _, gv := range []float64{0, 25, 50, 75, 100} {
		y := top + ph*(1-gv/100)
		ctx.SetSourceRGBA(hexToRGBA(a(p.Text, 0.10)))
		ctx.SetLineWidth(1)
		ctx.MoveTo(left, y)
		ctx.LineTo(left+pw, y)
		ctx.Stroke()
		ctx.SetSourceRGBA(hexToRGBA(p.Faint))
		ctx.MoveTo(4, y+3)
		ctx.ShowText(fmt.Sprintf("%d", int(gv)))
	}

	// окно периода
	since := time.Now().Add(-u.chartWindow())
	var pts []monHist
	for _, s := range samples {
		if !s.t.Before(since) {
			pts = append(pts, s)
		}
	}
	if len(pts) < 2 {
		ctx.SetSourceRGBA(hexToRGBA(p.Faint))
		ctx.SetFontSize(11)
		ctx.MoveTo(left+pw/2-120, top+ph/2)
		ctx.ShowText("Собираем данные… включите автообновление")
		return false
	}

	// подписи времени X
	ctx.SetSourceRGBA(hexToRGBA(p.Faint))
	ctx.SetFontSize(9)
	t0, t1 := pts[0].t, pts[len(pts)-1].t
	ctx.MoveTo(left, h-6)
	ctx.ShowText(t0.Format("15:04:05"))
	lbl := t1.Format("15:04:05")
	ctx.MoveTo(w-right-float64(len(lbl))*5.4, h-6)
	ctx.ShowText(lbl)

	series := []struct {
		col string
		get func(monHist) float64
	}{
		{p.Accent, func(s monHist) float64 { return s.cpu }},
		{p.Info, func(s monHist) float64 { return s.ram }},
		{p.Warn, func(s monHist) float64 { return s.disk }},
	}
	for _, s := range series {
		col := s.col
		ctx.SetSourceRGBA(hexToRGBA(col))
		ctx.SetLineWidth(1.8)
		first := true
		var lx, ly float64
		for _, pt := range pts {
			frac := 0.0
			if dt := t1.Sub(t0).Seconds(); dt > 0 {
				frac = pt.t.Sub(t0).Seconds() / dt
			}
			x := left + pw*frac
			y := top + ph*(1-clamp01(s.get(pt)/100))
			if first {
				ctx.MoveTo(x, y)
				first = false
			} else {
				ctx.LineTo(x, y)
			}
			lx, ly = x, y
		}
		ctx.Stroke()
		// маркер последней точки
		if !first {
			ctx.Arc(lx, ly, 2.4, 0, 2*3.14159)
			ctx.Fill()
		}
	}
	return false
}
