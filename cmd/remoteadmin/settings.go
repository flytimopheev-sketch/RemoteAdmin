package main

import (
        "encoding/base64"
        "fmt"
        "os"
        "path/filepath"
        "strings"

        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

var base64Std = base64.StdEncoding

// (autotest hooks live in dialogs.go: registerModal/unregisterModal)

// showSettingsDialog — all app settings with live theme preview.
// NB: содержимое обязательно внутри ScrolledWindow — раньше секции
// упаковывались прямо в контентную область, диалог «вытягивался» и
// нижние настройки (known_hosts) оставались за пределами экрана.
func showSettingsDialog() {
        s := store.State.Settings
        d, err := gtk.DialogNewWithButtons(
                "Настройки", app.window, gtk.DIALOG_MODAL,
                []interface{}{"Применить", gtk.RESPONSE_ACCEPT},
                []interface{}{"Отмена", gtk.RESPONSE_CANCEL},
        )
        if err != nil {
                return
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(540, 520)

        inner := boxNew(gtk.ORIENTATION_VERTICAL, 2)
        inner.SetMarginStart(12)
        inner.SetMarginEnd(12)
        inner.SetMarginTop(8)
        inner.SetMarginBottom(4)

        grid := mkGrid()
        row := 0

        // appearance
        addSection(inner, "Внешний вид")
        themeC, _ := gtk.ComboBoxTextNew()
        themeC.AppendText("Тёмная")
        themeC.AppendText("Светлая")
        if s.Theme == "light" {
                themeC.SetActive(1)
        } else {
                themeC.SetActive(0)
        }
        grid.attach(labelNew("Тема:"), 0, row, 1, 1)
        grid.attach(themeC, 1, row, 1, 1)
        row++

        opLabel := labelNew(fmt.Sprintf("Прозрачность: %.0f%%", s.Opacity*100))
        scale, _ := gtk.ScaleNewWithRange(gtk.ORIENTATION_HORIZONTAL, 40, 100, 1)
        scale.SetValue(s.Opacity * 100)

        scale.Connect("value-changed", func() {
                opLabel.SetText(fmt.Sprintf("Прозрачность: %.0f%%", scale.GetValue()))
        })
        grid.attach(labelNew("Прозрачность:"), 0, row, 1, 1)
        grid.attach(scale, 1, row, 1, 1)
        row++
        grid.attach(opLabel, 1, row, 1, 1)
        row++
        inner.PackStart(grid.w, false, false, 4)

        // ssh
        grid2 := mkGrid()
        row2 := 0
        addSection(inner, "SSH и выполнение")
        connSpin := spinBox(float64(s.ConnectTimeout), 1, 600)
        execSpin := spinBox(float64(s.ExecTimeout), 1, 86400)
        parSpin := spinBox(float64(maxInt(s.MaxParallel, 1)), 1, 128)
        retSpin := spinBox(float64(maxInt(s.Retries, 0)), 0, 10)

        grid2.attach(labelNew("Таймаут подключения, с:"), 0, row2, 1, 1)
        grid2.attach(connSpin, 1, row2, 1, 1)
        row2++
        grid2.attach(labelNew("Таймаут команды, с:"), 0, row2, 1, 1)
        grid2.attach(execSpin, 1, row2, 1, 1)
        row2++
        grid2.attach(labelNew("Параллельных потоков:"), 0, row2, 1, 1)
        grid2.attach(parSpin, 1, row2, 1, 1)
        row2++
        grid2.attach(labelNew("Повторов при сбое:"), 0, row2, 1, 1)
        grid2.attach(retSpin, 1, row2, 1, 1)
        row2++

        strictC, _ := gtk.ComboBoxTextNew()
        strictC.AppendText("Спрашивать (log TOFU)")
        strictC.AppendText("Принимать новые (accept-new)")
        strictC.AppendText("Без проверки")
        switch s.StrictHostKey {
        case "accept-new":
                strictC.SetActive(1)
        case "no":
                strictC.SetActive(2)
        default:
                strictC.SetActive(0)
        }
        grid2.attach(labelNew("Проверка ключей хоста:"), 0, row2, 1, 1)
        grid2.attach(strictC, 1, row2, 1, 1)
        row2++

        confC, _ := gtk.CheckButtonNewWithLabel("Подтверждать опасные операции (замена файлов, sed, rollback)")
        confC.SetActive(s.ConfirmDanger)
        grid2.attach(confC, 1, row2, 1, 1)
        row2++

        massSpin := spinBox(float64(maxInt(s.MassConfirmMin, 0)), 0, 1000)
        massSpin.SetTooltipText("От скольких хостов операция считается массовой и требует отдельного подтверждения с галочкой. 0 — выкл. Хосты среды «Продакшен» подтверждаются всегда.")
        grid2.attach(labelNew("Массовое изменение от N хостов (0 — выкл.):"), 0, row2, 1, 1)
        grid2.attach(massSpin, 1, row2, 1, 1)
        row2++
        inner.PackStart(grid2.w, false, false, 4)

        // monitoring & notifications
        grid3 := mkGrid()
        row3 := 0
        addSection(inner, "Мониторинг и уведомления")
        monSpin := spinBox(float64(maxInt(s.MonitorInterval, 1)), 1, 3600)
        grid3.attach(labelNew("Интервал опроса, с:"), 0, row3, 1, 1)
        grid3.attach(monSpin, 1, row3, 1, 1)
        row3++
        notC, _ := gtk.CheckButtonNewWithLabel("Уведомления рабочего стола")
        notC.SetActive(s.Notifications)
        grid3.attach(notC, 1, row3, 1, 1)
        row3++
        inner.PackStart(grid3.w, false, false, 4)

        // webhook + email
        addSection(inner, "Webhook и e-mail о результатах задач")
        weLbl := labelNew("После каждой групповой задачи отправляется событие\n(JSON) на webhook и/или письмо через SMTP.")
        wsc, _ := weLbl.GetStyleContext()
        wsc.AddClass("ramuted")
        weLbl.SetXAlign(0)
        inner.PackStart(weLbl, false, false, 0)
        grid4 := mkGrid()
        row4 := 0
        eHook := entryNew("https://example.com/hook или https://hooks.slack.com/services/…")
        eHook.SetText(s.WebhookURL)
        eHook.SetTooltipText("POST с JSON: {event, task, total, ok, failed, warn, hosts, time, status}")
        grid4.attach(labelNew("Webhook URL:"), 0, row4, 1, 1)
        grid4.attach(eHook, 1, row4, 1, 1)
        row4++

        failOnlyC, _ := gtk.CheckButtonNewWithLabel("Отправлять только при ошибках/предупреждениях")
        failOnlyC.SetActive(s.NotifyFailOnly)
        grid4.attach(failOnlyC, 1, row4, 1, 1)
        row4++

        eSMTP := entryNew("smtp.yandex.ru:465")
        eSMTP.SetText(s.EmailSMTP)
        grid4.attach(labelNew("SMTP-сервер:"), 0, row4, 1, 1)
        grid4.attach(eSMTP, 1, row4, 1, 1)
        row4++
        sslC, _ := gtk.CheckButtonNewWithLabel("TLS сразу (порт 465); снять — STARTTLS (587/25)")
        sslC.SetActive(s.EmailSSL)
        grid4.attach(sslC, 1, row4, 1, 1)
        row4++
        eUser := entryNew("user@example.ru")
        eUser.SetText(s.EmailUser)
        grid4.attach(labelNew("Логин SMTP:"), 0, row4, 1, 1)
        grid4.attach(eUser, 1, row4, 1, 1)
        row4++
        ePass := entryNew("пароль приложения")
        ePass.SetVisibility(false)
        ePass.SetText(store.EmailPass())
        grid4.attach(labelNew("Пароль SMTP:"), 0, row4, 1, 1)
        grid4.attach(ePass, 1, row4, 1, 1)
        row4++
        eFrom := entryNew("RemoteAdmin <user@example.ru>")
        eFrom.SetText(s.EmailFrom)
        grid4.attach(labelNew("От кого:"), 0, row4, 1, 1)
        grid4.attach(eFrom, 1, row4, 1, 1)
        row4++
        eTo := entryNew("admin@example.ru")
        eTo.SetText(s.EmailTo)
        grid4.attach(labelNew("Кому:"), 0, row4, 1, 1)
        grid4.attach(eTo, 1, row4, 1, 1)
        row4++
        testRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        btnTestHook := buttonNew("Проверить webhook")
        btnTestHook.Connect("clicked", func() {
                if err := testWebhookURL(entText(eHook)); err != nil {
                        errDialog(app.window, "Webhook: "+err.Error())
                } else {
                        infoDialog(app.window, "Тестовое событие отправлено — проверьте приёмник")
                }
        })
        btnTestMail := buttonNew("Проверить e-mail")
        btnTestMail.Connect("clicked", func() {
                if err := testEmailNow(entText(eSMTP), entText(eUser), entText(ePass), entText(eFrom), entText(eTo), sslC.GetActive()); err != nil {
                        errDialog(app.window, "E-mail: "+err.Error())
                } else {
                        infoDialog(app.window, "Тестовое письмо отправлено на "+entText(eTo))
                }
        })
        testRow.PackStart(btnTestHook, false, false, 0)
        testRow.PackStart(btnTestMail, false, false, 0)
        grid4.attach(labelNew(""), 0, row4, 1, 1)
        grid4.attach(testRow, 1, row4, 1, 1)
        row4++
        inner.PackStart(grid4.w, false, false, 4)

        // мастер-пароль
        addSection(inner, "Шифрование хранилища (state.json)")
        mpLbl := labelNew(masterStatusText())
        msc, _ := mpLbl.GetStyleContext()
        msc.AddClass("ramuted")
        mpLbl.SetXAlign(0)
        mpLbl.SetLineWrap(true)
        inner.PackStart(mpLbl, false, false, 0)
        mpRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        btnMpOn := buttonNew("Включить мастер-пароль…")
        btnMpOn.SetTooltipText("Весь state.json шифруется AES-256-GCM ключом из пароля (scrypt); пароль спрашивается при каждом запуске")
        btnMpOn.Connect("clicked", func() {
                promptMasterEnable()
                mpLbl.SetText(masterStatusText())
        })
        btnMpOff := buttonNew("Выключить…")
        btnMpOff.Connect("clicked", func() {
                promptMasterDisable()
                mpLbl.SetText(masterStatusText())
        })
        mpRow.PackStart(btnMpOn, false, false, 0)
        mpRow.PackStart(btnMpOff, false, false, 0)
        inner.PackStart(mpRow, false, false, 2)


        // known hosts manager
        addSection(inner, "Ключи хостов (known_hosts)")
        khLabel := labelNew(knownHostsSummary())
        khsc, _ := khLabel.GetStyleContext()
        khsc.AddClass("ramuted")
        khLabel.SetXAlign(0)
        inner.PackStart(khLabel, false, false, 0)
        khRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        btnKh := buttonNew("Управление ключами")
        btnKh.Connect("clicked", func() {
                showKnownHostsDialog()
                khLabel.SetText(knownHostsSummary())
        })
        btnKhReset := buttonNew("Сбросить все ключи")
        btnKhReset.GetStyleContext()
        btnKhReset.Connect("clicked", func() {
                if confirmDialog(app.window, "Удалить ВСЕ сохранённые ключи хостов?\nПри следующем подключении ключи будут приняты заново.") {
                        _ = os.Remove(knownHostsPath())
                        initKnownHosts()
                        khLabel.SetText(knownHostsSummary())
                        logf("warn", "♻ Все ключи хостов сброшены")
                }
        })
        khRow.PackStart(btnKh, false, false, 0)
        khRow.PackStart(btnKhReset, false, false, 0)
        inner.PackStart(khRow, false, false, 2)

        // прокрутка: всё содержимое внутри ScrolledWindow, диалог фиксированной
        // высоты — низ всегда доступен, даже на маленьких экранах
        sc, err2 := gtk.ScrolledWindowNew(nil, nil)
        if err2 == nil && sc != nil {
                sc.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
                sc.Add(inner)
                cbox, _ := d.GetContentArea()
                cbox.PackStart(sc, true, true, 0)
                cbox.ShowAll() // NB: показать контейнер вместе со ScrolledWindow
        } else {
                cbox, _ := d.GetContentArea()
                cbox.PackStart(inner, true, true, 0)
                cbox.ShowAll()
        }

        if d.Run() == gtk.RESPONSE_ACCEPT {
                s.Theme = map[int]string{0: "dark", 1: "light"}[themeC.GetActive()]
                s.Opacity = scale.GetValue() / 100
                s.ConnectTimeout = int(connSpin.GetValue())
                s.ExecTimeout = int(execSpin.GetValue())
                s.MaxParallel = int(parSpin.GetValue())
                s.Retries = int(retSpin.GetValue())
                s.StrictHostKey = map[int]string{0: "ask", 1: "accept-new", 2: "no"}[strictC.GetActive()]
                s.ConfirmDanger = confC.GetActive()
                s.MassConfirmMin = int(massSpin.GetValue())
                s.MonitorInterval = int(monSpin.GetValue())
                s.Notifications = notC.GetActive()
                s.WebhookURL = strings.TrimSpace(entText(eHook))
                s.NotifyFailOnly = failOnlyC.GetActive()
                s.EmailSMTP = strings.TrimSpace(entText(eSMTP))
                s.EmailSSL = sslC.GetActive()
                s.EmailUser = strings.TrimSpace(entText(eUser))
                s.EmailFrom = strings.TrimSpace(entText(eFrom))
                s.EmailTo = strings.TrimSpace(entText(eTo))
                store.SetEmailPass(entText(ePass))
                store.State.Settings = s
                _ = store.Save()
                applyTheme(app.window)
                // force repaint of window background
                app.window.QueueDraw()
                logf("ok", "⚙ Настройки применены (тема: %s, прозрачность %.0f%%, потоков %d)",
                        s.Theme, s.Opacity*100, s.MaxParallel)
        }
}

func addSection(box *gtk.Box, title string) {
        sep, _ := gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL)
        l := labelNew(title)
        sc, _ := l.GetStyleContext()
        sc.AddClass("ratitle")
        l.SetXAlign(0)
        box.PackStart(sep, false, false, 2)
        box.PackStart(l, false, false, 2)
}

func spinBox(val, min, max float64) *gtk.SpinButton {
        sp, _ := gtk.SpinButtonNewWithRange(min, max, 1)
        sp.SetValue(val)
        return sp
}

func knownHostsSummary() string {
        if kh == nil {
                return "Ключей: 0"
        }
        kh.mu.Lock()
        defer kh.mu.Unlock()
        return fmt.Sprintf("Сохранено ключей: %d (%s)", len(kh.hosts), knownHostsPath())
}

// showKnownHostsDialog lists stored host keys with delete option.
func showKnownHostsDialog() {
        d, err := gtk.DialogNewWithButtons(
                "Ключи хостов", app.window, gtk.DIALOG_MODAL,
                []interface{}{"Удалить выбранные", gtk.RESPONSE_ACCEPT},
                []interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
        )
        if err != nil {
                return
        }
        defer d.Destroy()
        d.SetDefaultSize(560, 420)
        box, _ := d.GetContentArea()

        tv, st := makeTree([]string{"Хост", "Отпечаток"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING})
        sel, _ := tv.GetSelection()
        sel.SetMode(gtk.SELECTION_MULTIPLE)
        kh.mu.Lock()
        hostsList := make([]string, 0, len(kh.hosts))
        for h := range kh.hosts {
                hostsList = append(hostsList, h)
        }
        kh.mu.Unlock()
        for _, h := range hostsList {
                iter := st.Append()
                _ = st.Set(iter, []int{0, 1}, []interface{}{h, kh.hosts[h]})
        }
        sc := scrolled(tv, 0)
        sc.SetVExpand(true)
        box.PackStart(sc, true, true, 4)
        box.ShowAll()

        if d.Run() == gtk.RESPONSE_ACCEPT {
                // collect selected rows, rewrite file
                var selected []string
                if mI, err := tv.GetModel(); err == nil {
                        if m, ok2 := anyTreeModel(mI); ok2 {
                                iter, ok3 := m.GetIterFirst()
                                for ok3 {
                                        if sel.IterIsSelected(iter) {
                                                selected = append(selected, iterGetString(m, iter, 0))
                                        }
                                        ok3 = m.IterNext(iter)
                                }
                        }
                }
                removeHostKeys(selected)
                initKnownHosts()
                logf("ok", "🗝 Удалено ключей: %d", len(selected))
        }
}

// removeHostKeys rewrites the app known_hosts without given hosts.
func removeHostKeys(hosts []string) {
        data, err := os.ReadFile(knownHostsPath())
        if err != nil {
                return
        }
        var out []string
        for _, line := range strings.Split(string(data), "\n") {
                l := strings.TrimSpace(line)
                if l == "" {
                        continue
                }
                fields := strings.Fields(l)
                if len(fields) < 3 {
                        continue
                }
                hostPart := fields[0]
                drop := false
                if strings.HasPrefix(hostPart, "|1|") {
                        parts := strings.Split(hostPart, "|")
                        if len(parts) == 4 {
                                for _, h := range hosts {
                                        if hostMatchesHashed(h, parts[2], parts[3]) {
                                                drop = true
                                                break
                                        }
                                }
                        }
                } else {
                        norm := normalizeHostPattern(hostPart)
                        for _, h := range hosts {
                                if norm == h || norm == h+":22" {
                                        drop = true
                                        break
                                }
                        }
                }
                if !drop {
                        out = append(out, l)
                }
        }
        _ = os.WriteFile(knownHostsPath(), []byte(strings.Join(out, "\n")+"\n"), 0600)
}

func hostMatchesHashed(hostname, saltB64, hashB64 string) bool {
        salt, e1 := base64Decode(saltB64)
        want, e2 := base64Decode(hashB64)
        if e1 != nil || e2 != nil {
                return false
        }
        got := sha1Hash(append(append([]byte{}, salt...), []byte(hostname)...))
        return string(got) == string(want)
}

func base64Decode(s string) ([]byte, error) {
        return base64Std.DecodeString(s)
}

// apply settings on start
var _ = filepath.Join
var _ = glib.TimeoutAdd
