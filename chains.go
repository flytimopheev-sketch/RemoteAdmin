package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// ChainsUI — ordered execution of templates with success/failure policies
// and rollback commands.
type ChainsUI struct {
	widget  gtk.IWidget
	tree    *gtk.TreeView
	st      *gtk.ListStore
	detail  *gtk.Label
	running bool
}

const (
	CHCOL_NAME  = 0
	CHCOL_STEPS = 1
)

func newChainsUI() *ChainsUI {
	u := &ChainsUI{}
	root := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)

	leftCard, leftBox := cardBox("Цепочки задач")
	u.tree, u.st = makeTree([]string{"Имя", "Шагов"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING})
	sel, _ := u.tree.GetSelection()
	sel.SetMode(gtk.SELECTION_SINGLE)
	sel.Connect("changed", func() { u.showDetail() })
	u.tree.Connect("key-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
		ke := gdk.EventKeyNewFromEvent(ev)
		if ke.KeyVal() == gdk.KEY_Delete {
			u.deleteSelected()
			return true
		}
		return false
	})
	sc := scrolled(u.tree, 0)
	sc.SetVExpand(true)
	leftBox.PackStart(sc, true, true, 0)

	btns := boxNew(gtk.ORIENTATION_VERTICAL, 3)
	btnNew := buttonNew("Новая цепочка")
	btnNew.Connect("clicked", func() { u.editChain(nil) })
	btnRun := buttonNew("Запустить")
	sr, _ := btnRun.GetStyleContext()
	sr.AddClass("suggested")
	btnRun.Connect("clicked", func() { u.runSelected() })
	btnEdit := buttonNew("Изменить")
	btnEdit.Connect("clicked", func() {
		if c := u.selected(); c != nil {
			u.editChain(c)
		}
	})
	btnDel := buttonNew("Удалить")
	btnDel.Connect("clicked", func() { u.deleteSelected() })
	btns.PackStart(btnNew, false, false, 0)
	btns.PackStart(btnRun, false, false, 0)
	btns.PackStart(btnEdit, false, false, 0)
	btns.PackStart(btnDel, false, false, 0)
	leftBox.PackStart(btns, false, false, 0)

	rightCard, rightBox := cardBox("Состав цепочки")
	u.detail = labelNew("Цепочка = последовательность шаблонов.\nПолитика: при успехе — продолжить/стоп,\nпри ошибке — стоп/продолжить/откат (rollback).")
	u.detail.SetXAlign(0)
	u.detail.SetLineWrap(true)
	u.detail.SetVAlign(gtk.ALIGN_START)
	dsc := scrolled(u.detail, 0)
	dsc.SetVExpand(true)
	rightBox.PackStart(dsc, true, true, 0)

	root.PackStart(leftCard, true, true, 0)
	root.PackStart(rightCard, true, true, 0)

	u.refresh()
	u.widget = root
	return u
}

func (u *ChainsUI) refresh() {
	if u.st == nil {
		return
	}
	u.st.Clear()
	for _, c := range store.State.Chains {
		iter := u.st.Append()
		_ = u.st.Set(iter, []int{CHCOL_NAME, CHCOL_STEPS}, []interface{}{c.Name, fmt.Sprintf("%d", len(c.Steps))})
	}
}

func (u *ChainsUI) selected() *Chain {
	m, iter, ok := selIter(u.tree)
	if !ok {
		return nil
	}
	name := iterGetString(m, iter, CHCOL_NAME)
	return store.ChainByName(name)
}

func (u *ChainsUI) showDetail() {
	c := u.selected()
	if c == nil {
		return
	}
	var b strings.Builder
	b.WriteString("<b>" + c.Name + "</b>\n\n")
	for i, s := range c.Steps {
		t := store.TemplateByName(s.Template)
		desc := s.Template
		if t != nil {
			desc = fmt.Sprintf("%s (%d задач)", s.Template, len(t.Tasks))
		} else {
			desc += " [шаблон не найден!]"
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, desc)
		fmt.Fprintf(&b, "   успех → %s | ошибка → %s\n", ruPolicy(s.OnSuccess, "success"), ruPolicy(s.OnFailure, "failure"))
		if s.OnFailure == "rollback" && len(s.Rollback) > 0 {
			b.WriteString("   откат: " + strings.Join(s.Rollback, "; ") + "\n")
		}
	}
	u.detail.SetMarkup(b.String())
}

