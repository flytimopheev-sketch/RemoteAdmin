package main

// execpanel.go — нижняя панель приложения (v1.9.0): три вкладки вместо
// смешанного журнала.
//
//   • «Выполнение» — хост → задача → статус словами → длительность → код
//     возврата; прогресс «7 из 12 хостов завершено»; раскрытие результата
//     пары «хост–задача»; «Повторить неудачные»; «Остановить» с выбором
//     области (очередь / текущий процесс / оба); история запусков.
//   • «Вывод» — раздельные stdout и stderr, поиск, копирование, сохранение.
//   • «События» — журнал приложения (подключения, действия, ошибки) с
//     развёрткой вывода двойным щелчком (v1.8.0).

import (
        "fmt"
        "os"
        "path/filepath"
        "strings"
        "time"

        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

const (
        EXC_HOST   = 0
        EXC_TASK   = 1
        EXC_STATUS = 2
        EXC_DUR    = 3
        EXC_RC     = 4
)

// ExecPanel — нижняя панель с тремя вкладками.
type ExecPanel struct {
        widget gtk.IWidget
        nb     *gtk.Notebook

        // «Выполнение»
        progLbl  *gtk.Label
        tree     *gtk.TreeView
        st       *gtk.ListStore
        detail   *gtk.TextView
        btnRetry *gtk.Button
        btnStop  *gtk.Button
        btnHist  *gtk.Button
        rowIters map[string]*gtk.TreeIter
        viewing  *RunSession
        viewLock bool // true = просмотр истории (не обновлять)

        // «Вывод»
        outView             *gtk.TextView
        outSearch           *gtk.Entry
        outFilter           *gtk.ComboBoxText
        outRebuildScheduled bool
}

var execPanel *ExecPanel

// execStatusColor — цвет статуса словами.
func execStatusColor(s string) string {
        switch s {
        case StSuccess:
                return "#34d399"
        case StError:
                return "#f87171"
        case StRunning:
                return "#60a5fa"
        case StQueued:
                return "#8b93a5"
        case StCancelled:
                return "#fbbf24"
        case StSkipped:
                return "#8b93a5"
        }
        return "#c9d1e3"
}

func execStatusMarkup(s string) string {
        return fmt.Sprintf(`<span foreground="%s" weight="bold">%s</span>`, execStatusColor(s), s)
}

// taskCellMarkup — две строки: имя шага (жирным) + команда моноширинным.
func taskCellMarkup(t PlaybookTask, idx int) string {
        cmd := t.stepCommand()
        if len(cmd) > 90 {
                cmd = cmd[:90] + "…"
        }
        title := fmt.Sprintf("%d. %s", idx+1, escapeMarkup(t.StepTitle()))
        return title + "\n" + `<span font="Monospace 8.5" foreground="#8fa0c0">$ ` + escapeMarkup(cmd) + `</span>`
}

// ---------- дерево панели ----------

func mkExecTree(titles []string, types []glib.Type, markupCols map[int]bool) (*gtk.TreeView, *gtk.ListStore) {
        tv, err := gtk.TreeViewNew()
        if err != nil {
                return nil, nil
        }
        st, err := gtk.ListStoreNew(types...)
        if err != nil {
                return nil, nil
        }
        tv.SetModel(st)
        tv.SetHeadersVisible(true)
        for i, t := range titles {
                col, _ := gtk.TreeViewColumnNew()
                col.SetTitle(t)
                cell, _ := gtk.CellRendererTextNew()
                col.PackStart(cell, true)
                if markupCols != nil && markupCols[i] {
                        col.AddAttribute(cell, "markup", i)
                } else {
                        col.AddAttribute(cell, "text", i)
                }
                if i == EXC_TASK || i == EXC_HOST {
                        col.SetExpand(true)
                }
                tv.AppendColumn(col)
        }
        return tv, st
}

// ---------- сборка панели ----------

func buildExecPanel() gtk.IWidget {
        p := &ExecPanel{rowIters: map[string]*gtk.TreeIter{}}
        execPanel = p

        p.nb, _ = gtk.NotebookNew()
        p.nb.SetHExpand(true)

        // ---------- вкладка «Выполнение» ----------
        exBox := boxNew(gtk.ORIENTATION_VERTICAL, 4)
        exTop := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        p.progLbl = labelNew("Нет активных запусков")
        p.progLbl.SetXAlign(0)
        exTop.PackStart(p.progLbl, true, true, 0)
        p.btnHist = buttonNew("История запусков")
        p.btnHist.SetTooltipText("Открыть сохранённые запуски: команды, цели, время и результаты\n(хранятся в ~/.config/RemoteAdmin/runs)")
        p.btnHist.Connect("clicked", func() { p.historyDialog() })
        p.btnStop = buttonNew("Остановить")
        p.btnStop.SetSensitive(false)
        p.btnStop.SetTooltipText("Остановить запуск (выбор: очередь / текущий процесс / оба)")
        p.btnStop.Connect("clicked", func() { p.stopClicked() })
        p.btnRetry = buttonNew("Повторить неудачные")
        p.btnRetry.SetSensitive(false)
        p.btnRetry.SetTooltipText("Перезапустить только неудачные задачи — только те,\nу которых включён флаг «Разрешён повторный запуск»")
        p.btnRetry.Connect("clicked", func() { p.retryFailed() })
        exTop.PackEnd(p.btnRetry, false, false, 0)
        exTop.PackEnd(p.btnStop, false, false, 0)
        exTop.PackEnd(p.btnHist, false, false, 0)
        exBox.PackStart(exTop, false, false, 0)

        p.tree, p.st = mkExecTree(
                []string{"Хост", "Задача", "Статус", "Длительность", "Код"},
                []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING},
                map[int]bool{EXC_HOST: true, EXC_TASK: true, EXC_STATUS: true},
        )
        sel, _ := p.tree.GetSelection()
        sel.SetMode(gtk.SELECTION_SINGLE)
        sel.Connect("changed", func() { p.showDetail() })
        tsc := scrolled(p.tree, 120)
        tsc.SetVExpand(true)
        exBox.PackStart(tsc, true, true, 0)

        // деталь выбранной пары «хост–задача»: stdout/stderr под таблицей
        p.detail, _ = gtk.TextViewNew()
        p.detail.SetEditable(false)
        p.detail.SetCursorVisible(false)
        p.detail.SetWrapMode(gtk.WRAP_WORD_CHAR)
        p.detail.SetMonospace(true)
        dsc := scrolled(p.detail, 96)
        dsc.SetVExpand(false)
        exBox.PackStart(dsc, false, false, 0)

        p.nb.AppendPage(exBox, tabLabel("Выполнение"))

        // ---------- вкладка «Вывод» ----------
        outBox := boxNew(gtk.ORIENTATION_VERTICAL, 4)
        outTop := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        p.outFilter, _ = gtk.ComboBoxTextNew()
        for _, s := range []string{"Всё", "Только stdout", "Только stderr", "Только ошибки"} {
                p.outFilter.AppendText(s)
        }
        p.outFilter.SetActive(0)
        p.outFilter.Connect("changed", func() { p.scheduleOutRebuild() })
        p.outSearch = entryNew("Поиск по выводу…")
        p.outSearch.Connect("changed", func() { p.scheduleOutRebuild() })
        btnCopy := buttonNew("Копировать")
        btnCopy.SetTooltipText("Скопировать показанный вывод в буфер обмена")
        btnCopy.Connect("clicked", func() { p.outCopy() })
        btnSave := buttonNew("Сохранить")
        btnSave.SetTooltipText("Сохранить показанный вывод в файл")
        btnSave.Connect("clicked", func() { p.outSave() })
        outTop.PackStart(p.outFilter, false, false, 0)
        outTop.PackStart(p.outSearch, true, true, 0)
        outTop.PackStart(btnCopy, false, false, 0)
        outTop.PackStart(btnSave, false, false, 0)
        outBox.PackStart(outTop, false, false, 0)
        p.outView, _ = gtk.TextViewNew()
        p.outView.SetEditable(false)
        p.outView.SetWrapMode(gtk.WRAP_NONE)
        p.outView.SetMonospace(true)
        obuf, _ := p.outView.GetBuffer()
        obuf.CreateTag("mono", map[string]interface{}{"family": "Monospace", "scale": 0.92})
        obuf.CreateTag("hdr", map[string]interface{}{"weight": 700, "foreground": "#9db2d8"})
        obuf.CreateTag("errtag", map[string]interface{}{"foreground": "#f87171", "family": "Monospace"})
        osc := scrolled(p.outView, 120)
        osc.SetVExpand(true)
        outBox.PackStart(osc, true, true, 0)
        p.nb.AppendPage(outBox, tabLabel("Вывод"))

        // ---------- вкладка «События» ----------
        evBox := boxNew(gtk.ORIENTATION_VERTICAL, 0)
        la, err := newLogArea()
        if err != nil {
                la = &LogArea{}
        }
        app.log = la
        evHead := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        evHint := labelNew("Подключения, действия пользователя, ошибки приложения. Двойной щелчок — полный вывод команды.")
        addClass(evHint, "ramuted")
        evHint.SetXAlign(0)
        btnClear := buttonNew("Очистить")
        btnClear.SetTooltipText("Очистить журнал событий")
        btnClear.Connect("clicked", func() { app.log.clear() })
        btnExp := buttonNew("Экспорт")
        btnExp.SetTooltipText("Сохранить журнал в текстовый файл")
        btnExp.Connect("clicked", func() { app.log.exportText() })
        evHead.PackStart(evHint, true, true, 0)
        evHead.PackEnd(btnExp, false, false, 0)
        evHead.PackEnd(btnClear, false, false, 0)
        evBox.PackStart(evHead, false, false, 3)
        evsc := scrolled(la.view, 120)
        evsc.SetVExpand(true)
        evBox.PackStart(evsc, true, true, 0)
        p.nb.AppendPage(evBox, tabLabel("События"))

        p.nb.Connect("switch-page", func(_ *gtk.Notebook, _ gtk.IWidget, num int) {
                if num == 1 {
                        p.rebuildOutput()
                }
        })

        runnerHooks.onResult = p.onResult
        runnerHooks.onHostDone = p.onHostDone
        runnerHooks.onRunStart = p.onRunStart
        runnerHooks.onRunEnd = p.onRunEnd
        runnerHooks.askPolicy = p.askPolicy

        p.widget = p.nb
        p.updateProgressLabel()
        return p.widget
}

