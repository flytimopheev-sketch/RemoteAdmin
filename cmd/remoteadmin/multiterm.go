package main

import (
        "fmt"
        "io"
        "regexp"
        "strconv"
        "strings"
        "sync"
        "time"

        "github.com/gotk3/gotk3/gtk"
        "golang.org/x/crypto/ssh"
)

// MultiTermUI — ClusterSSH-like grid: one REAL terminal per host (VTE).
//
// Каждая ячейка — настоящий терминальный эмулятор (libvte, как в GNOME
// Terminal): в него можно вводить команды непосредственно (щелчок по
// ячейке → клавиатура идёт в неё), полноэкранные программы (mc, top,
// vim) работают как в обычном терминале, есть скроллбэк и ANSI-цвета.
//
//   ⇉  — получать broadcast-команды из нижней строки;
//   ●  — статус сессии;
//   ✕  — закрыть только этот терминал.
//
// su → root (v1.11.0): если задан root-пароль и включён переключатель
// «su → root», после входа под учётной записью сессия сама выполняет
// `su -` и подставляет пароль root — терминал остаётся интерактивным.
type MultiTermUI struct {
        widget   gtk.IWidget
        grid     *gtk.Grid
        input    *gtk.Entry
        bcastLbl *gtk.Label
        suC      *gtk.CheckButton
        winModeC *gtk.CheckButton // «В отдельных окнах» (v1.12.0)
        ph       *gtk.Label       // подсказка-плейсхолдер первой сетки
        sessions map[string]*termSession
        mu       sync.Mutex
        running  bool
}

// termCell bundles the widgets of one terminal pane.
type termCell struct {
        box    *gtk.Box         // framed wrapper attached into the grid/window
        head   *gtk.Label       // status line
        send   *gtk.CheckButton // «⇉ отправлять сюда broadcast»
        vteBox *gtk.Box         // контейнер для VTE + скроллбара
        vte    *vteTerm
        win    *gtk.Window // отдельное окно (если терминал откреплён) — v1.12.0
}

type termSession struct {
        ip        string
        user      string
        cl        *ssh.Client
        sess      *ssh.Session
        stdin     io.WriteCloser
        cell      termCell
        send      bool   // получать broadcast-команды
        alive     bool
        closed    bool
        suWanted  bool   // выполнить su → root после входа
        suActive  bool   // su выполнен успешно
        bridge    *ptyBridge
        pendRows  int // ожидаемый размер сетки (до создания ssh-сессии)
        pendCols  int

        // кольцо последнего вывода — для поиска приглашения «Пароль:»
        ringMu sync.Mutex
        ring   string
}

func (s *termSession) pushOut(p []byte) {
        s.ringMu.Lock()
        s.ring += string(p)
        if len(s.ring) > 16384 {
                s.ring = s.ring[len(s.ring)-8192:]
        }
        s.ringMu.Unlock()
}

func (s *termSession) ringLen() int {
        s.ringMu.Lock()
        defer s.ringMu.Unlock()
        return len(s.ring)
}

var suFailRe = regexp.MustCompile(`(?i)(неверный пароль|authentication failure|попробуйте|try again|дважды|permission denied)`)

// waitRing ждёт появления шаблона в выводе после позиции mark.
func (s *termSession) waitRing(mark int, re *regexp.Regexp, timeout time.Duration) bool {
        deadline := time.Now().Add(timeout)
        for time.Now().Before(deadline) {
                s.ringMu.Lock()
                tail := ""
                if mark < len(s.ring) {
                        tail = s.ring[mark:]
                }
                s.ringMu.Unlock()
                if re.MatchString(tail) {
                        return true
                }
                time.Sleep(120 * time.Millisecond)
        }
        return false
}

