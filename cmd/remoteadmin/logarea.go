package main

import (
        "fmt"
        "os"
        "strings"
        "sync"
        "time"

        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

// ---------- log area ----------

var logColors = map[string]string{
        "ok":     "#34d399",
        "fail":   "#f87171",
        "warn":   "#fbbf24",
        "info":   "#60a5fa",
        "cmd":    "#7dd3fc",
        "accent": "#7c8cff",
}

type LogArea struct {
        view    *gtk.TextView
        buf     *gtk.TextBuffer
        widget  gtk.IWidget
        lines   int                  // всего строк в буфере (для привязки деталей)
        details map[int]*LogDetail   // номер строки -> раскрываемые данные
        mu      sync.Mutex
}

// LogDetail — данные «развёрнутой» записи журнала: полный вывод команды
// на конкретном хосте (stdout/stderr), открывается двойным щелчком.
type LogDetail struct {
        Task   string
        Host   string
        Status string
        Time   string
        Output string
}

func newLogArea() (*LogArea, error) {
        tv, err := gtk.TextViewNew()
        if err != nil {
                return nil, err
        }
        tv.SetEditable(false)
        tv.SetCursorVisible(false)
        tv.SetWrapMode(gtk.WRAP_WORD_CHAR)
        buf, err := tv.GetBuffer()
        if err != nil {
                return nil, err
        }
        for name, color := range logColors {
                buf.CreateTag(name, map[string]interface{}{
                        "foreground": color,
                })
        }
        buf.CreateTag("dim", map[string]interface{}{
                "foreground": "#8b93a5",
        })
        // placeholder line
        {
                iter := buf.GetStartIter()
                buf.InsertWithTagByName(iter, "Журнал событий появится здесь. Двойной щелчок по строке хоста — полный вывод.\n", "dim")
        }
        la := &LogArea{view: tv, buf: buf, details: map[int]*LogDetail{}}
        la.widget = tv
        // двойной щелчок по строке с деталями — окно с полным выводом
        tv.Connect("button-press-event", func(_ *gtk.TextView, ev *gdk.Event) bool {
                eb := gdk.EventButtonNewFromEvent(ev)
                if eb.Type() != gdk.EVENT_2BUTTON_PRESS || eb.Button() != 1 {
                        return false
                }
                bx, by := tv.WindowToBufferCoords(gtk.TEXT_WINDOW_TEXT, int(eb.X()), int(eb.Y()))
                iter := tv.GetIterAtLocation(bx, by)
                if iter == nil {
                        return false
                }
                la.openDetailAt(iter.GetLine())
                return false
        })
        return la, nil
}

// openDetailAt показывает полный вывод, если к строке привязаны детали.
func (l *LogArea) openDetailAt(line int) {
        l.mu.Lock()
        d := l.details[line]
        l.mu.Unlock()
        if d == nil {
                return
        }
        showOutputDialog(d.Task+" — "+d.Host, d)
}

// appendDetail добавляет строку журнала с привязанными «развёрнутыми»
// данными (двойной щелчок открывает полный вывод команды).
func (l *LogArea) appendDetail(kind, msg string, d *LogDetail) {
        if l == nil || l.buf == nil {
                return
        }
        glibIdle(func() {
                ts := time.Now().Format("15:04:05")
                l.mu.Lock()
                startLine := l.lines
                added := 1 + strings.Count(msg, "\n")
                l.lines += added
                if d != nil {
                        l.details[startLine] = d
                }
                l.mu.Unlock()
                iter := l.buf.GetEndIter()
                tag := ""
                switch kind {
                case "ok", "fail", "warn", "info", "cmd", "accent":
                        tag = kind
                }
                if tag != "" {
                        l.buf.InsertWithTagByName(iter, ts+"  ", "dim")
                        l.buf.InsertWithTagByName(iter, msg+"\n", tag)
                } else {
                        l.buf.Insert(iter, ts+"  "+msg+"\n")
                }
                mark := l.buf.CreateMark("scrollmark", l.buf.GetEndIter(), false)
                l.view.ScrollToMark(mark, 0.0, true, 0.0, 1.0)
                l.buf.DeleteMark(mark)
        })
}

// append logs a colored line; kind is one of ok|fail|warn|info|cmd|"".
func (l *LogArea) append(kind, msg string) {
        if l == nil || l.buf == nil {
                return
        }
        glibIdle(func() {
                ts := time.Now().Format("15:04:05")
                iter := l.buf.GetEndIter()
                tag := ""
                switch kind {
                case "ok", "fail", "warn", "info", "cmd", "accent":
                        tag = kind
                }
                if tag != "" {
                        l.buf.InsertWithTagByName(iter, ts+"  ", "dim")
                        l.buf.InsertWithTagByName(iter, msg+"\n", tag)
                } else {
                        l.buf.Insert(iter, ts+"  "+msg+"\n")
                }
                // autoscroll
                mark := l.buf.CreateMark("scrollmark", l.buf.GetEndIter(), false)
                l.view.ScrollToMark(mark, 0.0, true, 0.0, 1.0)
                l.buf.DeleteMark(mark)
        })
}

func (l *LogArea) clear() {
        if l == nil || l.buf == nil {
                return
        }
        l.buf.SetText("")
}

func (l *LogArea) exportText() {
        fn := fileChooser(app.window, "Сохранить журнал", gtk.FILE_CHOOSER_ACTION_SAVE, "remoteadmin_log.txt")
        if fn == "" {
                return
        }
        s, err := l.buf.GetText(l.buf.GetStartIter(), l.buf.GetEndIter(), false)
        if err != nil {
                s = ""
        }
        if err := os.WriteFile(fn, []byte(s), 0600); err != nil {
                errDialog(app.window, "Не удалось сохранить: "+err.Error())
                return
        }
        logf("ok", "📥 Журнал сохранён: %s", fn)
}

// logf logs into the app log area from anywhere.
func logf(kind, format string, args ...interface{}) {
        if app == nil || app.log == nil {
                fmt.Printf("[%s] %s\n", kind, fmt.Sprintf(format, args...))
                return
        }
        app.log.append(kind, fmt.Sprintf(format, args...))
}

// logfDetail logs a colored line and attaches expandable details
// (double-click opens the full command output). Маскирует секреты.
func logfDetail(kind, format string, d *LogDetail, args ...interface{}) {
        if app == nil || app.log == nil {
                fmt.Printf("[%s] %s\n", kind, fmt.Sprintf(format, args...))
                return
        }
        if d != nil {
                d.Output = redactSecrets(d.Output)
        }
        app.log.appendDetail(kind, redactSecrets(fmt.Sprintf(format, args...)), d)
}

// showOutputDialog — окно «развёрнутой» записи журнала: полный вывод
// команды (stdout/stderr) с цветовой разметкой и кнопкой копирования.
func showOutputDialog(title string, d *LogDetail) {
        dlg, err := gtk.DialogNewWithButtons(
                "Вывод: "+title,
                app.window, gtk.DIALOG_MODAL,
                []interface{}{"Копировать", 100},
                []interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
        )
        if err != nil {
                return
        }
        defer dlg.Destroy()
        defer unregisterModal()
        registerModal(dlg)
        dlg.SetDefaultSize(760, 520)

        box, _ := dlg.GetContentArea()
        box.SetMarginStart(10)
        box.SetMarginEnd(10)
        box.SetMarginTop(8)
        box.SetMarginBottom(6)

        // шапка: статус, хост, время
        dc0 := detailColors()
        stColor := dc0.ok
        switch d.Status {
        case "FAIL":
                stColor = dc0.fail
        case "WARN":
                stColor = dc0.warn
        }
        head := labelNew("")
        head.SetMarkup(fmt.Sprintf("<b>%s</b>   <span foreground=\"%s\">%s %s</span>   <span size=\"smaller\">%s</span>",
                escapeMarkup(d.Task), stColor, statusEmoji(d.Status), escapeMarkup(d.Status), escapeMarkup(d.Time)))
        head.SetXAlign(0)
        box.PackStart(head, false, false, 4)

        tv, _ := gtk.TextViewNew()
        tv.SetEditable(false)
        tv.SetCursorVisible(false)
        tv.SetWrapMode(gtk.WRAP_WORD_CHAR)
        tv.SetMonospace(true)
        buf, _ := tv.GetBuffer()
        dc := detailColors()
        buf.CreateTag("h1", map[string]interface{}{"weight": 700, "foreground": dc.text})
        buf.CreateTag("dim", map[string]interface{}{"foreground": dc.dim})
        buf.CreateTag("err", map[string]interface{}{"foreground": dc.fail})
        buf.CreateTag("warng", map[string]interface{}{"foreground": dc.warn})
        buf.CreateTag("okc", map[string]interface{}{"foreground": dc.ok})
        buf.CreateTag("cmd", map[string]interface{}{"foreground": dc.cmd, "weight": 700})
        buf.CreateTag("ansi-bold", map[string]interface{}{"weight": 700})

        out := strings.TrimRight(d.Output, "\n")
        if strings.TrimSpace(out) == "" {
                buf.InsertWithTagByName(buf.GetStartIter(), "(вывода нет — команда выполнена без вывода)\n", "dim")
        } else {
                for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
                        line = strings.TrimRight(line, "\r")
                        iter := buf.GetEndIter()
                        if strings.Contains(line, "\x1b") {
                                segs, _ := parseANSIStream(line + "\n")
                                for _, sg := range segs {
                                        tag := ""
                                        switch {
                                        case sg.Bold:
                                                tag = "ansi-bold"
                                        case sg.Color != "" && sg.Color != dc.text:
                                                tag = nearestDetailTag(sg.Color, dc)
                                        }
                                        if tag != "" {
                                                buf.InsertWithTagByName(iter, sg.Text, tag)
                                        } else {
                                                buf.Insert(iter, sg.Text)
                                        }
                                }
                                continue
                        }
                        switch classifyLine(line) {
                        case "cmd":
                                buf.InsertWithTagByName(iter, line+"\n", "cmd")
                        case "err":
                                buf.InsertWithTagByName(iter, line+"\n", "err")
                        case "warn":
                                buf.InsertWithTagByName(iter, line+"\n", "warng")
                        case "ok":
                                buf.InsertWithTagByName(iter, line+"\n", "okc")
                        default:
                                buf.Insert(iter, line+"\n")
                        }
                }
        }
        sc := scrolled(tv, 0)
        sc.SetVExpand(true)
        box.PackStart(sc, true, true, 0)
        box.ShowAll()

        resp := dlg.Run()
        if resp == 100 {
                clip, _ := gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)
                clip.SetText(d.Output)
                logf("ok", "📋 Вывод скопирован в буфер обмена")
        }
}

