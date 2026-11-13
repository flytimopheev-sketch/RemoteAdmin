package main

// runner.go — исполняющий движок плейбуков (v1.9.0).
//
// Возможности (запросы «управления выполнения»):
//   • очередь задач на каждый хост + пул воркеров с лимитом параллелизма;
//   • таймаут отдельной задачи (TimeoutSec) и всего запуска (RunTimeoutSec);
//   • политика ошибки: остановиться / продолжить / спросить;
//   • авто-повторы с задержкой — только при AllowRetry («Разрешён повторный запуск»);
//   • условия запуска шага: always / on_success / on_failure / rc:N;
//   • раздельные stdout/stderr и код возврата на каждую пару «хост–задача»;
//   • остановка с выбором: очередь / текущий процесс / оба;
//   • история запусков в ~/.config/RemoteAdmin/runs/*.json.
//
// Переменные {{ var }} (глобальные и по хостам) и секреты {{ secret:имя }}
// подставляются через существующие expandTaskVars/expandSecretRefs.

import (
        "encoding/json"
        "fmt"
        "os"
        "path/filepath"
        "regexp"
        "sort"
        "strconv"
        "strings"
        "sync"
        "sync/atomic"
        "time"

        "golang.org/x/crypto/ssh"
)

// Статусы словами.
const (
        StQueued    = "В очереди"
        StRunning   = "Выполняется"
        StSuccess   = "Успешно"
        StError     = "Ошибка"
        StCancelled = "Отменено"
        StSkipped   = "Пропущено"
)

// TaskResult — результат пары «хост — задача».
type TaskResult struct {
        Host     string    `json:"host"`
        TaskIdx  int       `json:"task"`
        TaskName string    `json:"task_name"`
        Status   string    `json:"status"`
        Start    time.Time `json:"start,omitempty"`
        End      time.Time `json:"end,omitempty"`
        DurMs    int64     `json:"dur_ms,omitempty"`
        RC       int       `json:"rc,omitempty"`
        Err      string    `json:"err,omitempty"`
        Stdout   string    `json:"stdout,omitempty"`
        Stderr   string    `json:"stderr,omitempty"`
        Attempt  int       `json:"attempt,omitempty"`
}

// RunSession — один запуск плейбука.
type RunSession struct {
        ID       string    `json:"id"`
        Started  time.Time `json:"started"`
        Finished time.Time `json:"finished,omitempty"`
        Label    string    `json:"label"`
        User     string    `json:"user,omitempty"`
        RootMode string    `json:"root_mode,omitempty"`
        Hosts    []string  `json:"hosts"`
        Tasks    []PlaybookTask `json:"tasks"`
        Parallel int       `json:"parallel,omitempty"`
        RunTimeoutSec int   `json:"run_timeout_sec,omitempty"`
        ErrPolicy     string `json:"err_policy,omitempty"`
        Results  []TaskResult `json:"results"`

        cancelFlag atomic.Bool
        killFlag   atomic.Bool
        mu         sync.Mutex
        waitAll    sync.WaitGroup
        vars       map[string]string // значения из формы перед запуском (сессионные)
}

// curSession — сессия, выполняющаяся прямо сейчас.
var curSession atomic.Pointer[RunSession]

// ---------- реестр активных сессий («остановить текущий процесс») ----------

type stopRegistry struct {
        mu      sync.Mutex
        closers map[string][]ioCloser
}

type ioCloser interface{ Close() error }

var stops = &stopRegistry{closers: map[string][]ioCloser{}}

func regSession(host string, s *ssh.Session) {
        stops.add(host, s)
}

func unregSession(host string, s *ssh.Session) {
        stops.remove(host, s)
}

func (r *stopRegistry) add(host string, c ioCloser) {
        r.mu.Lock()
        r.closers[host] = append(r.closers[host], c)
        r.mu.Unlock()
}