func newMultiTermUI() *MultiTermUI {
        u := &MultiTermUI{sessions: map[string]*termSession{}}
        root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

        toolbar := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        btnStart := buttonNew("Подключить выбранные")
        ss, _ := btnStart.GetStyleContext()
        ss.AddClass("suggested")
        btnStart.SetTooltipText("Открыть по одному настоящему терминалу (VTE) на каждый хост, выбранный на вкладке «Хосты».\nВ каждый терминал можно вводить команды отдельно — щёлкните по нему и печатайте.\nПолноэкранные программы (mc, top, vim) работают как в обычном терминале.")
        btnStart.Connect("clicked", func() { u.startAll() })
        btnStop := buttonNew("Закрыть все")
        btnStop.SetTooltipText("Закрыть все активные сессии")
        btnStop.Connect("clicked", func() { u.closeAll() })
        toolbar.PackStart(btnStart, false, false, 0)
        toolbar.PackStart(btnStop, false, false, 0)

        u.suC, _ = gtk.CheckButtonNewWithLabel("🔑 su → root")
        u.suC.SetTooltipText("После входа под учётной записью выполнить `su -` и подставить сохранённый root-пароль.\nЕсли пароль root не задан в настройках — вход остаётся под учётной записью.")
        u.suC.SetActive(store.State.UseRoot)
        toolbar.PackStart(u.suC, false, false, 0)

        // v1.12.0: терминалы в отдельных окнах — каждое окно можно свободно
        // перемещать, сворачивать и разворачивать средствами оконного менеджера
        u.winModeC, _ = gtk.CheckButtonNewWithLabel("В отдельных окнах")
        u.winModeC.SetActive(!store.State.TermGrid)
        u.winModeC.SetTooltipText("Каждый терминал — отдельное окно: перемещается, сворачивается и разворачивается как обычное окно.\nСнимите галочку, чтобы вернуть сетку терминалов внутри вкладки.")
        u.winModeC.Connect("toggled", func() {
                store.State.TermGrid = !u.winModeC.GetActive()
                _ = store.Save()
        })
        toolbar.PackStart(u.winModeC, false, false, 0)

        hint := labelNew("Каждый терминал — настоящий (VTE): печатайте прямо в него, mc/top работают. ⇉ — broadcast, ⤢ — в отдельное окно, ✕ — закрыть.")
        hint.SetLineWrap(true)
        hsc, _ := hint.GetStyleContext()
        hsc.AddClass("ramuted")
        toolbar.PackStart(hint, false, false, 4)
        root.PackStart(toolbar, false, false, 0)

        sc := scrolled(nil, 0)
        u.grid, _ = gtk.GridNew()
        u.grid.SetRowSpacing(8)
        u.grid.SetColumnSpacing(8)
        u.grid.SetMarginTop(4)
        sc.Add(u.grid)
        sc.SetVExpand(true)
        root.PackStart(sc, true, true, 0)

        // placeholder when nothing is connected
        ph := labelNew("Нет активных сессий.\n\n1. Отметьте хосты на вкладке «Хосты»\n2. Нажмите «Подключить выбранные»\n3. По умолчанию каждый терминал открывается В СВОЁМ ОКНЕ — его можно\n    перемещать, сворачивать и разворачивать; печатайте прямо в него\n4. Или используйте Broadcast внизу — команда уйдёт во все терминалы с галочкой ⇉\n5. Кнопка ⤢ в ячейке открепляет терминал в отдельное окно\n\nmc, top, vim и другие полноэкранные программы работают как в настоящем терминале")
        ph.SetLineWrap(true)
        ph.SetJustify(gtk.JUSTIFY_LEFT)
        ph.SetJustify(gtk.JUSTIFY_CENTER)
        ph.SetHAlign(gtk.ALIGN_CENTER)
        ph.SetVAlign(gtk.ALIGN_CENTER)
        addClass(ph, "raempty")
        u.ph = ph
        u.grid.Attach(ph, 0, 0, 1, 1)

        inputRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        addClass(inputRow, "broadcast")
        u.bcastLbl = labelNew("⌨  ВСЕМ ХОСТАМ (0 из 0):")
        u.input, _ = gtk.EntryNew()
        u.input.SetPlaceholderText("Broadcast: команда во все терминалы с галочкой ⇉ (Enter — отправить). В одиночный терминал печатайте прямо в нём.")
        u.input.Connect("activate", func() {
                cmd := entText(u.input)
                if strings.TrimSpace(cmd) != "" {
                        u.broadcast(cmd + "\n")
                        u.input.SetText("")
                }
        })
        inputRow.PackStart(u.bcastLbl, false, false, 0)
        inputRow.PackStart(u.input, true, true, 0)
        root.PackStart(inputRow, false, false, 0)

        u.widget = root
        return u
}

