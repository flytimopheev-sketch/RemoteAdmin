package main

import (
        "fmt"
        "net"
        "os"
        "path/filepath"
        "strings"
        "sync"
        "time"

        "golang.org/x/crypto/ssh"
)

// ---------- credentials ----------

// splitHostPort supports "host:port" entries (custom SSH port) while staying
// safe for plain IPv4/hostname and not misparsing IPv6.
func splitHostPort(s string) (string, int, bool) {
        if strings.Count(s, ":") != 1 {
                return s, 22, false
        }
        i := strings.Index(s, ":")
        if i == 0 || i == len(s)-1 {
                return s, 22, false
        }
        p := atoiSafe(s[i+1:])
        if p <= 0 || p > 65535 {
                return s, 22, false
        }
        return s[:i], p, true
}

func buildCreds(ip string) SSHCreds {
        s := store.State.Settings
        host, port := ip, 22
        if h, p, ok := splitHostPort(ip); ok {
                host, port = h, p
        }
        return SSHCreds{
                Host:       host,
                Port:       port,
                User:       store.State.Username,
                Password:   store.Password(),
                KeyPath:    store.State.KeyPath,
                KeyPass:    store.KeyPassphrase(),
                RootPass:   store.RootPassword(),
                StrictKey:  s.StrictHostKey,
                TimeoutSec: s.ConnectTimeout,
        }
}

func needCreds() bool {
        if store.State.Username == "" {
                warnDialog(app.window, "Укажите логин SSH в панели «Действия»")
                return false
        }
        if store.Password() == "" && store.State.KeyPath == "" {
                warnDialog(app.window, "Укажите пароль или путь к SSH-ключу")
                return false
        }
        return true
}

// ---------- parallel runner with progress ----------

// runParallel executes fn for each IP using a worker pool,
// updating host badges and the global progress bar.
func runParallel(ips []string, taskName string, fn func(ip string) (string, error)) {
        if len(ips) == 0 {
                warnDialog(app.window, "Не выбрано ни одного хоста")
                return
        }
        parallel := store.State.Settings.MaxParallel
        if parallel <= 0 {
                parallel = 8
        }
        for _, ip := range ips {
                app.hostsUI.updateStatusRow(ip, "run")
        }
        doneCh := make(chan struct{}, len(ips))
        go func() {
                sem := make(chan struct{}, parallel)
                var wg sync.WaitGroup
                var mu sync.Mutex
                completed := 0
                okN, failN, warnN := 0, 0, 0
                for _, ip := range ips {
                        wg.Add(1)
                        go func(ip string) {
                                defer wg.Done()
                                sem <- struct{}{}
                                defer func() { <-sem }()
                                out, err := fn(ip)
                                status := "ok"
                                st := "OK"
                                if err != nil {
                                        status = "fail"
                                        st = "FAIL"
                                        out = fmt.Sprintf("%s\nОшибка: %v", out, err)
                                } else {
                                        // warn if output suggests error
                                        lo := strings.ToLower(out)
                                        if strings.Contains(lo, "permission denied") || strings.Contains(lo, "command not found") {
                                                status = "warn"
                                                st = "WARN"
                                        }
                                }
                                mu.Lock()
                                completed++
                                if status == "fail" {
                                        failN++
                                } else if status == "warn" {
                                        warnN++
                                } else {
                                        okN++
                                }
                                d, t := completed, len(ips)
                                mu.Unlock()
                                out = redactSecrets(out) // секреты не должны попадать в журнал/хранилище
                                addReport(taskName, ip, st, out)
                                store.State.LastOutput[ip] = out
                                // цвет строки = результат: зелёный OK / красный FAIL /
                                // жёлтый WARN; двойной щелчок — полный вывод команды
                                line := fmt.Sprintf("%s %s → %s", statusEmoji(st), ip, st)
                                if err != nil {
                                        line += " — " + truncateOut(err.Error(), 140)
                                } else if st == "WARN" {
                                        for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
                                                if strings.TrimSpace(l) != "" {
                                                        line += " — " + truncateOut(l, 140)
                                                        break
                                                }
                                        }
                                }
                                kind := "ok"
                                switch st {
                                case "FAIL":
                                        kind = "fail"
                                case "WARN":
                                        kind = "warn"
                                }
                                logfDetail(kind, "%s", &LogDetail{
                                        Task:   taskName,
                                        Host:   ip,
                                        Status: st,
                                        Output: out,
                                }, line)
                                glibIdle(func() {
                                        app.hostsUI.updateStatusRow(ip, status)
                                        app.setProgress(d, t)
                                        app.setStatus(fmt.Sprintf("%s: %d/%d (ОК %d, ошибок %d)", taskName, d, t, okN, failN))
                                })
                                doneCh <- struct{}{}
                        }(ip)
                }
                wg.Wait()
                glibIdle(func() {
                        logf("accent", fmt.Sprintf("🏁 %s завершено: всего %d, ОК %d, ошибок %d", taskName, len(ips), okN, failN))
                        Notify(taskName, fmt.Sprintf("Готово: %d ОК, %d ошибок из %d", okN, failN, len(ips)))
                        // внешние уведомления: webhook и e-mail (если настроены)
                        notifyDone(taskName, ips, okN, failN, warnN)
                        _ = store.Save()
                })
        }()
}