func ruPolicy(v, kind string) string {
	switch v {
	case "continue":
		return "продолжить"
	case "stop":
		if kind == "success" {
			return "остановить цепочку"
		}
		return "остановить"
	case "rollback":
		return "откат (rollback)"
	}
	return v
}

func (u *ChainsUI) deleteSelected() {
	c := u.selected()
	if c == nil {
		return
	}
	if !confirmDialog(app.window, "Удалить цепочку «"+c.Name+"»?") {
		return
	}
	out := store.State.Chains[:0]
	for _, cc := range store.State.Chains {
		if cc.Name != c.Name {
			out = append(out, cc)
		}
	}
	store.State.Chains = out
	_ = store.Save()
	u.refresh()
	logf("ok", "🗑 Цепочка «%s» удалена", c.Name)
}

// editChain opens a builder dialog.
func (u *ChainsUI) editChain(existing *Chain) {
	if len(store.State.Templates) == 0 {
		warnDialog(app.window, "Сначала создайте хотя бы один шаблон на вкладке «Шаблоны»")
		return
	}
	d, err := gtk.DialogNewWithButtons(
		"Цепочка задач", app.window, gtk.DIALOG_MODAL,
		[]interface{}{"Сохранить", gtk.RESPONSE_ACCEPT},
		[]interface{}{"Отмена", gtk.RESPONSE_CANCEL},
	)
	if err != nil {
		return
	}
	defer d.Destroy()
	d.SetDefaultSize(640, 520)
	box, _ := d.GetContentArea()

	nameE := entryNew("Имя цепочки")
	if existing != nil {
		nameE.SetText(existing.Name)
	}
	box.PackStart(nameE, false, false, 4)

	steps := boxNew(gtk.ORIENTATION_VERTICAL, 6)
	stepRows := make([]*chainStepRow, 0)

	addRow := func(s *ChainStep) {
		row := newChainStepRow()
		if s != nil {
			row.load(s)
		}
		stepRows = append(stepRows, row)
		steps.PackStart(row.w, false, false, 0)
		row.w.ShowAll()
	}
	if existing != nil {
		for i := range existing.Steps {
			addRow(&existing.Steps[i])
		}
	} else {
		addRow(nil)
	}

	btnAddStep := buttonNew("Добавить шаг")
	btnAddStep.Connect("clicked", func() { addRow(nil) })
	box.PackStart(steps, true, true, 4)
	box.PackStart(btnAddStep, false, false, 4)
	box.ShowAll()

	if d.Run() == gtk.RESPONSE_ACCEPT {
		name := strings.TrimSpace(entText(nameE))
		if name == "" {
			warnDialog(app.window, "Укажите имя цепочки")
			return
		}
		var newSteps []ChainStep
		for _, r := range stepRows {
			s := r.collect()
			if s.Template == "" || s.Template == "— выберите шаблон —" {
				continue
			}
			newSteps = append(newSteps, s)
		}
		if len(newSteps) == 0 {
			warnDialog(app.window, "Добавьте хотя бы один шаг с шаблоном")
			return
		}
		replaced := false
		for i := range store.State.Chains {
			if store.State.Chains[i].Name == name {
				store.State.Chains[i].Steps = newSteps
				replaced = true
			}
		}
		if !replaced {
			store.State.Chains = append(store.State.Chains, Chain{Name: name, Steps: newSteps})
		}
		_ = store.Save()
		u.refresh()
		logf("ok", "💾 Цепочка «%s» сохранена (%d шагов)", name, len(newSteps))
	}
}

// chainStepRow is one editable step inside the chain dialog.
type chainStepRow struct {
	w      *gtk.Frame
	tpl    *gtk.ComboBoxText
	onSucc *gtk.ComboBoxText
	onFail *gtk.ComboBoxText
	rollE  *gtk.TextView
}