func (r *stopRegistry) remove(host string, c ioCloser) {
        r.mu.Lock()
        out := r.closers[host][:0]
        for _, x := range r.closers[host] {
                if x != c {
                        out = append(out, x)
                }
        }
        r.closers[host] = out
        r.mu.Unlock()
}

// killAll закрывает активные SSH-сессии — текущие команды завершаются.
func (r *stopRegistry) killAll() int {
        r.mu.Lock()
        n := 0
        for _, cs := range r.closers {
                for _, c := range cs {
                        _ = c.Close()
                        n++
                }
        }
        r.closers = map[string][]ioCloser{}
        r.mu.Unlock()
        return n
}

// ---------- раздельный вывод ----------

type execOut struct {
        Stdout string
        Stderr string
        RC     int
        Err    error
}

// execSeparate — команда с раздельным stdout/stderr и кодом возврата.
// Сессия регистрируется в stops: «Остановить текущий процесс» закроет
// именно активную команду, не убивая SSH-подключение целиком.
func execSeparate(cl *ssh.Client, hostKey, cmd string, timeoutSec int) execOut {
        type res struct {
                out execOut
        }
        ch := make(chan res, 1)
        sess, err := cl.NewSession()
        if err != nil {
                return execOut{RC: 1, Err: err}
        }
        regSession(hostKey, sess)
        defer unregSession(hostKey, sess)
        defer sess.Close()
        var so, se syncBuffer
        sess.Stdout = &so
        sess.Stderr = &se
        go func() {
                err := sess.Run(cmd)
                rc := 0
                if err != nil {
                        if ee, ok := err.(*ssh.ExitError); ok {
                                rc = ee.ExitStatus()
                        } else {
                                rc = 1
                        }
                }
                ch <- res{execOut{Stdout: so.String(), Stderr: se.String(), RC: rc, Err: err}}
        }()
        timeout := time.Duration(timeoutSec) * time.Second
        if timeoutSec <= 0 {
                timeout = 120 * time.Second
        }
        select {
        case r := <-ch:
                return r.out
        case <-time.After(timeout):
                sess.Close()
                return execOut{Stdout: so.String(), Stderr: se.String(), RC: -1, Err: fmt.Errorf("таймаут команды (%d с)", timeoutSec)}
        }
}

// execRootSeparate — команда через su - root с раздельным выводом.
func execRootSeparate(cl *ssh.Client, hostKey, cmd, rootPass string, timeoutSec int) execOut {
        sess, err := cl.NewSession()
        if err != nil {
                return execOut{RC: 1, Err: err}
        }
        regSession(hostKey, sess)
        defer unregSession(hostKey, sess)
        defer sess.Close()
        quoted := shellQuote(cmd)
        sess.Stdin = strings.NewReader(rootPass + "\n")
        var so, se syncBuffer
        sess.Stdout = &so
        sess.Stderr = &se
        done := make(chan error, 1)
        go func() { done <- sess.Run("su - root -c " + quoted) }()
        timeout := time.Duration(timeoutSec) * time.Second
        if timeoutSec <= 0 {
                timeout = 120 * time.Second
        }
        select {
        case err := <-done:
                rc := 0
                if err != nil {
                        if ee, ok := err.(*ssh.ExitError); ok {
                                rc = ee.ExitStatus()
                        } else {
                                rc = 1
                        }
                }
                return execOut{Stdout: so.String(), Stderr: se.String(), RC: rc, Err: err}
        case <-time.After(timeout):
                sess.Close()
                return execOut{Stdout: so.String(), Stderr: se.String(), RC: -1, Err: fmt.Errorf("таймаут команды (root) (%d с)", timeoutSec)}
        }
}

// ---------- обратные вызовы в панель ----------

var runnerHooks struct {
        onResult   func(res TaskResult)
        onHostDone func(host string, status string)
        onRunStart func(s *RunSession)
        onRunEnd   func(s *RunSession)
        askPolicy  func(s *RunSession, host string, t PlaybookTask, res TaskResult) bool
}

// ---------- сборка сессии ----------

