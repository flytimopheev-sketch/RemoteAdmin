package main

import (
        "errors"
        "fmt"
        "sort"
        "strconv"
        "strings"
        "time"
)

// ---------- планировщик задач (cron внутри приложения) ----------
//
// Каждое расписание = шаблон задач + группа хостов + cron-выражение.
// Галка каждые 20 секунд проверяет: пора ли запускать? Если да — шаблон
// выполняется на хостах группы через обычный движок плейбуков (с отчётом
// в журнал, уведомлениями и webhook'ом).

type Schedule struct {
        Name     string `json:"name"`
        Template string `json:"template"`     // имя шаблона из библиотеки
        Group    string `json:"group"`        // группа хостов или «Все хосты»
        Cron     string `json:"cron"`         // "*/15 * * * *" (5 полей)
        Enabled  bool   `json:"enabled"`
        LastRun  string `json:"last_run,omitempty"`
}

// ---------- парсер cron ----------

type cronField struct {
        min, max int
}

var cronFields = []cronField{
        {0, 59},  // минута
        {0, 23},  // час
        {1, 31},  // день месяца
        {1, 12},  // месяц
        {0, 6},   // день недели (0=вс, как в cron)
}

const cronWeekdaysRu = "пн вт ср чт пт сб вс"

// parseCron разбирает 5-поле cron. Возвращает 5 наборов допустимых значений.
func parseCron(expr string) ([][]int, error) {
        fields := strings.Fields(strings.TrimSpace(expr))
        if len(fields) != 5 {
                return nil, fmt.Errorf("нужно 5 полей (мин час день мес день_недели), получено %d", len(fields))
        }
        out := make([][]int, 5)
        for i, f := range fields {
                vals, err := cronParseField(f, cronFields[i].min, cronFields[i].max, i == 4)
                if err != nil {
                        return nil, fmt.Errorf("поле %d («%s»): %v", i+1, f, err)
                }
                if len(vals) == 0 {
                        return nil, fmt.Errorf("поле %d («%s») не даёт ни одного значения", i+1, f)
                }
                out[i] = vals
        }
        return out, nil
}

func cronParseField(f string, min, max int, dow bool) ([]int, error) {
        var set []int
        for _, part := range strings.Split(f, ",") {
                part = strings.TrimSpace(part)
                if part == "" {
                        return nil, errors.New("пустой элемент списка")
                }
                // имена дней недели пн/вт/...
                if dow {
                        for i, name := range strings.Fields(cronWeekdaysRu) {
                                if strings.EqualFold(part, name) || strings.EqualFold(part, name+"-"+name) {
                                        part = strconv.Itoa((i + 1) % 7)
                                }
                        }
                }
                step := 1
                range_ := part
                if i := strings.Index(part, "/"); i >= 0 {
                        range_ = part[:i]
                        s, err := strconv.Atoi(part[i+1:])
                        if err != nil || s <= 0 {
                                return nil, errors.New("неверный шаг")
                        }
                        step = s
                }
                lo, hi := min, max
                switch {
                case range_ == "*" || range_ == "?":
                	// весь диапазон
                case step == 1 && !strings.Contains(range_, "-"):
                	// одиночное значение: ровно оно («3» для часов = 03:00, не 3..23)
                	v, e := cronNum(range_, min, max)
                	if e != nil {
                		return nil, e
                	}
                	lo, hi = v, v
                default:
                	var e error
                	lo, hi, e = cronRange(range_, min, max)
                	if e != nil {
                		return nil, e
                	}
                }
                if lo > hi {
                        return nil, fmt.Errorf("начало %d больше конца %d", lo, hi)
                }
                for v := lo; v <= hi; v += step {
                        set = append(set, v)
                }
        }
        sort.Ints(set)
        // dedup
        var out []int
        for i, v := range set {
                if i == 0 || v != set[i-1] {
                        out = append(out, v)
                }
        }
        return out, nil
}

func cronRange(s string, min, max int) (int, int, error) {
        parts := strings.SplitN(s, "-", 2)
        if len(parts) == 2 {
                lo, e1 := cronNum(parts[0], min, max)
                hi, e2 := cronNum(parts[1], min, max)
                if e1 != nil || e2 != nil {
                        return 0, 0, errors.New("неверный диапазон")
                }
                return lo, hi, nil
        }
        // одиночное значение: */N → старт от min; N/N → от N
        v, err := cronNum(s, min, max)
        if err != nil {
                return 0, 0, err
        }
        return v, max, nil
}

func cronNum(s string, min, max int) (int, error) {
        v, err := strconv.Atoi(strings.TrimSpace(s))
        if err != nil {
                return 0, fmt.Errorf("«%s» не число", s)
        }
        if v < min || v > max {
                return 0, fmt.Errorf("значение %d вне диапазона %d–%d", v, min, max)
        }
        return v, nil
}

// cronNext вычисляет следующий момент запуска строго после from.
func cronNext(expr string, from time.Time) (time.Time, error) {
        f, err := parseCron(expr)
        if err != nil {
                return time.Time{}, err
        }
        t := from.Truncate(time.Minute).Add(time.Minute)
        limit := t.AddDate(0, 0, 366*2)
        for t.Before(limit) {
                if !containsInt(f[3], int(t.Month())) {
                        t = t.AddDate(0, 0, 1)
                        t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
                        continue
                }
                if !containsInt(f[4], int(t.Weekday())) || !containsInt(f[2], t.Day()) {
                        t = t.AddDate(0, 0, 1)
                        t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
                        continue
                }
                if !containsInt(f[1], t.Hour()) {
                        t = t.Add(time.Hour)
                        t = t.Truncate(time.Hour)
                        continue
                }
                if !containsInt(f[0], t.Minute()) {
                        t = t.Add(time.Minute)
                        continue
                }
                return t, nil
        }
        return time.Time{}, errors.New("не удалось найти время запуска за 2 года")
}