func (u *MultiTermUI) startAll() {
        if u.running {
                warnDialog(app.window, "Терминалы уже запущены. Сначала нажмите «Закрыть все».")
                return
        }
        ips := app.hostsUI.selectedHosts()
        if len(ips) == 0 {
                warnDialog(app.window, "Выберите хосты на вкладке «Хосты» (выделением строк)")
                return
        }
        if !needCreds() {
                return
        }
        if u.suC.GetActive() && store.RootPassword() == "" {
                warnDialog(app.window, "Включён режим «su → root», но root-пароль не задан.\nУкажите его в настройках (вкладка «Хосты» → Параметры подключения) —\nили снимите галочку «su → root».")
        }
        u.running = true
        // close old sessions synchronously (network-only ops, no widgets);
        // NB: the sessions map MUST be rebuilt here — doing it inside glibIdle
        // raced with the loop below and broadcast lost every fresh session.
        for _, s := range u.sessions {
                u.closeSession(s)
        }
        u.sessions = map[string]*termSession{}
        winMode := u.winModeC.GetActive()
        if !winMode {
                glibIdle(func() { u.grid = resetGrid(u.grid) })
        }
        // сетка: 1–2 хоста — одна колонка (крупные ячейки), 3–4 — две,
        // 5+ — три, чтобы терминалы оставались читаемыми
        cols := 1
        switch {
        case len(ips) >= 5:
                cols = 3
        case len(ips) >= 3:
                cols = 2
        }
        suWanted := u.suC.GetActive()
        for i, ip := range ips {
                s := &termSession{ip: ip, alive: false, send: true, suWanted: suWanted}
                s.cell = u.newTermCellFor(s)
                u.sessions[ip] = s
                row, col := i/cols, i%cols
                box := s.cell.box
                // everything UI-related first, THEN the session goroutine —
                // otherwise "подключено" header appends outrun the placeholder
                glibIdle(func() {
                        if winMode {
                                // v1.12.0: каждое окно отдельно — перемещение,
                                // сворачивание и разворачивание работают
                                u.openTermWindow(s)
                        } else {
                                u.grid.Attach(box, col, row, 1, 1)
                                u.grid.ShowAll()
                        }
                        u.setHead(s, "◌", "#60a5fa", "подключение…")
                        goSafe("терминал: сессия", func() { u.runSession(s) })
                })
        }
        u.refreshBcastLabel()
        u.updatePlaceholder()
        logf("info", "⌨ Терминалы: открывается %d сессий (VTE); su → root: %v; режим: %s",
                len(ips), suWanted, map[bool]string{true: "отдельные окна", false: "сетка"}[winMode])
}

func resetGrid(old *gtk.Grid) *gtk.Grid {
        parent, err := old.GetParent()
        if err == nil {
                if vp, ok := parent.(*gtk.Viewport); ok {
                        vp.Remove(old)
                        g, _ := gtk.GridNew()
                        g.SetRowSpacing(8)
                        g.SetColumnSpacing(8)
                        g.SetMarginTop(4)
                        vp.Add(g)
                        return g
                }
        }
        return old
}