// buildRunSession создаёт сессию из текущих настроек плейбука.
func buildRunSession(ips []string, tasks []PlaybookTask, label string, vars map[string]string) *RunSession {
        s := &RunSession{
                ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
                Started: time.Now(),
                Label:   label,
                User:    store.State.Username,
                Hosts:   append([]string(nil), ips...),
                Tasks:   append([]PlaybookTask(nil), tasks...),
                vars:    vars,
        }
        s.Parallel = store.State.effectiveParallel()
        s.RunTimeoutSec = store.State.PlaybookRunTimeoutSec
        s.ErrPolicy = store.State.effectiveErrPolicy()
        if s.ErrPolicy == "" {
                s.ErrPolicy = "stop"
        }
        for _, t := range tasks {
                if t.Root {
                        s.RootMode = "su (root)"
                        break
                }
        }
        for _, ip := range ips {
                for i, t := range tasks {
                        s.Results = append(s.Results, TaskResult{
                                Host: ip, TaskIdx: i, TaskName: t.StepTitle(), Status: StQueued, Attempt: 1,
                        })
                }
        }
        return s
}

// runSession выполняет запуск; блокируется до конца. true = остановлено вручную.
func (s *RunSession) runSession() bool {
        if runnerHooks.onRunStart != nil {
                runnerHooks.onRunStart(s)
        }
        curSession.Store(s)
        defer curSession.Store(nil)

        if s.RunTimeoutSec > 0 {
                go func() {
                        time.Sleep(time.Duration(s.RunTimeoutSec) * time.Second)
                        // общий таймаут истёк — если запуск ещё идёт, останавливаем всё
                        if curSession.Load() == s {
                                s.cancelFlag.Store(true)
                                s.killFlag.Store(true)
                                stops.killAll()
                        }
                }()
        }

        for _, ip := range s.Hosts {
                app.hostsUI.updateStatusRow(ip, "run")
        }

        sem := make(chan struct{}, s.Parallel)
        for _, ip := range s.Hosts {
                s.waitAll.Add(1)
                go func(ip string) {
                        defer s.waitAll.Done()
                        sem <- struct{}{}
                        defer func() { <-sem }()
                        s.runHost(ip)
                }(ip)
        }
        s.waitAll.Wait()

        s.Finished = time.Now()
        cancelled := s.cancelFlag.Load()
        if cancelled {
                s.mu.Lock()
                for i := range s.Results {
                        if s.Results[i].Status == StQueued || s.Results[i].Status == StRunning {
                                s.Results[i].Status = StCancelled
                        }
                }
                s.mu.Unlock()
        }
        if runnerHooks.onRunEnd != nil {
                runnerHooks.onRunEnd(s)
        }
        saveRunHistory(s)
        return cancelled
}

// runHost выполняет очередь задач на одном хосте.
func (s *RunSession) runHost(ip string) {
        defer func() {
                if runnerHooks.onHostDone != nil {
                        st := "ok"
                        if s.hostFailed(ip) {
                                st = "fail"
                        }
                        runnerHooks.onHostDone(ip, st)
                }
        }()
        if s.cancelFlag.Load() {
                return
        }
        creds := buildCreds(ip)
        cl, err := Connect(creds)
        if err != nil {
                for i := range s.Tasks {
                        s.setResult(ip, i, TaskResult{Host: ip, TaskIdx: i, TaskName: s.Tasks[i].StepTitle(), Status: StError, End: time.Now(), RC: 1, Err: "подключение: " + err.Error(), Attempt: 1})
                }
                return
        }
        defer cl.Close()

        var prevErr bool
        var prevRC int

        for i, raw := range s.Tasks {
                if s.cancelFlag.Load() {
                        return
                }
                // условие запуска шага (всегда / после успеха / после ошибки / rc:N)
                if !runIfAllows(raw.RunIf, i > 0 && prevErr, prevRC) {
                        s.setResult(ip, i, TaskResult{Host: ip, TaskIdx: i, TaskName: raw.StepTitle(), Status: StSkipped, End: time.Now(), Attempt: 1})
                        continue
                }
                st := time.Now()
                s.setResult(ip, i, TaskResult{Host: ip, TaskIdx: i, TaskName: raw.StepTitle(), Status: StRunning, Start: st, Attempt: 1})

                res := s.execTaskWithRetries(cl, ip, raw, creds)
                res.Host = ip
                res.TaskIdx = i
                res.TaskName = raw.StepTitle()
                res.End = time.Now()
                res.DurMs = time.Since(st).Milliseconds()
                res.Stdout = maskOutput(res.Stdout)
                res.Stderr = maskOutput(res.Stderr)
                res.Err = maskOutput(res.Err)
                s.setResult(ip, i, res)

                if res.Status == StError || res.Status == StCancelled {
                        prevErr = true
                        prevRC = res.RC
                        switch s.ErrPolicy {
                        case "continue":
                                continue
                        case "ask":
                                if runnerHooks.askPolicy != nil && runnerHooks.askPolicy(s, ip, raw, res) {
                                        continue
                                }
                                s.cancelFlag.Store(true)
                                return
                        default: // stop
                                s.cancelFlag.Store(true)
                                return
                        }
                } else {
                        prevErr = false
                        prevRC = res.RC
                }
        }
}

