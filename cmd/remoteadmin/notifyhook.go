package main

import (
        "crypto/tls"
        "encoding/json"
        "fmt"
        "net"
        "net/http"
        "net/smtp"
        "os"
        "strconv"
        "strings"
        "time"
)

// ---------- внешние уведомления: webhook и e-mail ----------
//
// После завершения групповой задачи runParallel вызывает notifyDone().
// Помимо рабочего стола (Notify) результат можно доставить:
//  - на webhook (HTTP POST с JSON-событием) — Slack/Mattermost/Telegram-bridge/свой сервис;
//  - письмом через SMTP (STARTTLS или TLS-с-первой-команды).

type HookEvent struct {
        Event   string   `json:"event"`
        App     string   `json:"app"`
        Version string   `json:"version"`
        Task    string   `json:"task"`
        Total   int      `json:"total"`
        OK      int      `json:"ok"`
        Failed  int      `json:"failed"`
        Warn    int      `json:"warn"`
        Hosts   []string `json:"hosts,omitempty"`
        Time    string   `json:"time"`
        Status  string   `json:"status"`
}

// notifyDone вызывается по завершении любой групповой задачи.
func notifyDone(taskName string, ips []string, okN, failN, warnN int) {
        ev := HookEvent{
                Event:   "task.finished",
                App:     AppName,
                Version: AppVersion,
                Task:    taskName,
                Total:   len(ips),
                OK:      okN,
                Failed:  failN,
                Warn:    warnN,
                Hosts:   ips,
                Time:    time.Now().Format("2006-01-02 15:04:05"),
        }
        if failN > 0 {
                ev.Status = "fail"
        } else if warnN > 0 {
                ev.Status = "warn"
        } else {
                ev.Status = "ok"
        }
        s := store.State.Settings
        if s.WebhookURL != "" {
                if !s.NotifyFailOnly || ev.Status != "ok" {
                        go postWebhook(s.WebhookURL, ev)
                }
        }
        if s.EmailTo != "" && s.EmailSMTP != "" {
                if !s.NotifyFailOnly || ev.Status != "ok" {
                        go sendEventEmail(ev)
                }
        }
}

// postWebhook отправляет JSON-событие; ответ игнорируется, ошибка — в журнал.
func postWebhook(url string, ev HookEvent) error {
        data, err := json.MarshalIndent(ev, "", "  ")
        if err != nil {
                return err
        }
        client := &http.Client{Timeout: 15 * time.Second}
        req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(data)))
        if err != nil {
                return err
        }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("User-Agent", AppName+"/"+AppVersion)
        req.Header.Set("X-RemoteAdmin-Event", ev.Event)
        req.Header.Set("X-RemoteAdmin-Status", ev.Status)
        resp, err := client.Do(req)
        if err != nil {
                logf("warn", "🔗 Webhook: %v", err)
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode >= 300 {
                logf("warn", "🔗 Webhook %s: HTTP %d", hostOfURL(url), resp.StatusCode)
                return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
        }
        logf("ok", "🔗 Webhook доставлен: %s (%s, %s)", hostOfURL(url), ev.Task, ev.Status)
        return nil
}

func hostOfURL(u string) string {
        u = strings.TrimPrefix(u, "http://")
        u = strings.TrimPrefix(u, "https://")
        if i := strings.IndexAny(u, "/"); i > 0 {
                u = u[:i]
        }
        return u
}

// hookEventText — человекочитаемое представление события (письмо, лог).
func hookEventText(ev HookEvent) string {
        var b strings.Builder
        fmt.Fprintf(&b, "Задача: %s\n", ev.Task)
        fmt.Fprintf(&b, "Результат: %s (OK %d, ошибок %d, предупреждений %d, всего %d)\n",
                ev.Status, ev.OK, ev.Failed, ev.Warn, ev.Total)
        if len(ev.Hosts) > 0 {
                fmt.Fprintf(&b, "Хосты: %s\n", strings.Join(ev.Hosts, ", "))
        }
        fmt.Fprintf(&b, "Время: %s\n", ev.Time)
        fmt.Fprintf(&b, "Источник: %s %s\n", ev.App, ev.Version)
        return b.String()
}