func newChainStepRow() *chainStepRow {
	r := &chainStepRow{}
	f, _ := gtk.FrameNew("Шаг")
	inner := boxNew(gtk.ORIENTATION_VERTICAL, 3)
	inner.SetMarginStart(6)
	inner.SetMarginEnd(6)
	inner.SetMarginTop(4)
	inner.SetMarginBottom(4)

	row1 := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	r.tpl, _ = gtk.ComboBoxTextNew()
	for _, t := range store.State.Templates {
		r.tpl.AppendText(t.Name)
	}
	if r.tpl.GetActive() < 0 && len(store.State.Templates) > 0 {
		r.tpl.SetActive(0)
	}
	row1.PackStart(labelNew("Шаблон:"), false, false, 0)
	row1.PackStart(r.tpl, true, true, 0)

	r.onSucc, _ = gtk.ComboBoxTextNew()
	r.onSucc.AppendText("Продолжить")
	r.onSucc.AppendText("Остановить")
	r.onSucc.SetActive(0)
	row1.PackStart(labelNew("Успех:"), false, false, 0)
	row1.PackStart(r.onSucc, false, false, 0)

	r.onFail, _ = gtk.ComboBoxTextNew()
	r.onFail.AppendText("Остановить")
	r.onFail.AppendText("Продолжить")
	r.onFail.AppendText("Откат (rollback)")
	r.onFail.SetActive(0)
	row1.PackStart(labelNew("Ошибка:"), false, false, 0)
	row1.PackStart(r.onFail, false, false, 0)
	inner.PackStart(row1, false, false, 0)

	row2 := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	row2.PackStart(labelNew("Rollback-команды:"), false, false, 0)
	r.rollE, _ = gtk.TextViewNew()
	tvSet(r.rollE, 40)
	row2.PackStart(scrolled(r.rollE, 44), true, true, 0)
	inner.PackStart(row2, false, false, 0)

	f.Add(inner)
	r.w = f
	return r
}

func (r *chainStepRow) load(s *ChainStep) {
	for i, t := range store.State.Templates {
		if t.Name == s.Template {
			r.tpl.SetActive(i)
			break
		}
	}
	switch s.OnSuccess {
	case "stop":
		r.onSucc.SetActive(1)
	default:
		r.onSucc.SetActive(0)
	}
	switch s.OnFailure {
	case "continue":
		r.onFail.SetActive(1)
	case "rollback":
		r.onFail.SetActive(2)
	default:
		r.onFail.SetActive(0)
	}
	tvSetText(r.rollE, strings.Join(s.Rollback, "\n"))
}

func (r *chainStepRow) collect() ChainStep {
	s := ChainStep{}
	s.Template = r.tpl.GetActiveText()
	switch r.onSucc.GetActive() {
	case 1:
		s.OnSuccess = "stop"
	default:
		s.OnSuccess = "continue"
	}
	switch r.onFail.GetActive() {
	case 1:
		s.OnFailure = "continue"
	case 2:
		s.OnFailure = "rollback"
	default:
		s.OnFailure = "stop"
	}
	for _, line := range strings.Split(tvText(r.rollE), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			s.Rollback = append(s.Rollback, line)
		}
	}
	return s
}

// runSelected executes the chain step by step.
func (u *ChainsUI) runSelected() {
	c := u.selected()
	if c == nil {
		return
	}
	if u.running {
		warnDialog(app.window, "Цепочка уже выполняется")
		return
	}
	ips := app.hostsUI.targetHosts()
	if len(ips) == 0 {
		warnDialog(app.window, "Нет хостов для выполнения")
		return
	}
	if !needCreds() {
		return
	}
	u.running = true
	logf("accent", "🔗 Цепочка «%s»: %d шагов на %d хост(ов)", c.Name, len(c.Steps), len(ips))
	go func() {
		defer func() { u.running = false }()
		u.executeChain(*c, ips)
	}()
}