// escapeMarkup — экранирование Pango-разметки в заголовках.
func escapeMarkup(s string) string {
        r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
        return r.Replace(s)
}

// glibIdle runs fn on the GTK main loop.
func glibIdle(fn func()) {
        glib.IdleAdd(func() bool { fn(); return false })
}

// report helpers
var repRefreshScheduled bool

// scheduleReportRefresh debounces the Журнал-tab refresh: during parallel
// runs dozens of entries arrive at once, one full rebuild per entry would
// be wasteful.
func scheduleReportRefresh() {
        if repRefreshScheduled {
                return
        }
        repRefreshScheduled = true
        glibTimeout(600, func() bool {
                repRefreshScheduled = false
                if app != nil && app.repUI != nil {
                        app.repUI.refresh()
                }
                return false
        })
}

func addReport(task, host, status, output string) {
        out := redactSecrets(output)
        if len(out) > 4000 {
                out = out[:4000] + "\n… (обрезано)"
        }
        store.AddReport(ReportEntry{
                Time:   time.Now().Format("2006-01-02 15:04:05"),
                Task:   task, Host: host, Status: status, Output: out,
        })
        scheduleReportRefresh()
}

func statusEmoji(status string) string {
        switch status {
        case "OK":
                return "✅"
        case "FAIL":
                return "❌"
        case "WARN":
                return "⚠️"
        }
        return "•"
}

// statusMarkup — цветной статус для дерева журнала (Pango-markup):
// зелёный — успех, красный — ошибка, жёлтый — предупреждение.
func statusMarkup(status string) string {
        dc := detailColors()
        c := dc.text
        switch status {
        case "OK":
                c = dc.ok
        case "FAIL":
                c = dc.fail
        case "WARN":
                c = dc.warn
        }
        return fmt.Sprintf("<span foreground=\"%s\" weight=\"bold\">%s %s</span>", c, statusEmoji(status), status)
}

func truncateOut(s string, n int) string {
        s = strings.TrimSpace(s)
        if len(s) > n {
                return s[:n] + "…"
        }
        return s
}

