package main

import (
        "encoding/csv"
        "fmt"
        "html"
        "os"
        "strings"
        "time"

        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

// ReportsUI — report log tab with HTML/CSV export.
type ReportsUI struct {
        widget gtk.IWidget
        tree   *gtk.TreeView
        st     *gtk.ListStore
        detail *gtk.TextView
        filter *gtk.ComboBoxText
        curFlt int // 0 = all, 1 = только ошибки/предупреждения, 2 = только успешные
}

const (
        RCOL_TIME = 0
        RCOL_TASK = 1
        RCOL_HOST = 2
        RCOL_STAT = 3
        RCOL_IDX  = 4 // hidden: index into reports
)

func newReportsUI() *ReportsUI {
        u := &ReportsUI{}
        root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

        toolbar := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        toolbar.PackStart(labelNew("Показать:"), false, false, 2)
        u.filter, _ = gtk.ComboBoxTextNew()
        u.filter.AppendText("Все записи")
        u.filter.AppendText("Только ошибки и предупреждения")
        u.filter.AppendText("Только успешные")
        u.filter.SetActive(0)
        u.filter.SetTooltipText("Фильтр журнала по результату выполнения")
        u.filter.Connect("changed", func() {
                u.curFlt = u.filter.GetActive()
                u.refresh()
        })
        toolbar.PackStart(u.filter, false, false, 0)
        btnHTML := buttonNew("Экспорт HTML")
        btnHTML.SetTooltipText("Сохранить журнал как HTML-отчёт")
        btnHTML.Connect("clicked", func() { exportReportHTML() })
        btnCSV := buttonNew("Экспорт CSV")
        btnCSV.SetTooltipText("Сохранить журнал как CSV-таблицу")
        btnCSV.Connect("clicked", func() { exportReportCSV() })
        btnPDF := buttonNew("Экспорт PDF")
        btnPDF.SetTooltipText("Сохранить журнал как PDF-документ (шрифт встраивается, кириллица поддерживается)")
        btnPDF.Connect("clicked", func() { exportReportPDF() })
        btnClear := buttonNew("Очистить")
        btnClear.Connect("clicked", func() {
                if confirmDialog(app.window, "Очистить журнал отчётов?") {
                        store.State.Reports = nil
                        _ = store.Save()
                        u.refresh()
                }
        })
        toolbar.PackEnd(btnClear, false, false, 0)
        toolbar.PackEnd(btnCSV, false, false, 0)
        toolbar.PackEnd(btnPDF, false, false, 0)
        toolbar.PackEnd(btnHTML, false, false, 0)
        root.PackStart(toolbar, false, false, 0)

        // NB: the 5th (hidden) column carries the row index into reports;
        // it MUST exist in the model or selection clicks break (previously
        // caused Gtk-CRITICAL and a wrong detail pane).
        // Статус — Pango-markup: зелёный OK / красный FAIL / жёлтый WARN.
        u.tree, u.st = makeTreeM([]string{"Время", "Задача", "Хост", "Статус", ""},
                []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING},
                map[int]bool{RCOL_STAT: true})
        // двойной щелчок — «развёрнутая» запись: полный вывод команды
        u.tree.Connect("button-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
                eb := gdk.EventButtonNewFromEvent(ev)
                if eb.Type() != gdk.EVENT_2BUTTON_PRESS || eb.Button() != 1 {
                        return false
                }
                idx := u.selectedIndex()
                if idx < 0 || idx >= len(store.State.Reports) {
                        return false
                }
                r := store.State.Reports[idx]
                showOutputDialog(r.Task+" — "+r.Host, &LogDetail{
                        Task: r.Task, Host: r.Host, Status: r.Status, Time: r.Time, Output: r.Output,
                })
                return false
        })
        hideCol(u.tree, RCOL_IDX)
        sel, _ := u.tree.GetSelection()
        sel.Connect("changed", func() { u.showDetail() })
        sc := scrolled(u.tree, 0)
        sc.SetVExpand(true)

        u.detail, _ = gtk.TextViewNew()
        u.detail.SetEditable(false)
        u.detail.SetWrapMode(gtk.WRAP_WORD_CHAR)
        u.detail.SetMonospace(true)
        addClass(u.detail, "rpdetail")
        buf, _ := u.detail.GetBuffer()
        // палитра деталей — зависит от темы: тёмный/светлый фон
        dc := detailColors()
        buf.CreateTag("h1", map[string]interface{}{"weight": 700, "scale": 1.35, "foreground": dc.text})
        buf.CreateTag("badge-ok", map[string]interface{}{"weight": 700, "foreground": dc.ok})
        buf.CreateTag("badge-fail", map[string]interface{}{"weight": 700, "foreground": dc.fail})
        buf.CreateTag("badge-warn", map[string]interface{}{"weight": 700, "foreground": dc.warn})
        buf.CreateTag("sec", map[string]interface{}{"weight": 700, "foreground": dc.sec})
        buf.CreateTag("dim", map[string]interface{}{"foreground": dc.dim})
        buf.CreateTag("err", map[string]interface{}{"foreground": dc.fail})
        buf.CreateTag("warng", map[string]interface{}{"foreground": dc.warn})
        buf.CreateTag("okc", map[string]interface{}{"foreground": dc.ok})
        buf.CreateTag("cmd", map[string]interface{}{"foreground": dc.cmd, "weight": 700})
        buf.CreateTag("ansi-bold", map[string]interface{}{"weight": 700})
        dsc := scrolled(u.detail, 0)
        dsc.SetVExpand(true)
        dsc.SetSizeRequest(340, -1)

        // NB: правая панель (детали записи) раньше была приклеена к правому
        // краю без возможности растянуть её — пользователь жаловался, что она
        // узкая. Теперь это GtkPaned: границу «список | детали» можно тащить
        // мышью, положение запоминается между сеансами.
        panes := makeHPaned("reports", sc, dsc, 560)

        root.PackStart(panes, true, true, 0)
        u.refresh()
        u.widget = root
        return u
}