func tabLabel(text string) *gtk.Label {
        return labelNew(text)
}

// ---------- отображение сессии ----------

func (p *ExecPanel) keyOf(host string, idx int) string {
        return host + "\x00" + fmt.Sprintf("%d", idx)
}

// loadSession заменяет содержимое таблицы содержимым сессии.
func (p *ExecPanel) loadSession(s *RunSession, viewing bool) {
        glibIdle(func() {
                p.viewing = s
                p.viewLock = viewing
                p.rowIters = map[string]*gtk.TreeIter{}
                p.st.Clear()
                s.mu.Lock()
                results := append([]TaskResult(nil), s.Results...)
                s.mu.Unlock()
                for _, r := range results {
                        iter := p.st.Append()
                        p.fillRow(iter, s, r)
                        p.rowIters[p.keyOf(r.Host, r.TaskIdx)] = iter
                }
                p.updateProgressLabel()
                p.updateBtns()
        })
}

func (p *ExecPanel) fillRow(iter *gtk.TreeIter, s *RunSession, r TaskResult) {
        tcmd := ""
        if r.TaskIdx >= 0 && r.TaskIdx < len(s.Tasks) {
                tcmd = taskCellMarkup(s.Tasks[r.TaskIdx], r.TaskIdx)
        } else {
                tcmd = escapeMarkup(r.TaskName)
        }
        _ = p.st.Set(iter,
                []int{EXC_HOST, EXC_TASK, EXC_STATUS, EXC_DUR, EXC_RC},
                []interface{}{
                        "<b>" + escapeMarkup(r.Host) + "</b>",
                        tcmd,
                        execStatusMarkup(r.Status),
                        formatDur(r.DurMs),
                        rcText(r),
                })
}