// ---------- privileged exec helper ----------

func execOnHost(ip, cmd string, root bool) (string, error) {
        creds := buildCreds(ip)
        cl, err := Connect(creds)
        if err != nil {
                return "", err
        }
        defer cl.Close()
        if root && store.State.UseRoot && creds.User != "root" {
                if creds.RootPass == "" {
                        return "", fmt.Errorf("нужен пароль root (su)")
                }
                return ExecRoot(cl, cmd, creds.RootPass, store.State.Settings.ExecTimeout)
        }
        return Exec(cl, cmd, store.State.Settings.ExecTimeout)
}

// ---------- quick actions ----------

func runPingAction(ips []string) {
        if len(ips) == 0 {
                warnDialog(app.window, "Не выбрано ни одного хоста")
                return
        }
        logf("info", "📡 Ping: %d хост(ов)…", len(ips))
        app.setStatus("Ping…")
        go runParallel(ips, "Ping", func(ip string) (string, error) {
                start := time.Now()
                conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", ip), 5*time.Second)
                if err != nil {
                        return "", fmt.Errorf("TCP 22 недоступен: %v", err)
                }
                conn.Close()
                ms := time.Since(start).Milliseconds()
                return fmt.Sprintf("TCP 22: OK, %d мс", ms), nil
        })
}

func runSysInfoAction(ips []string) {
        if !needCreds() {
                return
        }
        logf("info", "🖥 Сбор информации о системе: %d хост(ов)…", len(ips))
        go runParallel(ips, "Инфо о системе", func(ip string) (string, error) {
                out, err := execOnHost(ip,
                        "hostname; uname -r; grep PRETTY_NAME /etc/os-release 2>/dev/null | head -1; "+
                                "nproc; free -m | awk 'NR==2{print \"RAM: \" $3 \"/\" $2 \" МБ\"}'; "+
                                "df -h / | awk 'NR==2{print \"Диск /: \" $3 \"/\" $2 \" (\" $5 \")\"}'; uptime -p 2>/dev/null", false)
                if err == nil {
                        out = strings.TrimSpace(out)
                }
                return out, err
        })
}

func runQuickCommand(ips []string, cmd string) {
        if len(ips) == 0 {
                glibIdle(func() { warnDialog(app.window, "Не выбрано ни одного хоста") })
                return
        }
        if store.State.Settings.ConfirmDanger && looksDangerous(cmd) &&
                !glibSyncConfirm("Выполнить потенциально опасную команду?", cmd, len(ips)) {
                return
        }
        if store.State.Username == "" || (store.Password() == "" && store.State.KeyPath == "") {
                glibIdle(func() { warnDialog(app.window, "Укажите доступы в панели «Действия»") })
                return
        }
        logf("cmd", "▶ Команда на %d хост(ов): %s", len(ips), cmd)
        go runParallel(ips, "Команда", func(ip string) (string, error) {
                return execOnHost(ip, cmd, false)
        })
}