func (u *ReportsUI) refresh() {
        if u.st == nil {
                return
        }
        u.st.Clear()
        reps := store.State.Reports
        // newest first, honoring the filter
        for i := len(reps) - 1; i >= 0; i-- {
                r := reps[i]
                if u.curFlt == 1 && r.Status == "OK" {
                        continue
                }
                if u.curFlt == 2 && r.Status != "OK" {
                        continue
                }
                iter := u.st.Append()
                _ = u.st.Set(iter, []int{RCOL_TIME, RCOL_TASK, RCOL_HOST, RCOL_STAT, RCOL_IDX},
                        []interface{}{r.Time, r.Task, r.Host, statusMarkup(r.Status), fmt.Sprintf("%d", i)})
        }
        if u.detail != nil {
                tvSetText(u.detail, "")
        }
}

func (u *ReportsUI) selectedIndex() int {
        m, iter, ok := selIter(u.tree)
        if !ok {
                return -1
        }
        return atoiSafe(iterGetString(m, iter, RCOL_IDX))
}

func (u *ReportsUI) showDetail() {
        idx := u.selectedIndex()
        if idx < 0 || idx >= len(store.State.Reports) {
                tvSetText(u.detail, "")
                return
        }
        r := store.State.Reports[idx]
        buf, err := u.detail.GetBuffer()
        if err != nil || buf == nil {
                return
        }
        buf.SetText("")
        ins := func(text, tag string) {
                if text == "" {
                        return
                }
                iter := buf.GetEndIter()
                if tag != "" {
                        buf.InsertWithTagByName(iter, text, tag)
                } else {
                        buf.Insert(iter, text)
                }
        }

        // ---- шапка записи: задача, статус, хост, время ----
        badge := "badge-ok"
        switch r.Status {
        case "FAIL":
                badge = "badge-fail"
        case "WARN":
                badge = "badge-warn"
        }
        ins(r.Task+"\n", "h1")
        ins(statusEmoji(r.Status)+" "+r.Status+"\n", badge)
        ins("Хост:  "+r.Host+"\n", "dim")
        ins("Время:  "+r.Time+"\n\n", "dim")
        ins("────── ВЫВОД ──────\n\n", "sec")
        if strings.TrimSpace(r.Output) == "" {
                ins("(вывода нет — задача выполнена без вывода)\n", "dim")
                return
        }

        // ---- вывод: построчно, с ANSI-цветами и классификацией ошибок ----
        dc := detailColors()
        lines := strings.Split(strings.ReplaceAll(r.Output, "\r\n", "\n"), "\n")
        for _, line := range lines {
                line = strings.TrimRight(line, "\r")
                if strings.Contains(line, "\x1b") {
                        // строка с ANSI-кодами: вкладываем её сегменты как есть
                        segs, _ := parseANSIStream(line + "\n")
                        for _, sg := range segs {
                                tag := ""
                                switch {
                                case sg.Bold:
                                        tag = "ansi-bold"
                                case sg.Color != "" && sg.Color != dc.text:
                                        tag = nearestDetailTag(sg.Color, dc)
                                }
                                ins(sg.Text, tag)
                        }
                        continue
                }
                switch classifyLine(line) {
                case "cmd":
                        ins(line+"\n", "cmd")
                case "err":
                        ins(line+"\n", "err")
                case "warn":
                        ins(line+"\n", "warng")
                case "ok":
                        ins(line+"\n", "okc")
                default:
                        ins(line+"\n", "")
                }
        }
}