func (u *ChainsUI) executeChain(chain Chain, ips []string) {
	rollbackHosts := ips
	for i, step := range chain.Steps {
		tpl := store.TemplateByName(step.Template)
		if tpl == nil {
			logf("fail", "❌ Шаг %d: шаблон «%s» не найден — цепочка остановлена", i+1, step.Template)
			return
		}
		logf("info", "▶ Шаг %d/%d: шаблон «%s» …", i+1, len(chain.Steps), step.Template)
		ok, fail := u.runStepSync(tpl, ips)
		app.setStatus(fmt.Sprintf("Цепочка «%s»: шаг %d/%d — ОК %d, ошибок %d", chain.Name, i+1, len(chain.Steps), ok, fail))
		failed := fail > 0 && ok == 0 // step considered failed if ALL hosts failed
		switch {
		case !failed:
			if step.OnSuccess == "stop" {
				logf("info", "⏹ Политика шага %d: стоп после успеха", i+1)
				return
			}
		case failed && step.OnFailure == "rollback":
			logf("warn", "↩ Откат после ошибки шага %d…", i+1)
			u.runRollback(step.Rollback, rollbackHosts)
			return
		case failed && step.OnFailure == "continue":
			logf("warn", "⚠ Ошибка шага %d — продолжаем по политике", i+1)
		default: // failed && stop
			logf("fail", "❌ Ошибка шага %d — цепочка остановлена", i+1)
			return
		}
	}
	logf("accent", "🏁 Цепочка «%s» завершена", chain.Name)
	Notify("Цепочка «"+chain.Name+"»", "Все шаги выполнены")
}

// runStepSync runs template tasks on hosts and waits for completion.
func (u *ChainsUI) runStepSync(tpl *Template, ips []string) (okN, failN int) {
	r := u.runTemplateSync(tpl, ips)
	return r[0], r[1]
}

func (u *ChainsUI) runTemplateSync(tpl *Template, ips []string) [2]int {
	okN, failN := 0, 0
	var mu sync.Mutex
	wg := sync.WaitGroup{}
	sem := make(chan struct{}, maxInt(store.State.Settings.MaxParallel, 8))
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			creds := buildCreds(ip)
			cl, err := Connect(creds)
			if err != nil {
				mu.Lock()
				failN++
				mu.Unlock()
				addReport("Цепочка: "+tpl.Name, ip, "FAIL", "подключение: "+err.Error())
				glibIdle(func() { app.hostsUI.updateStatusRow(ip, "fail") })
				return
			}
			cl.Close()
			out, err := runAllTasksOnHost(ip, tpl.Tasks, creds)
			if err != nil {
				mu.Lock()
				failN++
				mu.Unlock()
				addReport("Цепочка: "+tpl.Name, ip, "FAIL", out+"\nОшибка: "+err.Error())
				glibIdle(func() { app.hostsUI.updateStatusRow(ip, "fail") })
				return
			}
			mu.Lock()
			okN++
			mu.Unlock()
			addReport("Цепочка: "+tpl.Name, ip, "OK", out)
			glibIdle(func() { app.hostsUI.updateStatusRow(ip, "ok") })
		}(ip)
	}
	wg.Wait()
	return [2]int{okN, failN}
}

func (u *ChainsUI) runRollback(cmds []string, ips []string) {
	if len(cmds) == 0 {
		return
	}
	logf("warn", "↩ Rollback-команды на %d хост(ов): %s", len(ips), strings.Join(cmds, "; "))
	for _, ip := range ips {
		for _, cmd := range cmds {
			out, err := execOnHost(ip, cmd, true)
			if err != nil {
				logf("fail", "❌ Rollback %s: %v", ip, err)
			} else {
				logf("ok", "↩ Rollback %s: %s", ip, truncateOut(out, 120))
			}
		}
	}
	logf("ok", "↩ Rollback завершён")
}

// runAllTasksOnHost opens a connection and runs all template tasks sequentially.
func runAllTasksOnHost(ip string, tasks []PlaybookTask, creds SSHCreds) (string, error) {
	cl, err := Connect(creds)
	if err != nil {
		return "", err
	}
	defer cl.Close()
	var b strings.Builder
	for i, t := range tasks {
		out, err := execTask(cl, ip, t, creds)
		fmt.Fprintf(&b, "[%d/%d] %s\n%s\n", i+1, len(tasks), t.Describe(), strings.TrimSpace(out))
		if err != nil {
			return b.String(), err
		}
	}
	return b.String(), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
