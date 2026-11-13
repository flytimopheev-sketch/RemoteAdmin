package main

// Журнал аудита: «кто, когда, что и на каких хостах запускал».
// Пишется в config/audit.jsonl (одна запись = одна строка JSON),
// просмотр — отдельная вкладка «Аудит» с фильтром и экспортом.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// AuditEntry — одна запись аудита.
type AuditEntry struct {
	Time    string   `json:"time"`                 // 2026-11-13 10:24:31
	User    string   `json:"user"`                 // локальный пользователь ОС
	Action  string   `json:"action"`               // «Быстрая команда», «Плейбук: имя»…
	Hosts   []string `json:"hosts"`                // затронутые хосты
	Root    int      `json:"root_tasks,omitempty"` // задач с повышенными правами
	Env     string   `json:"env,omitempty"`        // среды выбранных хостов
	OK      int      `json:"ok"`
	Fail    int      `json:"fail"`
	Warn    int      `json:"warn,omitempty"`
	Seconds float64  `json:"seconds,omitempty"` // длительность выполнения
	Details string   `json:"details,omitempty"` // короткая деталь (команда/файл)
}

var (
	auditMu   sync.Mutex
	auditFile string
)

// auditInit запоминает путь к файлу аудита (вызывается после initStateStore).
func auditInit() {
	if p := auditPath(); p != "" {
		auditFile = p
		_ = os.MkdirAll(filepath.Dir(p), 0700)
	}
}

// auditUser — имя локального пользователя ОС (кто нажал кнопку).
func auditUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "неизвестно"
}