// newTermCellFor builds one terminal pane: [⇉] статус [✕] + VTE.
// PTY создаётся сразу (в UI-потоке) — мосту остаётся только связать
// его с ssh-сессией.
func (u *MultiTermUI) newTermCellFor(s *termSession) termCell {
        box := boxNew(gtk.ORIENTATION_VERTICAL, 2)
        addClass(box, "termcell")

        // --- заголовок ячейки: галочка «⇉», статус, кнопка «✕» ---
        headRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        sendC, _ := gtk.CheckButtonNewWithLabel("⇉")
        sendC.SetActive(true)
        sendC.SetTooltipText("Отправлять broadcast-команды в этот терминал (снимите галочку, чтобы исключить хост)")
        sendC.Connect("toggled", func() {
                s.send = sendC.GetActive()
                u.refreshBcastLabel()
        })
        addClass(sendC, "termsend")
        head, _ := gtk.LabelNew("")
        head.SetXAlign(0)
        addClass(head, "termhead")
        closeBtn, _ := gtk.ButtonNewWithLabel("✕")
        addClass(closeBtn, "termclose")
        closeBtn.SetTooltipText("Закрыть только этот терминал")
        closeBtn.Connect("clicked", func() { u.closeOne(s) })
        // ⤢ (v1.12.0): открепить ячейку сетки в отдельное окно — его можно
        // перемещать, сворачивать и разворачивать как обычное окно
        popBtn, _ := gtk.ButtonNewWithLabel("⤢")
        addClass(popBtn, "termclose")
        popBtn.SetTooltipText("Открепить этот терминал в отдельное окно\n(окно можно перемещать, сворачивать и разворачивать)")
        popBtn.Connect("clicked", func() { u.detachToWindow(s) })
        headRow.PackStart(sendC, false, false, 0)
        headRow.PackStart(head, true, true, 0)
        headRow.PackEnd(closeBtn, false, false, 0)
        headRow.PackEnd(popBtn, false, false, 0)

        // --- настоящий терминал VTE ---
        vteBox := boxNew(gtk.ORIENTATION_HORIZONTAL, 0)
        vteBox.SetSizeRequest(430, 260)
        v := newVTE(vteBox, isLight())
        // при изменении размера сетки — сообщаем ssh-сессии (могла ещё
        // не существовать — тогда запоминаем и применяем при подключении)
        v.mu.Lock()
        v.onResize = func(rows, cols int) {
                if s.sess != nil {
                        _ = s.sess.WindowChange(rows, cols)
                } else {
                        s.pendRows, s.pendCols = rows, cols
                }
        }
        v.mu.Unlock()

        box.PackStart(headRow, false, false, 0)
        box.PackStart(vteBox, true, true, 0)
        return termCell{box: box, head: head, send: sendC, vteBox: vteBox, vte: v}
}

// openTermWindow открывает терминал сессии в отдельном окне оконного
// менеджера (v1.12.0): окно можно свободно перемещать, сворачивать и
// разворачивать, при закрытии окна сессия гасится.
func (u *MultiTermUI) openTermWindow(s *termSession) {
        if s == nil || s.cell.win != nil {
                return
        }
        win, err := gtk.WindowNew(gtk.WINDOW_TOPLEVEL)
        if err != nil {
                return
        }
        title := s.ip + " — Терминал"
        if s.user != "" {
                title = s.user + "@" + s.ip + " — Терминал"
        }
        win.SetTitle(AppName + " · " + title)
        win.SetDefaultSize(880, 540)
        win.SetPosition(gtk.WIN_POS_NONE)
        win.Add(s.cell.box)
        win.Connect("delete-event", func() bool {
                u.closeOne(s)
                return true
        })
        s.cell.win = win
        win.ShowAll()
}

// detachToWindow открепляет ячейку сетки в отдельное окно (кнопка ⤢).
func (u *MultiTermUI) detachToWindow(s *termSession) {
        if s == nil || s.cell.win != nil {
                return
        }
        glibIdle(func() {
                if u.grid != nil && s.cell.box != nil {
                        u.grid.Remove(s.cell.box)
                }
                u.openTermWindow(s)
        })
        logf("info", "⌨ Терминал %s откреплён в отдельное окно", s.ip)
}