// hostFailed — был ли ошибочный шаг на хосте.
func (s *RunSession) hostFailed(ip string) bool {
        s.mu.Lock()
        defer s.mu.Unlock()
        for _, r := range s.Results {
                if r.Host == ip && r.Status == StError {
                        return true
                }
        }
        return false
}

// execTaskWithRetries — выполнение с авто-повторами (если разрешены задачей).
func (s *RunSession) execTaskWithRetries(cl *ssh.Client, ip string, raw PlaybookTask, creds SSHCreds) TaskResult {
        attempt := 0
        for {
                attempt++
                res := s.execTaskOnce(cl, ip, raw, creds)
                res.Attempt = attempt
                if res.Status != StError {
                        return res
                }
                if !raw.AllowRetry || attempt > raw.RetryCount || s.cancelFlag.Load() {
                        return res
                }
                delay := raw.RetryDelaySec
                if delay <= 0 {
                        delay = 3
                }
                logf("warn", "↻ %s: повтор %d/%d через %d с — %s", ip, attempt, raw.RetryCount, delay, truncateOut(res.Err, 90))
                deadline := time.Now().Add(time.Duration(delay) * time.Second)
                for time.Now().Before(deadline) {
                        if s.cancelFlag.Load() {
                                res.Status = StCancelled
                                res.Err = "остановлено во время ожидания повтора"
                                return res
                        }
                        time.Sleep(200 * time.Millisecond)
                }
        }
}