// detailColors — цвета панели деталей для активной темы.
type detailPalette struct {
        text, dim, ok, fail, warn, cmd, sec string
}

func detailColors() detailPalette {
        if isLight() {
                return detailPalette{
                        text: "#1c2230", dim: "#5c6577", ok: "#0e9f6e",
                        fail: "#dc4b3f", warn: "#b26a00", cmd: "#3574f0", sec: "#4353d8",
                }
        }
        return detailPalette{
                text: "#e8ebf4", dim: "#9aa3b8", ok: "#34d399",
                fail: "#f87171", warn: "#fbbf24", cmd: "#7dd3fc", sec: "#7c8cff",
        }
}

// nearestDetailTag подбирает тег панели деталей под ANSI-цвет вывода.
func nearestDetailTag(color string, dc detailPalette) string {
        switch color {
        case "#f87171", "#fb7185", "#ef4444":
                return "err"
        case "#34d399", "#6ee7b7", "#0e9f6e":
                return "okc"
        case "#fbbf24", "#fde68a", "#d9820b":
                return "warng"
        case "#60a5fa", "#93c5fd", "#38bdf8", "#7dd3fc":
                return "cmd"
        case "#9aa3b8", "#7c86a0":
                return "dim"
        default:
                _ = dc
                return ""
        }
}

// classifyLine — визуальная классификация строки вывода.
func classifyLine(line string) string {
        t := strings.TrimSpace(line)
        if t == "" {
                return ""
        }
        low := strings.ToLower(t)
        // команды и нумерация задач плейбука ([1/3] ▶ …)
        if strings.HasPrefix(t, "$") || strings.HasPrefix(t, "# ") ||
                strings.HasPrefix(t, "▶") || (len(t) > 0 && t[0] == '[' && strings.Contains(t, "]/")) {
                return "cmd"
        }
        // явные ошибки
        for _, kw := range []string{"ошибка", "ошибк", "error", "fail", "denied", "not found",
                "отказ", "cannot", "can't", "не найден", "недоступен", "не удалось", "failure", "fatal"} {
                if strings.Contains(low, kw) {
                        return "err"
                }
        }
        // предупреждения
        for _, kw := range []string{"warn", "предупрежд", "deprecated", "внимание", " устарел"} {
                if strings.Contains(low, kw) {
                        return "warn"
                }
        }
        // короткие позитивные итоги (OK, успешно, замена произведена)
        if len(t) < 80 && (strings.Contains(low, "ok") || strings.Contains(low, "успешн") || strings.Contains(low, "завершен")) {
                return "ok"
        }
        return ""
}

func exportReportHTML() {
        if len(store.State.Reports) == 0 {
                warnDialog(app.window, "Журнал пуст — нечего экспортировать")
                return
        }
        fn := fileChooser(app.window, "Сохранить HTML-отчёт", gtk.FILE_CHOOSER_ACTION_SAVE, "remoteadmin_report.html")
        if fn == "" {
                return
        }
        if err := os.WriteFile(fn, []byte(buildReportHTML()), 0644); err != nil {
                errDialog(app.window, "Экспорт: "+err.Error())
                return
        }
        logf("ok", "🌐 HTML-отчёт: %s", fn)
}