func containsInt(list []int, v int) bool {
        for _, x := range list {
                if x == v {
                        return true
                }
        }
        return false
}

// cronHuman — человекочитаемое описание выражения.
func cronHuman(expr string) string {
	f := strings.Fields(strings.TrimSpace(expr))
	if len(f) != 5 {
		return "неверное выражение"
	}
	var parts []string
	switch {
	case f[0] == "*" && f[1] == "*":
		parts = append(parts, "каждую минуту")
	case strings.HasPrefix(f[0], "*/") && f[1] == "*":
		parts = append(parts, "каждые "+strings.TrimPrefix(f[0], "*/")+" минут")
	case f[1] == "*":
		parts = append(parts, "ежечасно на "+f[0]+"-й минуте")
	default:
		parts = append(parts, fmt.Sprintf("в %s:%s", pad2(f[1]), pad2(f[0])))
	}
	dow := func(s string) string {
		// заменяем 1..7 на имена дней недели, остальное оставляем как есть
		fields := strings.Split(s, ",")
		var out []string
		for _, fld := range fields {
			known := map[string]string{"0": "вс", "1": "пн", "2": "вт", "3": "ср", "4": "чт", "5": "пт", "6": "сб", "7": "вс"}
			if nm, ok := known[fld]; ok {
				out = append(out, nm)
			} else {
				out = append(out, fld)
			}
		}
		return strings.Join(out, ",")
	}
	if f[2] != "*" && f[4] != "*" {
		parts = append(parts, fmt.Sprintf("в %s-е числа месяца и по дням (%s)", f[2], dow(f[4])))
	} else if f[2] != "*" {
		parts = append(parts, fmt.Sprintf("в %s-е числа месяца", f[2]))
	} else if f[4] != "*" {
		parts = append(parts, fmt.Sprintf("по дням недели (%s)", dow(f[4])))
	}
	return strings.Join(parts, ", ")
}

func pad2(s string) string {
        v := atoiSafe(s)
        return fmt.Sprintf("%02d", v)
}

// ---------- движок расписаний ----------

// scheduleDue проверяет: пора ли запускать (последний запуск был до
// вычисленного предыдущего срабатывания).
func scheduleDue(s Schedule, now time.Time) bool {
        if !s.Enabled {
                return false
        }
        next, err := cronNext(s.Cron, now)
        if err != nil {
                return false
        }
        // момент «предыдущего» срабатывания = next - период нельзя вычислить
        // напрямую; вместо этого: запускать, если next <= now+проверочный
        // интервал и после LastRun прошло достаточно
        if next.After(now.Add(21 * time.Second)) {
                return false
        }
        if s.LastRun != "" {
                last, e := time.ParseInLocation("2006-01-02 15:04:05", s.LastRun, time.Local)
                if e == nil && next.Truncate(time.Minute).Before(last.Add(30*time.Second)) {
                        return false // это срабатывание уже выполнялось
                }
        }
        return true
}

// schedulerTick вызывается по таймеру каждые 20 секунд.
func schedulerTick() {
        now := time.Now()
        changed := false
        for i := range store.State.Schedules {
                s := &store.State.Schedules[i]
                if scheduleDue(*s, now) {
                        stamp := now.Format("2006-01-02 15:04:05")
                        s.LastRun = stamp
                        changed = true
                        go runScheduled(*s, now)
                }
        }
        if changed {
                if u := app.schedUI; u != nil {
                        u.refresh()
                }
                _ = store.Save()
        }
}

// runScheduled выполняет шаблон на группе хостов.
func runScheduled(s Schedule, when time.Time) {
        tpl := store.TemplateByName(s.Template)
        if tpl == nil {
                logf("fail", "⏰ Расписание «%s»: шаблон «%s» не найден", s.Name, s.Template)
                return
        }
        var ips []string
        for _, h := range store.State.Hosts {
                if s.Group == "Все хосты" || h.Group == s.Group {
                        ips = append(ips, h.IP)
                }
        }
        if len(ips) == 0 {
                logf("warn", "⏰ Расписание «%s»: в группе «%s» нет хостов", s.Name, s.Group)
                return
        }
        if store.State.Username == "" || (store.Password() == "" && store.State.KeyPath == "") {
                logf("fail", "⏰ Расписание «%s»: не заданы доступы SSH (логин/пароль)", s.Name)
                return
        }
        logf("accent", "⏰ Расписание «%s» запускает «%s» на %d хост(ов) [%s]", s.Name, s.Template, len(ips), s.Cron)
        runPlaybookOnHosts(ips, tpl.Tasks, "⏰ "+s.Name+": "+s.Template)
}

// nextRunText — строка «следующий запуск» для таблицы.
func nextRunText(s Schedule) string {
        if !s.Enabled {
                return "— (выключено)"
        }
        t, err := cronNext(s.Cron, time.Now())
        if err != nil {
                return "ошибка: " + err.Error()
        }
        return t.Format("02.01 15:04")
}

// startScheduler — таймер на 20 с.
func startScheduler() {
        glibTimeout(20, func() bool {
                defer func() {
                        if r := recover(); r != nil {
                                logf("fail", "scheduler panic: %v", r)
                        }
                }()
                schedulerTick()
                return true
        })
}