// rcText — «0» для успеха, «—» для очереди, иначе код возврата.
func rcText(r TaskResult) string {
        switch r.Status {
        case StQueued, StRunning, StSkipped:
                return "—"
        }
        return fmt.Sprintf("%d", r.RC)
}

// onResult — асинхронное обновление одной пары.
func (p *ExecPanel) onResult(res TaskResult) {
        glibIdle(func() {
                s := p.viewing
                if s == nil || p.viewLock {
                        return
                }
                if iter, ok := p.rowIters[p.keyOf(res.Host, res.TaskIdx)]; ok {
                        p.fillRow(iter, s, res)
                }
                p.updateProgressLabel()
                p.updateBtns()
                if p.nb != nil && p.nb.GetCurrentPage() == 1 {
                        p.scheduleOutRebuild()
                }
        })
}

// onHostDone — хост завершил все задачи.
func (p *ExecPanel) onHostDone(host, status string) {
        app.hostsUI.updateStatusRow(host, status)
}

func (p *ExecPanel) onRunStart(s *RunSession) {
        glibIdle(func() {
                p.viewLock = false
                p.loadSession(s, false)
                if app != nil && app.pbUI != nil {
                        app.pbUI.refreshTargetLine()
                }
        })
}

func (p *ExecPanel) onRunEnd(s *RunSession) {
        glibIdle(func() {
                p.loadSession(s, false)
                okN, failN, cancelN, skippedN := s.counts()
                kind := "accent"
                msg := fmt.Sprintf("🏁 Запуск «%s» завершён: успех %d, ошибок %d, отменено %d, пропущено %d",
                        s.Label, okN, failN, cancelN, skippedN)
                if failN == 0 && cancelN == 0 {
                        kind = "ok"
                }
                logf(kind, "%s", msg)
                Notify("RemoteAdmin", fmt.Sprintf("«%s»: %d ОК, %d ошибок", s.Label, okN, failN))
                notifyDone(s.Label, s.Hosts, okN, failN, 0)
                app.setProgress(okN+failN+cancelN, okN+failN+cancelN+skippedN)
                app.setStatus(fmt.Sprintf("«%s»: успех %d, ошибок %d", s.Label, okN, failN))
        })
}