func exportReportCSV() {
        if len(store.State.Reports) == 0 {
                warnDialog(app.window, "Журнал пуст — нечего экспортировать")
                return
        }
        fn := fileChooser(app.window, "Сохранить CSV-отчёт", gtk.FILE_CHOOSER_ACTION_SAVE, "remoteadmin_report.csv")
        if fn == "" {
                return
        }
        f, err := os.Create(fn)
        if err != nil {
                errDialog(app.window, "Экспорт: "+err.Error())
                return
        }
        defer f.Close()
        w := csv.NewWriter(f)
        _ = w.Write([]string{"Время", "Задача", "Хост", "Статус", "Вывод"})
        for _, r := range store.State.Reports {
                _ = w.Write([]string{r.Time, r.Task, r.Host, r.Status, r.Output})
        }
        w.Flush()
        logf("ok", "📊 CSV-отчёт: %s", fn)
}

func buildReportHTML() string {
        okN, failN, warnN := 0, 0, 0
        hosts := map[string]bool{}
        for _, r := range store.State.Reports {
                switch r.Status {
                case "OK":
                        okN++
                case "FAIL":
                        failN++
                case "WARN":
                        warnN++
                }
                hosts[r.Host] = true
        }
        var b strings.Builder
        b.WriteString(`<!DOCTYPE html>
<html lang="ru"><head><meta charset="utf-8">
<title>RemoteAdmin — отчёт</title>
<style>
body{font-family:'Segoe UI',DejaVu Sans,sans-serif;background:#f4f6f8;color:#202124;margin:24px;}
h1{font-size:22px;} .sub{color:#5f6368;font-size:13px;margin-bottom:18px;}
.cards{display:flex;gap:14px;margin-bottom:20px;}
.card{background:#fff;border-radius:10px;padding:14px 22px;box-shadow:0 1px 3px rgba(0,0,0,.12);}
.card .n{font-size:26px;font-weight:700;} .ok{color:#188038;} .fail{color:#d93025;} .warn{color:#e8710a;}
table{border-collapse:collapse;width:100%;background:#fff;border-radius:10px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,.12);}
th{background:#1f2933;color:#fff;text-align:left;padding:8px 12px;font-size:13px;}
td{padding:7px 12px;border-bottom:1px solid #e8eaed;font-size:13px;vertical-align:top;}
tr:hover td{background:#f4f6f8;}
.st-OK{color:#188038;font-weight:700;} .st-FAIL{color:#d93025;font-weight:700;} .st-WARN{color:#e8710a;font-weight:700;}
pre{white-space:pre-wrap;margin:0;color:#5f6368;max-width:520px;font-size:12px;}
</style></head><body>
`)
        fmt.Fprintf(&b, "<h1>RemoteAdmin — отчёт о выполнении</h1>\n<div class=\"sub\">Сформирован %s • RemoteAdmin %s</div>\n",
                time.Now().Format("02.01.2006 15:04:05"), AppVersion)
        b.WriteString("<div class=\"cards\">")
        fmt.Fprintf(&b, "<div class=\"card\"><div class=\"n\">%d</div>Всего</div>", len(store.State.Reports))
        fmt.Fprintf(&b, "<div class=\"card\"><div class=\"n ok\">%d</div>Успешно</div>", okN)
        fmt.Fprintf(&b, "<div class=\"card\"><div class=\"n warn\">%d</div>Предупреждения</div>", warnN)
        fmt.Fprintf(&b, "<div class=\"card\"><div class=\"n fail\">%d</div>Ошибки</div>", failN)
        fmt.Fprintf(&b, "<div class=\"card\"><div class=\"n\">%d</div>Хостов</div>", len(hosts))
        b.WriteString("</div>\n")
        b.WriteString("<table><tr><th>Время</th><th>Задача</th><th>Хост</th><th>Статус</th><th>Вывод</th></tr>\n")
        for _, r := range store.State.Reports {
                fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td><td class=\"st-%s\">%s</td><td><pre>%s</pre></td></tr>\n",
                        html.EscapeString(r.Time), html.EscapeString(r.Task), html.EscapeString(r.Host),
                        r.Status, r.Status, html.EscapeString(r.Output))
        }
        b.WriteString("</table></body></html>")
        return b.String()
}

var _ = gdk.KEY_f