// updatePlaceholder обновляет подсказку первой сетки (когда терминалы
// открыты в отдельных окнах — показывает их количество).
func (u *MultiTermUI) updatePlaceholder() {
        glibIdle(func() {
                if u.ph == nil {
                        return
                }
                u.mu.Lock()
                n := len(u.sessions)
                u.mu.Unlock()
                if n > 0 {
                        u.ph.SetText(fmt.Sprintf("Открыто терминалов: %d — каждый в отдельном окне: перемещается, сворачивается, разворачивается.\nBroadcast-строка внизу уходит во все терминалы с галочкой ⇉. «Закрыть все» гасит все окна.", n))
                }
        })
}

// setHead updates the per-cell status line: marker, color and suffix text.
func (u *MultiTermUI) setHead(s *termSession, marker, color, suffix string) {
        if s == nil || s.cell.head == nil {
                return
        }
        glibIdle(func() {
                s.cell.head.SetMarkup("<span foreground=\"" + color + "\">" + marker + "</span>  " +
                        "<b>" + xmlEscape(s.user+"@"+s.ip) + "</b>" +
                        "  <span foreground=\"" + color + "\" size=\"smaller\">" + xmlEscape(suffix) + "</span>")
        })
}

func xmlEscape(s string) string {
        r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
        return r.Replace(s)
}

// termMsg печатает служебное сообщение в VTE ячейки.
func (u *MultiTermUI) termMsg(s *termSession, text string) {
        glibIdle(func() {
                if s.cell.vte != nil {
                        s.cell.vte.feed(text)
                }
        })
}

// termMsgTag — совместимость по цвету: dim/okc/err как маркер строки.
func (u *MultiTermUI) termMsgTag(s *termSession, text, tag string) {
        u.termMsg(s, text)
}