func (p *ExecPanel) updateProgressLabel() {
        s := p.viewing
        if s == nil || p.viewLock {
                if p.progLbl != nil {
                        p.progLbl.SetText("Нет активных запусков")
                }
                return
        }
        done, total := s.hostsDone()
        okN, failN, cancelN, skippedN := s.counts()
        var b strings.Builder
        fmt.Fprintf(&b, "Хостов завершено: %d из %d · задач: успех %d, ошибок %d, отменено %d, пропущено %d",
                done, total, okN, failN, cancelN, skippedN)
        if !s.Started.IsZero() && s.Finished.IsZero() {
                fmt.Fprintf(&b, " · идёт %s", formatDur(time.Since(s.Started).Milliseconds()))
        } else if !s.Finished.IsZero() {
                fmt.Fprintf(&b, " · заняло %s", formatDur(s.Finished.Sub(s.Started).Milliseconds()))
        }
        p.progLbl.SetText(b.String())
}

func (p *ExecPanel) updateBtns() {
        running := p.viewing != nil && !p.viewLock && p.viewing.Finished.IsZero()
        p.btnStop.SetSensitive(running)
        p.btnRetry.SetSensitive(p.failedRetryableCount() > 0)
}

func (p *ExecPanel) failedRetryableCount() int {
        s := p.viewing
        if s == nil {
                return 0
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        n := 0
        for _, r := range s.Results {
                if r.Status != StError || r.TaskIdx < 0 || r.TaskIdx >= len(s.Tasks) {
                        continue
                }
                if s.Tasks[r.TaskIdx].AllowRetry {
                        n++
                }
        }
        return n
}

// ---------- деталь пары ----------

func (p *ExecPanel) showDetail() {
        s := p.viewing
        if s == nil {
                return
        }
        host, idx, ok := p.selectedPair()
        if !ok {
                return
        }
        s.mu.Lock()
        var resCopy TaskResult
        for i := range s.Results {
                if s.Results[i].Host == host && s.Results[i].TaskIdx == idx {
                        resCopy = s.Results[i]
                        break
                }
        }
        s.mu.Unlock()
        var b strings.Builder
        if resCopy.Host == "" {
                b.WriteString("Выберите строку выше, чтобы увидеть stdout/stderr.")
        } else {
                fmt.Fprintf(&b, "Хост: %s\nЗадача: %s\nСтатус: %s · код возврата: %s · длительность: %s · попыток: %d\n",
                        resCopy.Host, resCopy.TaskName, resCopy.Status, rcText(resCopy), formatDur(resCopy.DurMs), resCopy.Attempt)
                if resCopy.Err != "" {
                        b.WriteString("Ошибка: " + resCopy.Err + "\n")
                }
                b.WriteString("\n— STDOUT —\n" + orDash(resCopy.Stdout))
                b.WriteString("\n— STDERR —\n" + orDash(resCopy.Stderr))
        }
        dbuf, _ := p.detail.GetBuffer()
        dbuf.SetText(orDash(strings.TrimSpace(b.String())))
}

func orDash(s string) string {
        if strings.TrimSpace(s) == "" {
                return "(пусто)"
        }
        return s
}

func (p *ExecPanel) selectedPair() (string, int, bool) {
        m, iter, ok := selIter(p.tree)
        if !ok {
                return "", -1, false
        }
        host := iterGetString(m, iter, EXC_HOST)
        host = strings.TrimSuffix(strings.TrimPrefix(host, "<b>"), "</b>")
        taskCol := iterGetString(m, iter, EXC_TASK)
        idx := -1
        n := 0
        for i, c := range taskCol {
                if c == '.' && i > 0 {
                        if v, err := fmt.Sscanf(taskCol[:i], "%d", &n); err == nil && v == 1 {
                                idx = n - 1
                                break
                        }
                }
        }
        if idx < 0 && p.viewing != nil {
                for i, t := range p.viewing.Tasks {
                        if strings.Contains(taskCol, escapeMarkup(t.StepTitle())) {
                                idx = i
                                break
                        }
                }
        }
        return host, idx, idx >= 0
}

// ---------- вывод (вкладка 2) ----------

func (p *ExecPanel) scheduleOutRebuild() {
        if p.outRebuildScheduled {
                return
        }
        p.outRebuildScheduled = true
        glibTimeout(250, func() bool {
                p.outRebuildScheduled = false
                p.rebuildOutput()
                return false
        })
}

func (p *ExecPanel) rebuildOutput() {
        s := p.viewing
        obuf, _ := p.outView.GetBuffer()
        if s == nil {
                obuf.SetText("Пока нет выполненных запусков.")
                return
        }
        filter := 0
        if p.outFilter != nil {
                filter = p.outFilter.GetActive()
        }
        search := ""
        if p.outSearch != nil {
                search = strings.ToLower(strings.TrimSpace(entText(p.outSearch)))
        }
        obuf.SetText("")
        s.mu.Lock()
        results := append([]TaskResult(nil), s.Results...)
        s.mu.Unlock()
        written := false
        for _, r := range results {
                switch filter {
                case 1:
                        r.Stderr = ""
                case 2:
                        r.Stdout = ""
                case 3:
                        if r.Status != StError {
                                continue
                        }
                }
                block := fmt.Sprintf("%s · %s · %s · код %s · %s", r.Host, r.TaskName, r.Status, rcText(r), formatDur(r.DurMs))
                body := ""
                if strings.TrimSpace(r.Stdout) != "" {
                        body += "\n[stdout]\n" + r.Stdout
                }
                if strings.TrimSpace(r.Stderr) != "" {
                        body += "\n[stderr]\n" + r.Stderr
                }
                if r.Err != "" {
                        body += "\n[ошибка] " + r.Err
                }
                full := block + body
                if search != "" && !strings.Contains(strings.ToLower(full), search) {
                        continue
                }
                if written {
                        iter := obuf.GetEndIter()
                        obuf.Insert(iter, "\n──────────────\n")
                }
                written = true
                iter := obuf.GetEndIter()
                obuf.InsertWithTagByName(iter, block+"\n", "hdr")
                if body != "" {
                        iter := obuf.GetEndIter()
                        obuf.InsertWithTagByName(iter, body+"\n", "mono")
                }
        }
        if !written {
                iter := obuf.GetEndIter()
                if search != "" {
                        obuf.Insert(iter, "Ничего не найдено по запросу «"+search+"»")
                } else {
                        obuf.Insert(iter, "Нет данных для отображения.")
                }
        }
}

func (p *ExecPanel) outCopy() {
        buf, _ := p.outView.GetBuffer()
        start, end := buf.GetStartIter(), buf.GetEndIter()
        text, _ := buf.GetText(start, end, false)
        cb, err := gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)
        if err == nil {
                cb.SetText(text)
        }
        logf("ok", "📋 Вывод скопирован (%d симв.)", len(text))
}

