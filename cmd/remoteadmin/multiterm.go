package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/gtk"
	"golang.org/x/crypto/ssh"
)

// MultiTermUI — ClusterSSH-like grid: one shell per host,
// broadcast input to all sessions.
//
// Каждая ячейка: заголовок «⇉ ● user@host — статус  [✕]», где
//   ⇉  — галочка «отправлять в этот терминал broadcast-команды»;
//   ●  — маркер состояния сессии (цвет = состояние);
//   ✕  — закрыть только этот терминал.
// Вывод рендерится с ANSI-цветами (xterm-256color → TextTag), поэтому
// терминал выглядит как настоящий: ошибки красным, пути синим и т.д.
type MultiTermUI struct {
	widget   gtk.IWidget
	grid     *gtk.Grid
	input    *gtk.Entry
	bcastLbl *gtk.Label
	sessions map[string]*termSession
	mu       sync.Mutex
	running  bool
}

// termCell bundles the widgets of one terminal pane.
type termCell struct {
	box  *gtk.Box       // framed wrapper attached into the grid
	head *gtk.Label     // status line
	send *gtk.CheckButton // «⇉ отправлять сюда broadcast»
	view *gtk.TextView
	buf  *gtk.TextBuffer
}

type termSession struct {
	ip    string
	user  string
	cl    *ssh.Client
	sess  *ssh.Session
	stdin io.WriteCloser
	cell  termCell
	tc    *ansiTagCache
	pend  string // хвост с обрезанной escape-последовательностью
	send  bool   // получать broadcast-команды
	alive bool
	closed bool
}

// ansiTagCache создаёт TextTag-ы для ANSI-цветов лениво, кэширует по ключу.
type ansiTagCache struct {
	buf *gtk.TextBuffer
	m   map[string]string
}

func newTagCache(buf *gtk.TextBuffer) *ansiTagCache {
	return &ansiTagCache{buf: buf, m: map[string]string{}}
}

func (tc *ansiTagCache) tagFor(color string, bold, dim bool) string {
	if dim && color == "" {
		color = "#8b93a5"
	}
	key := color + "|" + strconv.FormatBool(bold) + "|" + strconv.FormatBool(dim)
	if n, ok := tc.m[key]; ok {
		return n
	}
	props := map[string]interface{}{}
	if color != "" {
		props["foreground"] = color
	}
	if bold {
		props["weight"] = 700
	}
	name := fmt.Sprintf("ansi%d", len(tc.m))
	tc.buf.CreateTag(name, props)
	tc.m[key] = name
	return name
}