// execTaskOnce — одно выполнение задачи (адаптация execTask v1.8.0
// на раздельный вывод, переменные сессии и таймаут задачи).
func (s *RunSession) execTaskOnce(cl *ssh.Client, ip string, raw PlaybookTask, creds SSHCreds) TaskResult {
        // подстановка {{ var }} (глобальные + по хостам) и {{ secret:имя }}
        t := store.expandTaskVars(ip, raw)
        if len(s.vars) > 0 {
                t = applySessionVars(t, s.vars)
        }
        timeout := t.TimeoutSec
        if timeout <= 0 {
                timeout = store.State.Settings.ExecTimeout
        }
        root := t.Root || (store.State.UseRoot && creds.User != "root")
        run := func(cmd string) execOut {
                if root && creds.User != "root" {
                        if creds.RootPass == "" {
                                return execOut{RC: 1, Err: fmt.Errorf("нужен пароль root (su)")}
                        }
                        return execRootSeparate(cl, ip, cmd, creds.RootPass, timeout)
                }
                return execSeparate(cl, ip, cmd, timeout)
        }

        mk := func(o execOut) TaskResult {
                r := TaskResult{RC: o.RC, Stdout: o.Stdout, Stderr: o.Stderr}
                if o.Err != nil {
                        r.Status = StError
                        r.Err = o.Err.Error()
                } else {
                        r.Status = StSuccess
                }
                return r
        }

        switch t.Type {
        case "command":
                return mk(run(t.Source))

        case "script":
                content, err := os.ReadFile(t.Source)
                if err != nil {
                        return TaskResult{Status: StError, RC: 1, Err: "скрипт недоступен: " + err.Error()}
                }
                expanded := store.expandVars(ip, string(content))
                tmpLocal := filepath.Join(os.TempDir(), fmt.Sprintf(".ra_scr_%d_%d.sh", os.Getpid(), time.Now().UnixNano()%100000))
                if err := os.WriteFile(tmpLocal, []byte(expanded), 0755); err != nil {
                        return TaskResult{Status: StError, RC: 1, Err: err.Error()}
                }
                defer os.Remove(tmpLocal)
                tmp := fmt.Sprintf("/tmp/.ra_pb_%d.sh", nowMs())
                if err := uploadThenSuRoot(cl, tmpLocal, tmp, rootPassFor(creds, root), timeout); err != nil {
                        return TaskResult{Status: StError, RC: 1, Err: err.Error()}
                }
                defer Exec(cl, "rm -f "+shellQuote(tmp), 15)
                return mk(run("sh " + shellQuote(tmp)))

        case "copy":
                if root && creds.User != "root" {
                        if err := uploadThenSuRoot(cl, t.Source, t.Dest, creds.RootPass, timeout); err != nil {
                                return TaskResult{Status: StError, RC: 1, Err: err.Error()}
                        }
                        o := run("chmod 644 " + shellQuote(t.Dest))
                        if o.Err != nil {
                                return mk(o)
                        }
                        return TaskResult{Status: StSuccess, RC: 0, Stdout: "скопировано: " + t.Dest}
                }
                if err := scpUpload(cl, t.Source, t.Dest); err != nil {
                        return TaskResult{Status: StError, RC: 1, Err: err.Error()}
                }
                return TaskResult{Status: StSuccess, RC: 0, Stdout: "скопировано: " + t.Dest}

        case "replace", "replace_with_cmds":
                bak := fmt.Sprintf("%s.ra_bak_%d", t.Dest, nowMs())
                if o := run(fmt.Sprintf("cp -f %s %s 2>/dev/null; true", shellQuote(t.Dest), shellQuote(bak))); o.Err != nil {
                        return mk(o)
                }
                if root && creds.User != "root" {
                        if err := uploadThenSuRoot(cl, t.Source, t.Dest, creds.RootPass, timeout); err != nil {
                                return TaskResult{Status: StError, RC: 1, Err: err.Error()}
                        }
                } else {
                        if err := scpUpload(cl, t.Source, t.Dest); err != nil {
                                return TaskResult{Status: StError, RC: 1, Err: err.Error()}
                        }
                }
                r := TaskResult{Status: StSuccess, RC: 0, Stdout: "файл заменён (backup: " + bak + ")"}
                if t.Type == "replace_with_cmds" {
                        for _, c := range t.PostCommands {
                                o := run(c)
                                r.Stdout += fmt.Sprintf("\n$ %s\n%s", c, strings.TrimSpace(o.Stdout+o.Stderr))
                                if o.Err != nil {
                                        r.Status = StError
                                        r.Err = "пост-команда: " + o.Err.Error()
                                        break
                                }
                        }
                }
                return r

        case "edit":
                if strings.TrimSpace(t.Search) == "" {
                        return TaskResult{Status: StError, RC: 1, Err: "пустой текст поиска"}
                }
                sed := fmt.Sprintf("sed -i 's|%s|%s|g' %s", sedEscape(t.Search), sedEscape(t.Replace), shellQuote(t.Source))
                o := run(sed)
                r := mk(o)
                if t.RestartService != "" {
                        ro := run("systemctl restart " + shellQuote(t.RestartService))
                        r.Stdout += "\n" + ro.Stdout + ro.Stderr
                        if ro.Err != nil {
                                r.Status = StError
                                r.Err = "restart " + t.RestartService + ": " + ro.Err.Error()
                        } else {
                                r.Stdout += "\nслужба " + t.RestartService + " перезапущена"
                        }
                }
                return r

        case "service":
                if strings.TrimSpace(t.Source) == "" {
                        return TaskResult{Status: StError, RC: 1, Err: "укажите имя службы"}
                }
                act := t.Action
                if act == "" {
                        act = "restart"
                }
                return mk(run("systemctl " + act + " " + shellQuote(t.Source)))

        case "package":
                if strings.TrimSpace(t.Source) == "" {
                        return TaskResult{Status: StError, RC: 1, Err: "укажите пакеты"}
                }
                act := t.Action
                if act == "" {
                        act = "install"
                }
                pkgs := strings.Fields(strings.ReplaceAll(t.Source, ",", " "))
                quoted := make([]string, len(pkgs))
                for i, p := range pkgs {
                        quoted[i] = shellQuote(p)
                }
                pkgList := strings.Join(quoted, " ")
                mgrOut := run("(command -v dnf >/dev/null 2>&1 && echo dnf) || (command -v yum >/dev/null 2>&1 && echo yum) || (command -v apt-get >/dev/null 2>&1 && echo apt-get) || echo dnf")
                mgr := strings.TrimSpace(mgrOut.Stdout)
                if mgr == "" {
                        mgr = "dnf"
                }
                var cmd string
                switch {
                case mgr == "apt-get" && act == "remove":
                        cmd = "DEBIAN_FRONTEND=noninteractive apt-get -y remove " + pkgList
                case mgr == "apt-get" && act == "upgrade":
                        cmd = "DEBIAN_FRONTEND=noninteractive apt-get -y upgrade"
                case mgr == "apt-get" && act == "info":
                        cmd = "apt-cache show " + pkgList + " 2>/dev/null | head -20"
                case mgr == "apt-get":
                        cmd = "DEBIAN_FRONTEND=noninteractive apt-get -y install " + pkgList
                case act == "remove":
                        cmd = mgr + " -y remove " + pkgList
                case act == "upgrade":
                        cmd = mgr + " -y upgrade"
                case act == "info":
                        cmd = mgr + " info " + pkgList
                default:
                        cmd = mgr + " -y install " + pkgList
                }
                return mk(run(cmd))
        }
        return TaskResult{Status: StError, RC: 1, Err: "неизвестный тип задачи: " + t.Type}
}