func (p *ExecPanel) outSave() {
        fn := fileChooser(app.window, "Сохранить вывод", gtk.FILE_CHOOSER_ACTION_SAVE, "remoteadmin_output.txt")
        if fn == "" {
                return
        }
        buf, _ := p.outView.GetBuffer()
        start, end := buf.GetStartIter(), buf.GetEndIter()
        text, _ := buf.GetText(start, end, false)
        if err := os.WriteFile(fn, []byte(text), 0600); err != nil {
                errDialog(app.window, "Не удалось сохранить: "+err.Error())
                return
        }
        logf("ok", "📥 Вывод сохранён: %s", fn)
}

// ---------- остановка ----------

func (p *ExecPanel) stopClicked() {
        s := curSession.Load()
        if s == nil {
                return
        }
        p.stopDialog(s)
}

// stopDialog — выбор того, что остановится (с пояснением каждой опции).
func (p *ExecPanel) stopDialog(s *RunSession) {
        d, err := gtk.DialogNewWithButtons("Остановить запуск?", app.window, gtk.DIALOG_MODAL)
        if err != nil {
                return
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(520, 190)
        box, _ := d.GetContentArea()
        box.SetMarginStart(12)
        box.SetMarginEnd(12)
        box.SetMarginTop(8)
        box.SetMarginBottom(8)
        lbl := labelNew("Что именно остановить? Запуск: «" + s.Label + "»")
        lbl.SetXAlign(0)
        box.PackStart(lbl, false, false, 6)
        inner := boxNew(gtk.ORIENTATION_VERTICAL, 5)
        mkBtn := func(txt, tip string, resp gtk.ResponseType) {
                b := buttonNew(txt)
                b.SetTooltipText(tip)
                b.Connect("clicked", func() { d.Response(resp) })
                inner.PackStart(b, false, false, 0)
        }
        mkBtn("Остановить очередь",
                "Новые задачи на хостах не запускаются; уже выполняющиеся команды доработают до конца",
                gtk.RESPONSE_OK)
        mkBtn("Остановить текущий процесс",
                "Активные команды на хостах будут прерваны сразу; очередь продолжит запускать остальные шаги",
                gtk.RESPONSE_APPLY)
        mkBtn("Остановить всё (и очередь, и процессы)",
                "Прерывает текущие команды и не запускает новые задачи — полный останов",
                gtk.RESPONSE_YES)
        box.PackStart(inner, true, true, 6)
        box.ShowAll()
        resp := d.Run()
        switch resp {
        case gtk.RESPONSE_OK:
                s.stopQueue()
                logf("warn", "⏹ Остановка очереди: новые задачи не запускаются («%s»)", s.Label)
        case gtk.RESPONSE_APPLY:
                s.stopCurrent()
        case gtk.RESPONSE_YES:
                s.stopBoth()
                logf("warn", "⏹ Полная остановка запуска «%s»", s.Label)
        }
}

// askPolicy — диалог политики ошибки «спросить».
func (p *ExecPanel) askPolicy(s *RunSession, host string, t PlaybookTask, res TaskResult) bool {
        ch := make(chan bool, 1)
        glibIdle(func() {
                d, err := gtk.DialogNewWithButtons("Ошибка задачи", app.window, gtk.DIALOG_MODAL,
                        []interface{}{"Продолжить", gtk.RESPONSE_ACCEPT},
                        []interface{}{"Остановить", gtk.RESPONSE_CANCEL},
                )
                if err != nil {
                        ch <- false
                        return
                }
                defer d.Destroy()
                defer unregisterModal()
                registerModal(d)
                box, _ := d.GetContentArea()
                lbl := labelNew(fmt.Sprintf("Хост %s: задача «%s»\n\n%s", host, t.StepTitle(), truncateOut(res.Err, 400)))
                lbl.SetLineWrap(true)
                box.PackStart(lbl, false, false, 10)
                box.ShowAll()
                ch <- d.Run() == gtk.RESPONSE_ACCEPT
        })
        return <-ch
}

// ---------- повтор неудачных ----------

func (p *ExecPanel) retryFailed() {
        s := p.viewing
        if s == nil {
                return
        }
        if !needCreds() {
                return
        }
        type pair struct {
                host string
                idx  int
        }
        var pairs []pair
        skippedNoPerm := 0
        s.mu.Lock()
        for _, r := range s.Results {
                if r.Status != StError || r.TaskIdx < 0 || r.TaskIdx >= len(s.Tasks) {
                        continue
                }
                if s.Tasks[r.TaskIdx].AllowRetry {
                        pairs = append(pairs, pair{r.Host, r.TaskIdx})
                } else {
                        skippedNoPerm++
                }
        }
        s.mu.Unlock()
        if len(pairs) == 0 {
                warnDialog(app.window, "Нет неудачных задач с включённым «Разрешён повторный запуск».\n\nВключите флаг у задачи в редакторе плейбука — приложение не считает команду безопасной для повтора автоматически.")
                return
        }
        if skippedNoPerm > 0 {
                logf("warn", "↻ Пропущено задач без разрешения повтора: %d", skippedNoPerm)
        }
        taskSet := map[int]bool{}
        hostSet := map[string]bool{}
        for _, pr := range pairs {
                taskSet[pr.idx] = true
                hostSet[pr.host] = true
        }
        var tasks []PlaybookTask
        for oldIdx := range taskSet {
                tasks = append(tasks, s.Tasks[oldIdx])
        }
        var hosts []string
        for ip := range hostSet {
                hosts = append(hosts, ip)
        }
        newS := buildRunSession(hosts, tasks, "Повтор: "+s.Label, s.vars)
        go func() { newS.runSession() }()
        logf("accent", "↻ Повтор неудачных: %d задач на %d хост(ов)", len(tasks), len(hosts))
}

// ---------- история запусков ----------

func (p *ExecPanel) historyDialog() {
        runs := loadRunHistory()
        d, err := gtk.DialogNewWithButtons("История запусков", app.window, gtk.DIALOG_MODAL,
                []interface{}{"Открыть", gtk.RESPONSE_ACCEPT},
                []interface{}{"Удалить", gtk.RESPONSE_REJECT},
                []interface{}{"Закрыть", gtk.RESPONSE_CANCEL},
        )
        if err != nil {
                return
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(780, 470)
        box, _ := d.GetContentArea()
        box.SetMarginStart(10)
        box.SetMarginEnd(10)
        box.SetMarginTop(8)
        box.SetMarginBottom(6)

        tree, st := mkExecTree(
                []string{"Дата", "Плейбук", "Хостов", "Успешно", "Ошибки", "Длительность"},
                []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING},
                nil,
        )
        sel, _ := tree.GetSelection()
        sel.SetMode(gtk.SELECTION_SINGLE)
        for _, r := range runs {
                okN, failN, _, _ := countsOf(r.Results)
                iter := st.Append()
                dur := "—"
                if !r.Finished.IsZero() {
                        dur = formatDur(r.Finished.Sub(r.Started).Milliseconds())
                }
                _ = st.Set(iter,
                        []int{0, 1, 2, 3, 4, 5},
                        []interface{}{
                                r.Started.Format("2006-01-02 15:04"),
                                r.Label,
                                fmt.Sprintf("%d", len(r.Hosts)),
                                fmt.Sprintf("%d", okN),
                                fmt.Sprintf("%d", failN),
                                dur,
                        })
        }
        hint := labelNew("Откройте вчерашний запуск — увидите использованные команды, цели, время и результаты каждой задачи на каждом хосте.")
        addClass(hint, "ramuted")
        hint.SetLineWrap(true)
        hint.SetXAlign(0)
        box.PackStart(hint, false, false, 4)
        sc := scrolled(tree, 0)
        sc.SetVExpand(true)
        box.PackStart(sc, true, true, 4)
        box.ShowAll()
        resp := d.Run()
        if resp == gtk.RESPONSE_REJECT {
                if m, iter, ok := selIter(tree); ok {
                        deleteRunFileByStamp(iterGetString(m, iter, 0))
                }
                p.historyDialog()
                return
        }
        if resp != gtk.RESPONSE_ACCEPT {
                return
        }
        if m, iter, ok := selIter(tree); ok {
                stamp := iterGetString(m, iter, 0)
                for _, r := range runs {
                        if r.Started.Format("2006-01-02 15:04") == stamp {
                                p.loadSession(recordToSession(r), true)
                                logf("info", "🗂 Открыт запуск «%s» от %s (просмотр)", r.Label, stamp)
                                break
                        }
                }
        }
}

func deleteRunFileByStamp(stamp string) {
        dir := runsDir()
        ents, _ := os.ReadDir(dir)
        for _, e := range ents {
                if strings.HasPrefix(e.Name(), "run_") && strings.Contains(e.Name(), stamp) {
                        _ = os.Remove(filepath.Join(dir, e.Name()))
                        logf("ok", "🗑 Запуск %s удалён из истории", stamp)
                        return
                }
        }
}
