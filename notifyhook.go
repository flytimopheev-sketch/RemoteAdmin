package main

import (
        "crypto/tls"
        "encoding/json"
        "fmt"
        "net"
        "net/http"
        "net/smtp"
        "os"
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
//  - smtps://… (порт 465, TLS сразу)  — задаётся в настройках галкой «SSL/TLS сразу»;
//  - STARTTLS, если сервер его предлагает (порт 587/25).
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

func smtpSend(addr, user, pass, from, to, subject, body string, ssl bool) error {
        if from == "" {
                from = user
        }
        if i := strings.Index(to, ","); i > 0 {
                to = strings.TrimSpace(to[:i])
        }
        host, _, err := net.SplitHostPort(addr)
        if err != nil {
                return fmt.Errorf("адрес SMTP должен быть host:port: %v", err)
        }
        var c *smtp.Client
        if ssl {
                conn, e := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
                if e != nil {
                        return fmt.Errorf("TLS-подключение: %v", e)
                }
                c, err = smtp.NewClient(conn, host)
        } else {
                c, err = smtp.Dial(addr)
        }
        if err != nil {
                return err
        }
        defer c.Close()
        if err := c.Hello("localhost"); err != nil {
                return err
        }
        if !ssl {
                if ok, _ := c.Extension("STARTTLS"); ok {
                        if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
                                return fmt.Errorf("STARTTLS: %v", err)
                        }
                }
        }
        if user != "" {
                if ok, _ := c.Extension("AUTH"); ok {
                        if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
                                return fmt.Errorf("AUTH: %v", err)
                        }
                }
        }
        if err := c.Mail(from); err != nil {
                return err
        }
        if err := c.Rcpt(to); err != nil {
                return err
        }
        w, err := c.Data()
        if err != nil {
                return err
        }
        msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\nDate: %s\r\n\r\n%s\r\n",
                from, to, subject, time.Now().Format(time.RFC1123Z), body)
        if _, err := w.Write([]byte(msg)); err != nil {
                return err
        }
        if err := w.Close(); err != nil {
                return err
        }
        return c.Quit()
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