// setResult обновляет результат пары «хост–задача» и дёргает хук панели.
func (s *RunSession) setResult(host string, idx int, r TaskResult) {
        s.mu.Lock()
        for i := range s.Results {
                if s.Results[i].Host == host && s.Results[i].TaskIdx == idx {
                        if r.Start.IsZero() {
                                r.Start = s.Results[i].Start
                        }
                        s.Results[i] = r
                        s.mu.Unlock()
                        if runnerHooks.onResult != nil {
                                res := r
                                go runnerHooks.onResult(res)
                        }
                        return
                }
        }
        s.Results = append(s.Results, r)
        s.mu.Unlock()
}

// applySessionVars — подстановка сессионных значений поверх сохранённых.
func applySessionVars(t PlaybookTask, vars map[string]string) PlaybookTask {
        repl := func(x string) string {
                for name, val := range vars {
                        x = strings.ReplaceAll(x, "{{"+name+"}}", val)
                        x = strings.ReplaceAll(x, "{{ "+name+" }}", val)
                }
                return x
        }
        t.Source = repl(t.Source)
        t.Action = repl(t.Action)
        t.Dest = repl(t.Dest)
        t.Search = repl(t.Search)
        t.Replace = repl(t.Replace)
        t.RestartService = repl(t.RestartService)
        for i := range t.PostCommands {
                t.PostCommands[i] = repl(t.PostCommands[i])
        }
        return t
}