// sendEventEmail отправляет письмо через SMTP. Поддерживается:
//  - порт 465 → TLS-с-первой-команды (автоопределение по порту);
//  - галка «TLS сразу» в настройках — принудительный implicit TLS;
//  - STARTTLS, если сервер его предлагает (порт 587/25);
//  - AUTH PLAIN или LOGIN (почта Mail.ru/Yandex/Ред АДМ требует LOGIN).
func sendEventEmail(ev HookEvent) error {
        s := store.State.Settings
        if s.EmailSMTP == "" || s.EmailTo == "" {
                return fmt.Errorf("SMTP не настроен")
        }
        subject := fmt.Sprintf("[RemoteAdmin] %s — %s (OK %d / ошибок %d)", ev.Task, ev.Status, ev.OK, ev.Failed)
        body := hookEventText(ev)
        err := smtpSend(s.EmailSMTP, s.EmailUser, store.EmailPass(), s.EmailFrom, s.EmailTo, subject, body, s.EmailSSL)
        if err != nil {
                logf("warn", "✉ E-mail: %v", err)
        } else {
                logf("ok", "✉ Письмо отправлено: %s → %s", s.EmailTo, ev.Task)
        }
        return err
}

// loginAuth реализует механизм AUTH LOGIN (в Go stdlib его нет,
// а многие серверы — Mail.ru, Yandex 465, Exchange — предлагают только его).
type loginAuth struct {
        user, pass string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
        return "LOGIN", []byte(a.user), nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
        if !more {
                return nil, nil
        }
        switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
        case "username:":
                return []byte(a.user), nil
        case "password:":
                return []byte(a.pass), nil
        }
        // некоторые серверы шлют просто «User Name» / «Password» без двоеточия
        if strings.Contains(strings.ToLower(string(fromServer)), "user") {
                return []byte(a.user), nil
        }
        return []byte(a.pass), nil
}

// splitRecipients разбивает поле «Кому» на список адресов
// (разделители: запятая, точка с запятой, пробел, перевод строки).
func splitRecipients(to string) []string {
        var out []string
        for _, t := range strings.FieldsFunc(to, func(r rune) bool {
                return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
        }) {
                t = strings.TrimSpace(t)
                if t != "" {
                        out = append(out, t)
                }
        }
        return out
}

func smtpSend(addr, user, pass, from, to, subject, body string, ssl bool) error {
        if from == "" {
                from = user
        }
        rcpts := splitRecipients(to)
        if len(rcpts) == 0 {
                return fmt.Errorf("не указан получатель")
        }
        to = rcpts[0]
        host, portStr, err := net.SplitHostPort(addr)
        if err != nil {
                // пользователь указал только хост — подбираем порт по галке
                host = strings.TrimSpace(addr)
                if host == "" {
                        return fmt.Errorf("не указан SMTP-сервер")
                }
                if ssl {
                        portStr = "465"
                } else {
                        portStr = "587"
                }
                addr = net.JoinHostPort(host, portStr)
        }
        port, _ := strconv.Atoi(portStr)
        // v1.14.0: автоопределение шифрования — жалоба «почта не работает»:
        // порт 465 (Evolution «SSL/TLS на зашифрованном порту») требует TLS
        // СРАЗУ, без EHLO; раньше при снятой галке соединение шло открытым
        // текстом и сервер обрывал его (EOF) — письмо не уходило.
        implicitTLS := ssl || port == 465 || port == 4465

        dialer := &net.Dialer{Timeout: 10 * time.Second}
        var conn net.Conn
        var c *smtp.Client
        if implicitTLS {
                tlsc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
                conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsc)
                if err != nil {
                        return fmt.Errorf("TLS-подключение к %s: %v", addr, err)
                }
                c, err = smtp.NewClient(conn, host)
        } else {
                conn, err = dialer.Dial("tcp", addr)
                if err == nil {
                        c, err = smtp.NewClient(conn, host)
                }
        }
        if err != nil {
                return err
        }
        defer c.Close()
        // общий тайм-аут сессии: сервер не отвечает — не зависаем навсегда
        conn.SetDeadline(time.Now().Add(60 * time.Second))
        if err := c.Hello("localhost"); err != nil {
                return fmt.Errorf("EHLO: %v", err)
        }
        if !implicitTLS {
                if ok, _ := c.Extension("STARTTLS"); ok {
                        if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
                                return fmt.Errorf("STARTTLS: %v", err)
                        }
                }
        }
        if user != "" {
                if err := tryAuth(c, user, pass, host); err != nil {
                        return err
                }
        }
        if err := c.Mail(from); err != nil {
                return fmt.Errorf("MAIL FROM: %v", err)
        }
        for _, r := range rcpts {
                if err := c.Rcpt(r); err != nil {
                        return fmt.Errorf("RCPT %s: %v", r, err)
                }
        }
        w, err := c.Data()
        if err != nil {
                return err
        }
        msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\nX-Mailer: %s/%s\r\nDate: %s\r\n\r\n%s\r\n",
                from, strings.Join(rcpts, ", "), subject, AppName, AppVersion, time.Now().Format(time.RFC1123Z), body)
        if _, err := w.Write([]byte(msg)); err != nil {
                return err
        }
        if err := w.Close(); err != nil {
                return fmt.Errorf("DATA: %v", err)
        }
        return c.Quit()
}