func (u *MultiTermUI) runSession(s *termSession) {
        // паника в goroutine сессии убивала весь процесс (жалоба: «сама
        // программа иногда вылетает») — теперь пишем в журнал и гасим сессию
        defer func() {
                if r := recover(); r != nil {
                        logf("err", "⌨ Сбой сессии %s: %v", s.ip, r)
                        s.alive = false
                        u.setHead(s, "✕", "#f87171", "внутренний сбой сессии")
                }
        }()
        creds := buildCreds(s.ip)
        s.user = creds.User
        u.setHead(s, "◌", "#60a5fa", "подключение…")
        u.termMsg(s, "Подключение к "+s.ip+"…\n")
        cl, err := Connect(creds)
        if err != nil {
                u.setHead(s, "✕", "#f87171", "ошибка подключения")
                u.termMsgTag(s, fmt.Sprintf("ОШИБКА подключения: %v\nПроверьте адрес, доступы и сеть.\n", err), "err")
                glibIdle(func() { app.hostsUI.updateStatusRow(s.ip, "fail") })
                return
        }
        s.cl = cl
        // заголовок отдельного окна — логин@хост
        glibIdle(func() {
                if s.cell.win != nil {
                        s.cell.win.SetTitle(AppName + " · " + s.user + "@" + s.ip + " — Терминал")
                }
        })
        sess, err := cl.NewSession()
        if err != nil {
                u.setHead(s, "✕", "#f87171", "нет сессии")
                u.termMsgTag(s, fmt.Sprintf("ОШИБКА session: %v\n", err), "err")
                cl.Close()
                return
        }
        s.sess = sess
        modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
        rows, cols := 30, 100
        if s.pendRows > 0 {
                rows = s.pendRows
        }
        if s.pendCols > 0 {
                cols = s.pendCols
        }
        if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
                u.setHead(s, "✕", "#f87171", "нет PTY")
                u.termMsgTag(s, fmt.Sprintf("ОШИБКА pty: %v\n", err), "err")
                cl.Close()
                return
        }
        stdin, _ := sess.StdinPipe()
        s.stdin = stdin
        stdout, _ := sess.StdoutPipe()

        // применить размер, накопленный до подключения
        if s.pendRows > 0 && s.pendCols > 0 {
                _ = sess.WindowChange(s.pendRows, s.pendCols)
                s.pendRows, s.pendCols = 0, 0
        }

        // мост ssh ↔ VTE PTY (PTY уже создан при создании ячейки)
        s.bridge = startPTYBridge(s.cell.vte, stdout, stdin, func(chunk []byte) { s.pushOut(chunk) })
        if s.bridge == nil {
                u.setHead(s, "✕", "#f87171", "PTY VTE недоступен")
                u.termMsgTag(s, "Не удалось создать PTY VTE (libvte).\n", "err")
                cl.Close()
                return
        }

        suffix := "печать прямо в терминал"
        u.setHead(s, "●", "#34d399", suffix)
        u.termMsgTag(s, "Подключено: "+creds.User+"@"+s.ip+"\nПечатайте прямо в этом терминале; mc/top работают как в обычном терминале.\n", "dim")
        s.alive = true
        glibIdle(func() { u.refreshBcastLabel() })

        if err := sess.Shell(); err != nil {
                u.setHead(s, "✕", "#f87171", "shell недоступен")
                u.termMsgTag(s, fmt.Sprintf("ОШИБКА shell: %v\n", err), "err")
                s.alive = false
                u.stopSession(s)
                cl.Close()
                return
        }

        // --- su → root (v1.11.0): вход под учётной записью, затем su ---
        if s.suWanted && creds.RootPass != "" && creds.User != "root" {
                if u.suEscalate(s, creds.RootPass) {
                        s.suActive = true
                        u.setHead(s, "●", "#fbbf24", "root (su)")
                        u.termMsgTag(s, "── выполнен su → root ──\n", "dim")
                        logf("ok", "🔑 %s: повышены права до root (su)", s.ip)
                } else {
                        u.setHead(s, "●", "#fbbf24", "su не выполнен — можно ввести вручную")
                        u.termMsgTag(s, "su → root не выполнен автоматически (нет приглашения или неверный пароль).\nВведите `su -` вручную, если нужно.\n", "dim")
                }
        } else if s.suWanted && creds.User == "root" {
                s.suActive = true
                u.setHead(s, "●", "#fbbf24", "root (вход root)")
        }

        _ = sess.Wait()
        s.alive = false
        glibIdle(func() { u.refreshBcastLabel() })
        if !s.closed {
                u.setHead(s, "○", "#8b93a5", "сессия закрыта")
                u.termMsgTag(s, "\n── сессия закрыта ──\n", "dim")
        }
        u.stopSession(s)
        cl.Close()
        glibIdle(func() { app.hostsUI.updateStatusRow(s.ip, "") })
}

// suEscalate выполняет `su -` и подставляет пароль root через мост.
func (u *MultiTermUI) suEscalate(s *termSession, rootPass string) bool {
        if s.stdin == nil {
                return false
        }
        passRe := regexp.MustCompile(`(?i)(пароль|password)`)
        mark := s.ringLen()
        if _, err := s.stdin.Write([]byte("su -\n")); err != nil {
                return false
        }
        if !s.waitRing(mark, passRe, 5*time.Second) {
                return false
        }
        time.Sleep(250 * time.Millisecond) // дать приглашению дорисоваться
        if _, err := s.stdin.Write([]byte(rootPass + "\n")); err != nil {
                return false
        }
        // неудача видна в выводе как «неверный пароль» / «Authentication failure»
        time.Sleep(1300 * time.Millisecond)
        s.ringMu.Lock()
        tail := s.ring[mark:]
        s.ringMu.Unlock()
        return !suFailRe.MatchString(tail)
}

// refreshBcastLabel показывает, скольким терминалам уйдёт broadcast-команда.
func (u *MultiTermUI) refreshBcastLabel() {
        glibIdle(func() {
                if u.bcastLbl == nil {
                        return
                }
                total, on := 0, 0
                u.mu.Lock()
                for _, s := range u.sessions {
                        total++
                        if s.send {
                                on++
                        }
                }
                u.mu.Unlock()
                u.bcastLbl.SetText(fmt.Sprintf("⌨  ВСЕМ ХОСТАМ (%d из %d):", on, total))
        })
}