// maskOutput — секреты + пароли никогда не попадают в панель/историю.
func maskOutput(text string) string {
        text = redactSecrets(text)
        if rp := store.RootPassword(); rp != "" {
                text = strings.ReplaceAll(text, rp, "••••••")
        }
        if pw := store.Password(); pw != "" {
                text = strings.ReplaceAll(text, pw, "••••••")
        }
        return text
}

// ---------- остановка ----------

func (s *RunSession) stopQueue() {
        s.cancelFlag.Store(true)
}

func (s *RunSession) stopCurrent() {
        s.killFlag.Store(true)
        n := stops.killAll()
        logf("warn", "⏹ Остановлено текущих процессов: %d", n)
}

func (s *RunSession) stopBoth() {
        s.stopCurrent()
        s.stopQueue()
}

// ---------- счётчики ----------

func countsOf(results []TaskResult) (ok, fail, cancel, skipped int) {
        for _, r := range results {
                switch r.Status {
                case StSuccess:
                        ok++
                case StError:
                        fail++
                case StCancelled:
                        cancel++
                case StSkipped:
                        skipped++
                }
        }
        return
}

func (s *RunSession) counts() (ok, fail, cancel, skipped int) {
        s.mu.Lock()
        defer s.mu.Unlock()
        return countsOf(s.Results)
}

func (s *RunSession) hostsDone() (done, total int) {
        total = len(s.Hosts)
        s.mu.Lock()
        defer s.mu.Unlock()
        for _, ip := range s.Hosts {
                busy := false
                for _, r := range s.Results {
                        if r.Host == ip && (r.Status == StQueued || r.Status == StRunning) {
                                busy = true
                                break
                        }
                }
                if !busy {
                        done++
                }
        }
        return
}

// ---------- история запусков ----------

const (
        runsKeepMax   = 100
        outputClipMax = 64 * 1024
)

func runsDir() string {
        dir, err := configDir()
        if err != nil {
                return "/tmp/remoteadmin_runs"
        }
        return filepath.Join(dir, "runs")
}

// RunRecord — чистая (без атомиков) копия сессии для сохранения в JSON.
type RunRecord struct {
        ID            string         `json:"id"`
        Started       time.Time      `json:"started"`
        Finished      time.Time      `json:"finished,omitempty"`
        Label         string         `json:"label"`
        User          string         `json:"user,omitempty"`
        RootMode      string         `json:"root_mode,omitempty"`
        Hosts         []string       `json:"hosts"`
        Tasks         []PlaybookTask `json:"tasks"`
        Parallel      int            `json:"parallel,omitempty"`
        RunTimeoutSec int            `json:"run_timeout_sec,omitempty"`
        ErrPolicy     string         `json:"err_policy,omitempty"`
        Results       []TaskResult   `json:"results"`
}

func (s *RunSession) toRecord() RunRecord {
        s.mu.Lock()
        results := append([]TaskResult(nil), s.Results...)
        s.mu.Unlock()
        return RunRecord{
                ID: s.ID, Started: s.Started, Finished: s.Finished,
                Label: s.Label, User: s.User, RootMode: s.RootMode,
                Hosts: append([]string(nil), s.Hosts...),
                Tasks: append([]PlaybookTask(nil), s.Tasks...),
                Parallel: s.Parallel, RunTimeoutSec: s.RunTimeoutSec, ErrPolicy: s.ErrPolicy,
                Results: results,
        }
}

func recordToSession(rec RunRecord) *RunSession {
        return &RunSession{
                ID: rec.ID, Started: rec.Started, Finished: rec.Finished,
                Label: rec.Label, User: rec.User, RootMode: rec.RootMode,
                Hosts: rec.Hosts, Tasks: rec.Tasks,
                Parallel: rec.Parallel, RunTimeoutSec: rec.RunTimeoutSec, ErrPolicy: rec.ErrPolicy,
                Results: rec.Results,
        }
}

