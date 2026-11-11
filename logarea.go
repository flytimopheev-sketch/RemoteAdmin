package main

import (
	"fmt"

	"github.com/gotk3/gotk3/glib"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	view   *gtk.TextView
	buf    *gtk.TextBuffer
	widget gtk.IWidget
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
		buf.InsertWithTagByName(iter, "Журнал событий появится здесь.\n", "dim")
	}
	la := &LogArea{view: tv, buf: buf}
	la.widget = tv
	return la, nil
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
	out := output
	if len(out) > 4000 {
		out = out[:4000] + "\n… (обрезано)"
	}
	store.AddReport(ReportEntry{
		Time: time.Now().Format("2006-01-02 15:04:05"),
		Task: task, Host: host, Status: status, Output: out,
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

func truncateOut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

var _ = filepath.Join // keep import in case of future use