func newMultiTermUI() *MultiTermUI {
	u := &MultiTermUI{sessions: map[string]*termSession{}}
	root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

	toolbar := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	btnStart := buttonNew("Подключить выбранные")
	ss, _ := btnStart.GetStyleContext()
	ss.AddClass("suggested")
	btnStart.SetTooltipText("Открыть по одной интерактивной сессии на каждый хост, выбранный на вкладке «Хосты»")
	btnStart.Connect("clicked", func() { u.startAll() })
	btnStop := buttonNew("Закрыть все")
	btnStop.SetTooltipText("Закрыть все активные сессии")
	btnStop.Connect("clicked", func() { u.closeAll() })
	toolbar.PackStart(btnStart, false, false, 0)
	toolbar.PackStart(btnStop, false, false, 0)

	hint := labelNew("⇉ на терминале — получать команды broadcast, ✕ — закрыть только этот терминал; ошибки — красным, ANSI-цвета сохраняются")
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
	ph := labelNew("Нет активных сессий.\n\n1. Отметьте хосты на вкладке «Хосты»\n2. Нажмите «Подключить выбранные»\n3. Вводьте команды в поле Broadcast внизу\n\nГалочка ⇉ — кому отправлять команду, ✕ — закрыть терминал")
	ph.SetJustify(gtk.JUSTIFY_CENTER)
	ph.SetHAlign(gtk.ALIGN_CENTER)
	ph.SetVAlign(gtk.ALIGN_CENTER)
	addClass(ph, "ramuted")
	u.grid.Attach(ph, 0, 0, 1, 1)

	inputRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
	addClass(inputRow, "broadcast")
	u.bcastLbl = labelNew("⌨  ВСЕМ ХОСТАМ (0 из 0):")
	u.input, _ = gtk.EntryNew()
	u.input.SetPlaceholderText("Команда для выбранных терминалов… (Enter — отправить)")
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
	u.running = true
	// close old sessions synchronously (network-only ops, no widgets);
	// NB: the sessions map MUST be rebuilt here — doing it inside glibIdle
	// raced with the loop below and broadcast lost every fresh session.
	for _, s := range u.sessions {
		u.closeSession(s)
	}
	u.sessions = map[string]*termSession{}
	glibIdle(func() { u.grid = resetGrid(u.grid) })
	// сетка: 1–2 хоста — одна колонка (крупные ячейки), 3–4 — две,
	// 5+ — три, чтобы терминалы оставались читаемыми
	cols := 1
	switch {
	case len(ips) >= 5:
		cols = 3
	case len(ips) >= 3:
		cols = 2
	}
	for i, ip := range ips {
		s := &termSession{ip: ip, alive: false, send: true}
		s.cell = u.newTermCellFor(s)
		u.sessions[ip] = s
		row, col := i/cols, i%cols
		box := s.cell.box
		// everything UI-related first, THEN the session goroutine —
		// otherwise "подключено" header appends outrun the placeholder
		glibIdle(func() {
			u.grid.Attach(box, col, row, 1, 1)
			u.setHead(s, "◌", "#60a5fa", "подключение…")
			u.termAppend(s, "Подключение к "+ip+"…\n")
			u.grid.ShowAll()
			go u.runSession(s)
		})
	}
	u.refreshBcastLabel()
	logf("info", "⌨ Терминалы: открывается %d сессий, ввод — broadcast (можно отключать галочкой ⇉)", len(ips))
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

// newTermCellFor builds one terminal pane: [⇉] статус [✕] + monospace view.
func (u *MultiTermUI) newTermCellFor(s *termSession) termCell {
	tv, _ := gtk.TextViewNew()
	tv.SetEditable(false)
	tv.SetWrapMode(gtk.WRAP_CHAR)
	tv.SetMonospace(true)
	tv.SetSizeRequest(360, 230)
	tv.SetLeftMargin(8)
	tv.SetRightMargin(8)
	tv.SetTopMargin(6)
	buf, _ := tv.GetBuffer()
	buf.CreateTag("dim", map[string]interface{}{"foreground": "#8b93a5"})
	buf.CreateTag("err", map[string]interface{}{"foreground": "#f87171"})
	buf.CreateTag("okc", map[string]interface{}{"foreground": "#34d399"})

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
	headRow.PackStart(sendC, false, false, 0)
	headRow.PackStart(head, true, true, 0)
	headRow.PackEnd(closeBtn, false, false, 0)

	sc := scrolled(tv, 0)
	sc.SetVExpand(true)
	box.PackStart(headRow, false, false, 0)
	box.PackStart(sc, true, true, 0)
	return termCell{box: box, head: head, send: sendC, view: tv, buf: buf}
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

func (u *MultiTermUI) termAppend(s *termSession, text string) {
	u.termAppendTag(s, text, "")
}

func (u *MultiTermUI) termAppendTag(s *termSession, text, tag string) {
	glibIdle(func() {
		if s == nil || s.cell.buf == nil {
			return
		}
		iter := s.cell.buf.GetEndIter()
		if tag != "" {
			s.cell.buf.InsertWithTagByName(iter, text, tag)
		} else {
			s.cell.buf.Insert(iter, text)
		}
		trimTermBuffer(s)
		scrollTerm(s)
	})
}

// termAppendANSI рендерит вывод SSH с ANSI-кодами: сегменты вставляются за
// один проход основного цикла, обрезанная escape-последовательность (чанк
// разбил её посередине) копится в s.pend до следующего куска.
func (u *MultiTermUI) termAppendANSI(s *termSession, text string) {
	if s.tc == nil && s.cell.buf != nil {
		s.tc = newTagCache(s.cell.buf)
	}
	data := s.pend + text
	segs, rest := parseANSIStream(data)
	s.pend = rest
	if len(segs) == 0 {
		return
	}
	type chunk struct {
		text, tag string
	}
	chunks := make([]chunk, 0, len(segs))
	for _, sg := range segs {
		chunks = append(chunks, chunk{sg.Text, s.tc.tagFor(sg.Color, sg.Bold, sg.Dim)})
	}
	glibIdle(func() {
		if s == nil || s.cell.buf == nil {
			return
		}
		for _, c := range chunks {
			iter := s.cell.buf.GetEndIter()
			if c.tag != "" {
				s.cell.buf.InsertWithTagByName(iter, c.text, c.tag)
			} else {
				s.cell.buf.Insert(iter, c.text)
			}
		}
		trimTermBuffer(s)
		scrollTerm(s)
	})
}

func trimTermBuffer(s *termSession) {
	if s.cell.buf.GetCharCount() > 60000 {
		start := s.cell.buf.GetStartIter()
		end := s.cell.buf.GetIterAtOffset(20000)
		s.cell.buf.Delete(start, end)
	}
}

func scrollTerm(s *termSession) {
	mark := s.cell.buf.CreateMark("term", s.cell.buf.GetEndIter(), false)
	s.cell.view.ScrollToMark(mark, 0.0, true, 0.0, 1.0)
	s.cell.buf.DeleteMark(mark)
}

func (u *MultiTermUI) runSession(s *termSession) {
	creds := buildCreds(s.ip)
	s.user = creds.User
	u.setHead(s, "◌", "#60a5fa", "подключение…")
	cl, err := Connect(creds)
	if err != nil {
		u.setHead(s, "✕", "#f87171", "ошибка подключения")
		u.termAppendTag(s, fmt.Sprintf("ОШИБКА подключения: %v\nПроверьте адрес, доступы и сеть.\n", err), "err")
		glibIdle(func() { app.hostsUI.updateStatusRow(s.ip, "fail") })
		return
	}
	s.cl = cl
	sess, err := cl.NewSession()
	if err != nil {
		u.setHead(s, "✕", "#f87171", "нет сессии")
		u.termAppendTag(s, fmt.Sprintf("ОШИБКА session: %v\n", err), "err")
		cl.Close()
		return
	}
	s.sess = sess
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := sess.RequestPty("xterm-256color", 30, 100, modes); err != nil {
		u.setHead(s, "✕", "#f87171", "нет PTY")
		u.termAppendTag(s, fmt.Sprintf("ОШИБКА pty: %v\n", err), "err")
		cl.Close()
		return
	}
	stdin, _ := sess.StdinPipe()
	s.stdin = stdin
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()

	u.setHead(s, "●", "#34d399", "подключено — команды из Broadcast")
	u.termAppendTag(s, "Подключено: "+creds.User+"@"+s.ip+"\nВсё, что введено в поле Broadcast внизу, выполняется и здесь (если стоит галочка ⇉).\n", "dim")
	s.alive = true
	glibIdle(func() { u.refreshBcastLabel() })

	// readers: stdout — с ANSI-раскраской, stderr — красным
	go func() {
		r := bufio.NewReader(stdout)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				u.termAppendANSI(s, string(chunk[:n]))
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		r := bufio.NewReader(stderr)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				u.termAppendTag(s, string(chunk[:n]), "err")
			}
			if err != nil {
				return
			}
		}
	}()

	if err := sess.Shell(); err != nil {
		u.setHead(s, "✕", "#f87171", "shell недоступен")
		u.termAppendTag(s, fmt.Sprintf("ОШИБКА shell: %v\n", err), "err")
		s.alive = false
		cl.Close()
		return
	}
	_ = sess.Wait()
	s.alive = false
	glibIdle(func() { u.refreshBcastLabel() })
	if !s.closed {
		u.setHead(s, "○", "#8b93a5", "сессия закрыта")
		u.termAppendTag(s, "\n── сессия закрыта ──\n", "dim")
	}
	cl.Close()
	glibIdle(func() { app.hostsUI.updateStatusRow(s.ip, "") })
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
			// echo the command in the pane so it is clear WHAT was sent WHERE
			u.termAppendTag(s, "\n[all] $ "+strings.TrimRight(text, "\n")+"\n", "okc")
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

// closeOne закрывает ОДИН терминал: сессия гасится в фоне, ячейка убирается
// из сетки; когда терминалов не осталось — кнопка «Подключить» снова живая.
func (u *MultiTermUI) closeOne(s *termSession) {
	if s == nil {
		return
	}
	u.mu.Lock()
	delete(u.sessions, s.ip)
	empty := len(u.sessions) == 0
	u.mu.Unlock()
	s.closed = true
	go func() {
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
		if u.grid != nil && s.cell.box != nil {
			u.grid.Remove(s.cell.box)
			s.cell.box.Destroy()
		}
		u.refreshBcastLabel()
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
	if s.alive && s.sess != nil {
		_ = s.sess.Signal(ssh.SIGKILL)
		_ = s.sess.Close()
	}
	if s.cl != nil {
		s.cl.Close()
	}
	s.alive = false
}

func (u *MultiTermUI) closeAll() {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, s := range u.sessions {
		if s.alive {
			n++
		}
		u.closeSession(s)
	}
	u.sessions = map[string]*termSession{}
	u.running = false
	if n > 0 {
		logf("info", "⏹ Все терминалы закрыты (%d)", n)
	}
	glibIdle(func() { u.refreshBcastLabel() })
}

var _ = time.Second