// tryAuth подбирает механизм аутентификации: PLAIN → LOGIN → CRAM-MD5.
// Раньше только PLAIN: Mail.ru/Yandex на некоторых портах отвечают
// «535 auth failed» именно потому, что ждут LOGIN.
func tryAuth(c *smtp.Client, user, pass, host string) error {
        okPlain, _ := c.Extension("AUTH")
        if !okPlain {
                return nil // сервер не требует аутентификации (локальный relay)
        }
        // пароль нельзя отправлять открытым текстом: без TLS разрешаем
        // аутентификацию только для локальных релеев (localhost/127.0.0.1)
        st, _ := c.TLSConnectionState()
        if !st.HandshakeComplete {
                if host != "localhost" && host != "127.0.0.1" && !strings.HasPrefix(host, "::1") {
                        return fmt.Errorf("сервер %s не поддерживает шифрование (STARTTLS/TLS) — пароль отправлять небезопасно; включите порт 465 или 587", host)
                }
        }
        if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err == nil {
                return nil
        }
        if err := c.Auth(&loginAuth{user: user, pass: pass}); err == nil {
                return nil
        }
        if err := c.Auth(smtp.CRAMMD5Auth(user, pass)); err == nil {
                return nil
        }
        return fmt.Errorf("AUTH: сервер отклонил логин/пароль (проверьте логин и пароль приложения)")
}

// testWebhookURL — кнопка «Проверить» в настройках: шлёт тестовое событие.
func testWebhookURL(url string) error {
        if strings.TrimSpace(url) == "" {
                return fmt.Errorf("укажите URL")
        }
        ev := HookEvent{
                Event: "test", App: AppName, Version: AppVersion,
                Task: "Тест webhook", Status: "ok",
                Time: time.Now().Format("2006-01-02 15:04:05"), Total: 0,
                Hosts: []string{},
        }
        return postWebhook(url, ev)
}

// testEmailNow — кнопка «Проверить» в настройках: шлёт тестовое письмо.
func testEmailNow(smtpAddr, user, pass, from, to string, ssl bool) error {
        if strings.TrimSpace(smtpAddr) == "" || strings.TrimSpace(to) == "" {
                return fmt.Errorf("укажите сервер и получателя")
        }
        return smtpSend(smtpAddr, user, pass, from, to,
                "[RemoteAdmin] тестовое письмо",
                fmt.Sprintf("Настройки SMTP работают.\n\nИсточник: %s %s\nВремя: %s\nХост: %s\n",
                        AppName, AppVersion, time.Now().Format("2006-01-02 15:04:05"), os.Getenv("HOSTNAME")),
                ssl)
}