// auditAppend добавляет запись в журнал аудита (O_APPEND — параллельные
// записи не конфликтуют). Записи только добавляются, не редактируются.
func auditAppend(e AuditEntry) {
	if auditFile == "" {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().Format("2006-01-02 15:04:05")
	}
	if e.User == "" {
		e.User = auditUser()
	}
	data, err := json.Marshal(&e)
	if err != nil {
		return
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	f, err := os.OpenFile(auditFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// auditReadAll читает все записи аудита (файл может отсутствовать).
func auditReadAll() []AuditEntry {
	if auditFile == "" {
		return nil
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	f, err := os.Open(auditFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// auditHostsSummary сжимает список хостов для показа: первые три + «+N».
func auditHostsSummary(hosts []string) string {
	if len(hosts) == 0 {
		return "—"
	}
	const max = 3
	names := make([]string, 0, max)
	for i, h := range hosts {
		if i >= max {
			break
		}
		if e := store.FindHost(h); e != nil {
			h = e.DisplayName()
		}
		names = append(names, h)
	}
	s := strings.Join(names, ", ")
	if rest := len(hosts) - max; rest > 0 {
		s += fmt.Sprintf(" +%d", rest)
	}
	return s
}

// auditEnvsOf собирает уникальные среды затронутых хостов («Продакшен, Тест»).
func auditEnvsOf(ips []string) string {
	seen := map[string]bool{}
	var out []string
	for _, ip := range ips {
		if h := store.FindHost(ip); h != nil && h.Env != "" && !seen[h.Env] {
			seen[h.Env] = true
			out = append(out, h.Env)
		}
	}
	return strings.Join(out, ", ")
}

// ---------- вкладка «Аудит» ----------

// audit store columns
const (
	ACOL_TIME  = 0
	ACOL_USER  = 1
	ACOL_ACT   = 2
	ACOL_HOSTS = 3
	ACOL_ENV   = 4
	ACOL_RES   = 5
	ACOL_DUR   = 6
)

// AuditUI — просмотр журнала аудита: таблица + фильтр + экспорт.
type AuditUI struct {
	widget  gtk.IWidget
	tree    *gtk.TreeView
	st      *gtk.ListStore
	filterE *gtk.Entry
	all     []AuditEntry
}

func newAuditUI() *AuditUI {
	u := &AuditUI{}
	card, box := cardBox("АУДИТ — кто, когда, что и на каких хостах запускал")

	top := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
	u.filterE = entryNew("Фильтр: пользователь, действие, хост, среда…")
	u.filterE.Connect("changed", func() { u.refresh() })
	btnCSV := buttonNew("Экспорт CSV")
	btnCSV.SetTooltipText("Сохранить журнал аудита в CSV-файл")
	btnCSV.Connect("clicked", func() { u.exportCSV() })
	btnWipe := buttonNew("Очистить")
	btnWipe.SetTooltipText("Удалить все записи аудита (подтверждается отдельно)")
	btnWipe.Connect("clicked", func() {
		if confirmDialog(app.window, "Удалить весь журнал аудита?\nФайл audit.jsonl будет очищен безвозвратно.") {
			auditMu.Lock()
			_ = os.WriteFile(auditFile, nil, 0600)
			auditMu.Unlock()
			u.refresh()
			logf("warn", "Журнал аудита очищен вручную")
		}
	})
	top.PackStart(u.filterE, true, true, 0)
	top.PackEnd(btnCSV, false, false, 0)
	top.PackEnd(btnWipe, false, false, 0)
	box.PackStart(top, false, false, 0)

	u.tree, u.st = makeTree(
		[]string{"Время", "Пользователь", "Действие", "Хосты", "Среда", "Итог", "Длит."},
		[]glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING},
	)
	sc, _ := gtk.ScrolledWindowNew(nil, nil)
	sc.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	sc.Add(u.tree)
	sc.SetVExpand(true)
	box.PackStart(sc, true, true, 0)

	hint := labelNew("Каждый запуск команд, плейбуков и массовых операций записывается в файл audit.jsonl рядом с настройками. Записи нельзя изменить — только добавить или очистить файл целиком.")
	addClass(hint, "ramuted")
	hint.SetLineWrap(true)
	box.PackStart(hint, false, false, 2)

	u.widget = card
	u.refresh()
	return u
}

// matchesFilter — проверка одной записи по фильтру (регистронезависимо).
func (u *AuditUI) matchesFilter(e AuditEntry, f string) bool {
	f = strings.ToLower(strings.TrimSpace(f))
	if f == "" {
		return true
	}
	hay := strings.ToLower(e.User + " " + e.Action + " " + strings.Join(e.Hosts, " ") + " " + e.Env)
	return strings.Contains(hay, f)
}

func (u *AuditUI) refresh() {
	if u.st == nil {
		return
	}
	u.all = auditReadAll()
	// новые записи сверху
	u.st.Clear()
	f := ""
	if u.filterE != nil {
		f, _ = u.filterE.GetText()
	}
	shown := 0
	for i := len(u.all) - 1; i >= 0; i-- {
		e := u.all[i]
		if !u.matchesFilter(e, f) {
			continue
		}
		res := fmt.Sprintf("ОК %d / ошиб. %d", e.OK, e.Fail)
		if e.Warn > 0 {
			res += fmt.Sprintf(" / пред. %d", e.Warn)
		}
		if e.Root > 0 {
			res += fmt.Sprintf(" · root:%d", e.Root)
		}
		dur := ""
		if e.Seconds >= 1 {
			dur = fmt.Sprintf("%.0f с", e.Seconds)
		} else if e.Seconds > 0 {
			dur = fmt.Sprintf("%.1f с", e.Seconds)
		}
		env := e.Env
		iter := u.st.Append()
		_ = u.st.Set(iter, []int{ACOL_TIME, ACOL_USER, ACOL_ACT, ACOL_HOSTS, ACOL_ENV, ACOL_RES, ACOL_DUR},
			[]interface{}{e.Time, e.User, e.Action, auditHostsSummary(e.Hosts), env, res, dur})
		shown++
	}
	if shown == 0 {
		iter := u.st.Append()
		_ = u.st.Set(iter, []int{ACOL_TIME, ACOL_USER, ACOL_ACT},
			[]interface{}{"—", "—", "Записей пока нет: выполните любую команду — она появится здесь"})
	}
}

func (u *AuditUI) exportCSV() {
	fn := fileChooser(app.window, "Сохранить аудит (CSV)", gtk.FILE_CHOOSER_ACTION_SAVE, "remoteadmin_audit.csv")
	if fn == "" {
		return
	}
	var b strings.Builder
	b.WriteString("Время;Пользователь;Действие;Хосты;Среда;Root-задач;ОК;Ошибок;Предупреждений;Секунды;Детали\r\n")
	for _, e := range auditReadAll() {
		row := []string{e.Time, e.User, e.Action, strings.Join(e.Hosts, " "), e.Env,
			fmt.Sprint(e.Root), fmt.Sprint(e.OK), fmt.Sprint(e.Fail), fmt.Sprint(e.Warn),
			fmt.Sprintf("%.1f", e.Seconds), e.Details}
		for i, c := range row {
			if i > 0 {
				b.WriteString(";")
			}
			b.WriteString(`"` + strings.ReplaceAll(c, `"`, `""`) + `"`)
		}
		b.WriteString("\r\n")
	}
	if err := os.WriteFile(fn, []byte(b.String()), 0600); err != nil {
		errDialog(app.window, "Не удалось сохранить: "+err.Error())
		return
	}
	logf("ok", "Аудит сохранён: %s", fn)
}

// sessionAuditStats считает итоги сессии выполнения для записи аудита.
func (s *RunSession) sessionAuditStats() (ok, fail, warn, root int) {
        s.mu.Lock()
        defer s.mu.Unlock()
        for _, r := range s.Results {
                switch r.Status {
                case StSuccess:
                        ok++
                case StError:
                        fail++
                case StCancelled:
                        warn++
                }
        }
        for _, t := range s.Tasks {
                if t.Root {
                        root++
                }
        }
        return
}