func (u *MultiTermUI) broadcast(text string) {
        u.mu.Lock()
        n := 0
        skipped := 0
        for _, s := range u.sessions {
                if !s.alive {
                        continue
                }
                if !s.send {
                        skipped++
                        continue
                }
                if s.stdin != nil {
                        // команда уйдёт и в VTE — удалённая сторона её отэхо
                        if _, err := s.stdin.Write([]byte(text)); err == nil {
                                n++
                        }
                }
        }
        u.mu.Unlock()
        if n == 0 && skipped == 0 {
                logf("warn", "⌨ Broadcast: нет активных сессий — сначала «Подключить выбранные»")
                return
        }
        logf("cmd", "⌨ Broadcast → %d сессий: %s", n, strings.TrimSpace(text))
        if skipped > 0 {
                logf("info", "⌨ Пропущено терминалов (галочка ⇉ снята): %d", skipped)
        }
}

// stopSession гасит мост и ввод в терминал.
func (u *MultiTermUI) stopSession(s *termSession) {
        if s.bridge != nil {
                s.bridge.close()
        }
        glibIdle(func() {
                if s.cell.vte != nil {
                        s.cell.vte.setInputEnabled(false)
                }
        })
}

// closeOne закрывает ОДИН терминал: сессия гасится в фоне, ячейка/окно
// уничтожаются; когда терминалов не осталось — кнопка «Подключить» снова живая.
// Порядок важен: СНАЧАЛА vte.markDead() (гасит все C-вызовы в VTE), ПОТОМ
// Destroy виджета — иначе use-after-free и падение при многих терминалах.
func (u *MultiTermUI) closeOne(s *termSession) {
        if s == nil {
                return
        }
        u.mu.Lock()
        delete(u.sessions, s.ip)
        empty := len(u.sessions) == 0
        u.mu.Unlock()
        s.closed = true
        u.stopSession(s)
        s.cell.vte.markDead()
        go func() {
                defer crashRecover("терминал: kill")
                if s.alive && s.sess != nil {
                        _ = s.sess.Signal(ssh.SIGKILL)
                        _ = s.sess.Close()
                }
                if s.cl != nil {
                        s.cl.Close()
                }
                s.alive = false
        }()
        glibIdle(func() {
                if s.cell.win != nil {
                        s.cell.win.Destroy()
                        s.cell.win = nil
                }
                if u.grid != nil && s.cell.box != nil {
                        u.grid.Remove(s.cell.box)
                        s.cell.box.Destroy()
                }
                u.refreshBcastLabel()
                u.updatePlaceholder()
        })
        if empty {
                u.running = false
        }
        logf("info", "⏹ Терминал %s закрыт (осталось: %d)", s.ip, func() int {
                u.mu.Lock()
                defer u.mu.Unlock()
                return len(u.sessions)
        }())
}

func (u *MultiTermUI) closeSession(s *termSession) {
        if s == nil {
                return
        }
        s.closed = true
        u.stopSession(s)
        if s.alive && s.sess != nil {
                _ = s.sess.Signal(ssh.SIGKILL)
                _ = s.sess.Close()
        }
        if s.cl != nil {
                s.cl.Close()
        }
        s.alive = false
        s.cell.vte.markDead()
        // отдельное окно гасим сразу (v1.12.0)
        if s.cell.win != nil {
                w := s.cell.win
                s.cell.win = nil
                glibIdle(func() { w.Destroy() })
        }
}

func (u *MultiTermUI) closeAll() {
        u.mu.Lock()
        n := 0
        for _, s := range u.sessions {
                if s.alive {
                        n++
                }
                u.closeSession(s)
        }
        u.sessions = map[string]*termSession{}
        u.running = false
        u.mu.Unlock()
        if n > 0 {
                logf("info", "⏹ Все терминалы закрыты (%d)", n)
        }
        glibIdle(func() { u.refreshBcastLabel() })
}

var (
        _ = strconv.Itoa
        _ = io.EOF
)