// saveRunHistory сохраняет запуск в отдельный JSON (секреты маскируются).
func saveRunHistory(s *RunSession) {
        dir := runsDir()
        if err := os.MkdirAll(dir, 0700); err != nil {
                return
        }
        rec := s.toRecord()
        for i := range rec.Results {
                if len(rec.Results[i].Stdout) > outputClipMax {
                        rec.Results[i].Stdout = rec.Results[i].Stdout[:outputClipMax] + "\n… (обрезано)"
                }
                if len(rec.Results[i].Stderr) > outputClipMax {
                        rec.Results[i].Stderr = rec.Results[i].Stderr[:outputClipMax] + "\n… (обрезано)"
                }
                rec.Results[i].Stdout = maskOutput(rec.Results[i].Stdout)
                rec.Results[i].Stderr = maskOutput(rec.Results[i].Stderr)
                rec.Results[i].Err = maskOutput(rec.Results[i].Err)
        }
        data, err := json.MarshalIndent(&rec, "", "  ")
        if err != nil {
                return
        }
        fn := filepath.Join(dir, fmt.Sprintf("run_%s_%s.json", s.Started.Format("20060102_150405"), s.ID))
        _ = os.WriteFile(fn, data, 0600)
        pruneRunHistory()
}

// pruneRunHistory удаляет самые старые записи сверх runsKeepMax.
func pruneRunHistory() {
        dir := runsDir()
        ents, err := os.ReadDir(dir)
        if err != nil || len(ents) <= runsKeepMax {
                return
        }
        type fnTime struct {
                name string
                t    time.Time
        }
        var list []fnTime
        for _, e := range ents {
                if !strings.HasPrefix(e.Name(), "run_") {
                        continue
                }
                info, err := e.Info()
                if err != nil {
                        continue
                }
                list = append(list, fnTime{e.Name(), info.ModTime()})
        }
        sort.Slice(list, func(i, j int) bool { return list[i].t.Before(list[j].t) })
        for i := 0; i < len(list)-runsKeepMax; i++ {
                _ = os.Remove(filepath.Join(dir, list[i].name))
        }
}

// loadRunHistory — последние запуски (новые первыми).
func loadRunHistory() []RunRecord {
        dir := runsDir()
        ents, err := os.ReadDir(dir)
        if err != nil {
                return nil
        }
        var out []RunRecord
        for _, e := range ents {
                if !strings.HasPrefix(e.Name(), "run_") || !strings.HasSuffix(e.Name(), ".json") {
                        continue
                }
                data, err := os.ReadFile(filepath.Join(dir, e.Name()))
                if err != nil {
                        continue
                }
                var rec RunRecord
                if json.Unmarshal(data, &rec) == nil {
                        out = append(out, rec)
                }
        }
        sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
        if len(out) > 200 {
                out = out[:200]
        }
        return out
}

// formatDur — «2.3 с» / «1 м 05 с».
func formatDur(ms int64) string {
        if ms <= 0 {
                return "—"
        }
        d := time.Duration(ms) * time.Millisecond
        if d < time.Second {
                return fmt.Sprintf("%d мс", ms)
        }
        if d < time.Minute {
                return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " с"
        }
        return fmt.Sprintf("%d м %02d с", int(d.Minutes()), int(d.Seconds())%60)
}

// collectVarRefs — все имена {{…}} (без секретов), использованные в задачах.
func collectVarRefs(tasks []PlaybookTask) []string {
        var refs []string
        seen := map[string]bool{}
        add := func(s string) {
                for _, m := range varRefRe.FindAllString(s, -1) {
                        name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m, "{{"), "}}"))
                        if name == "" || strings.HasPrefix(name, "secret:") {
                                continue
                        }
                        if !seen[name] {
                                seen[name] = true
                                refs = append(refs, name)
                        }
                }
        }
        for _, t := range tasks {
                add(t.Source)
                add(t.Action)
                add(t.Dest)
                add(t.Search)
                add(t.Replace)
                add(t.RestartService)
                for _, c := range t.PostCommands {
                        add(c)
                }
        }
        return refs
}

var varRefRe = regexp.MustCompile(`\{\{[^{}]+\}\}`)