func runQuickScript(ips []string, localPath string) {
        if len(ips) == 0 {
                glibIdle(func() { warnDialog(app.window, "Не выбрано ни одного хоста") })
                return
        }
        if store.State.Username == "" || (store.Password() == "" && store.State.KeyPath == "") {
                glibIdle(func() { warnDialog(app.window, "Укажите доступы в панели «Действия»") })
                return
        }
        logf("info", "📜 Скрипт %s на %d хост(ов)…", filepath.Base(localPath), len(ips))
        go runParallel(ips, "Скрипт", func(ip string) (string, error) {
                creds := buildCreds(ip)
                cl, err := Connect(creds)
                if err != nil {
                        return "", err
                }
                defer cl.Close()
                tmp := fmt.Sprintf("/tmp/.ra_script_%d.sh", time.Now().UnixNano())
                if err := scpUpload(cl, localPath, tmp); err != nil {
                        return "", err
                }
                defer Exec(cl, "rm -f "+shellQuote(tmp), 15)
                cmd := "sh " + shellQuote(tmp)
                if store.State.UseRoot && creds.User != "root" && creds.RootPass != "" {
                        return ExecRoot(cl, cmd, creds.RootPass, store.State.Settings.ExecTimeout)
                }
                return Exec(cl, cmd, store.State.Settings.ExecTimeout)
        })
}

func runQuickReplace(ips []string, localPath, remotePath string) {
        if len(ips) == 0 {
                glibIdle(func() { warnDialog(app.window, "Не выбрано ни одного хоста") })
                return
        }
        // NB: подтверждение выполняет replaceRunDialog (файл + путь + список
        // хостов) — здесь повторный вопрос больше не задаётся.
        logf("warn", "🔄 Замена файла %s → %s на %d хост(ах)…", filepath.Base(localPath), remotePath, len(ips))
        go runParallel(ips, "Замена файла", func(ip string) (string, error) {
                creds := buildCreds(ip)
                cl, err := Connect(creds)
                if err != nil {
                        return "", err
                }
                defer cl.Close()
                needRoot := store.State.UseRoot && creds.User != "root"
                if needRoot && creds.RootPass == "" {
                        return "", fmt.Errorf("нужен пароль root")
                }
                if !needRoot {
                        if err := scpUpload(cl, localPath, remotePath); err != nil {
                                return "", err
                        }
                        return "файл заменён: " + remotePath, nil
                }
                // backup + replace as root
                bak := fmt.Sprintf("%s.ra_bak_%d", remotePath, time.Now().Unix())
                if _, err := ExecRoot(cl, fmt.Sprintf("cp -f %s %s 2>/dev/null; true", shellQuote(remotePath), shellQuote(bak)), creds.RootPass, 30); err != nil {
                        return "", err
                }
                if err := uploadThenSuRoot(cl, localPath, remotePath, creds.RootPass, store.State.Settings.ExecTimeout); err != nil {
                        return "", err
                }
                return fmt.Sprintf("файл заменён (backup: %s)", bak), nil
        })
}

func scpUpload(cl *ssh.Client, localPath, remotePath string) error {
        return uploadThenSuRoot(cl, localPath, remotePath, "", 300)
}

// looksDangerous detects destructive shell patterns for confirmation.
func looksDangerous(cmd string) bool {
        lo := strings.ToLower(cmd)
        patterns := []string{
                "rm -rf", "mkfs", "dd if=", "> /dev/", "reboot", "shutdown", "poweroff",
                ":(){", "chmod -r 000", "chown -r", "iptables -f", "kill -9 1",
                "sed -i", "userdel", "groupdel", "init 0", "init 6",
        }
        for _, p := range patterns {
                if strings.Contains(lo, p) {
                        return true
                }
        }
        return false
}

// glibSyncConfirm asks a blocking yes/no from any goroutine.
func glibSyncConfirm(title, detail string, hosts int) bool {
        ch := make(chan bool, 1)
        glibIdle(func() {
                text := fmt.Sprintf("%s\n\n%s\nХостов: %d\n\nПродолжить?", title, detail, hosts)
                ch <- confirmDialog(app.window, text)
        })
        select {
        case v := <-ch:
                return v
        case <-time.After(1800 * time.Second):
                return false
        }
}

var _ = os.Getenv
