package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"golang.org/x/crypto/ssh"
)

var pbTypes = []struct {
	Key string
	Ru  string
}{
	{"command", "Команда"},
	{"script", "Скрипт"},
	{"copy", "Копировать"},
	{"replace", "Заменить файл"},
	{"replace_with_cmds", "Заменить + команды"},
	{"edit", "Редактировать"},
}

func pbTypeRu(key string) string {
	for _, t := range pbTypes {
		if t.Key == key {
			return t.Ru
		}
	}
	return key
}

// PlaybookUI is the task sequence editor tab.
type PlaybookUI struct {
	widget    gtk.IWidget
	tree      *gtk.TreeView
	st        *gtk.ListStore
	typeCombo *gtk.ComboBoxText
	form      *simpleGrid
	sourceE   *gtk.Entry
	sourceBtn *gtk.Button // «Обзор…» — выбрать локальный скрипт/файл
	destE     *gtk.Entry
	searchE   *gtk.Entry
	replaceE  *gtk.Entry
	serviceE  *gtk.Entry
	postE     *gtk.TextView
	rootC     *gtk.CheckButton
	formRows  map[string]*gtk.Widget
}

func newPlaybookUI() *PlaybookUI {
	u := &PlaybookUI{}
	root := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
	// left: task list
	leftCard, leftBox := cardBox("Последовательность задач")
	u.tree, u.st = makeTree([]string{"#", "Задача"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING})
	sel, _ := u.tree.GetSelection()
	sel.SetMode(gtk.SELECTION_SINGLE)
	sel.Connect("changed", func() { u.loadSelectedIntoForm() })
	u.tree.Connect("key-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
		ke := gdk.EventKeyNewFromEvent(ev)
		if ke.KeyVal() == gdk.KEY_Delete {
			u.removeSelected()
			return true
		}
		return false
	})
	sc, _ := gtk.ScrolledWindowNew(nil, nil)
	sc.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	sc.Add(u.tree)
	sc.SetVExpand(true)
	leftBox.PackStart(sc, true, true, 0)

	btnRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
	btnUp := buttonNew("↑")
	btnUp.Connect("clicked", func() { u.move(-1) })
	btnDown := buttonNew("↓")
	btnDown.Connect("clicked", func() { u.move(1) })
	btnDel := buttonNew("🗑")
	btnDel.Connect("clicked", func() { u.removeSelected() })
	btnRow.PackStart(btnUp, false, false, 0)
	btnRow.PackStart(btnDown, false, false, 0)
	btnRow.PackStart(btnDel, false, false, 0)
	leftBox.PackStart(btnRow, false, false, 0)

	runRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
	btnRun := buttonNew("Запустить playbook")
	scr, _ := btnRun.GetStyleContext()
	scr.AddClass("suggested")
	btnRun.Connect("clicked", func() { u.runPlaybook() })
	btnTpl := buttonNew("Сохранить как шаблон")
	btnTpl.Connect("clicked", func() { u.saveAsTemplate() })
	btnClear := buttonNew("Очистить")
	btnClear.Connect("clicked", func() {
		if confirmDialog(app.window, "Очистить весь список задач?") {
			store.State.Playbook = nil
			_ = store.Save()
			u.refresh()
		}
	})
	runRow.PackStart(btnRun, true, true, 0)
	runRow.PackStart(btnTpl, true, true, 0)
	runRow.PackStart(btnClear, false, false, 0)
	leftBox.PackStart(runRow, false, false, 0)

	// right: task form
	rightCard, rightBox := cardBox("Параметры задачи")

	u.typeCombo, _ = gtk.ComboBoxTextNew()
	for _, t := range pbTypes {
		u.typeCombo.AppendText(t.Ru)
	}
	u.typeCombo.SetActive(0)
	u.typeCombo.Connect("changed", func() { u.updateFormVisibility() })
	typeRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	typeRow.PackStart(labelNew("Тип:"), false, false, 0)
	typeRow.PackStart(u.typeCombo, true, true, 0)
	rightBox.PackStart(typeRow, false, false, 0)

	u.form = mkGrid()
	u.sourceE = entryNew("Источник / команда")
	// «Обзор…» — выбрать скрипт/файл диалогом вместо ручного ввода пути
	u.sourceBtn = buttonNew("Обзор…")
	u.sourceBtn.SetTooltipText("Выбрать локальный файл или скрипт")
	u.sourceBtn.Connect("clicked", func() {
		fn := fileChooser(app.window, "Выберите скрипт или файл", gtk.FILE_CHOOSER_ACTION_OPEN, "")
		if fn != "" {
			u.sourceE.SetText(fn)
		}
	})
	srcRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	srcRow.PackStart(u.sourceE, true, true, 0)
	srcRow.PackStart(u.sourceBtn, false, false, 0)
	u.destE = entryNew("Назначение (напр. /etc/app.conf)")
	u.searchE = entryNew("Найти текст")
	u.replaceE = entryNew("Заменить на")
	u.serviceE = entryNew("Служба для перезапуска (опц.)")
	u.postE, _ = gtk.TextViewNew()
	tvSet(u.postE, 70)
	postScroll := scrolled(u.postE, 60)

	u.form.attach(labelNew("Источник:"), 0, 0, 1, 1)
	u.form.attach(srcRow, 1, 0, 1, 1)
	u.form.attach(labelNew("Назначение:"), 0, 1, 1, 1)
	u.form.attach(u.destE, 1, 1, 1, 1)
	u.form.attach(labelNew("Найти:"), 0, 2, 1, 1)
	u.form.attach(u.searchE, 1, 2, 1, 1)
	u.form.attach(labelNew("Заменить:"), 0, 3, 1, 1)
	u.form.attach(u.replaceE, 1, 3, 1, 1)
	u.form.attach(labelNew("Служба:"), 0, 4, 1, 1)
	u.form.attach(u.serviceE, 1, 4, 1, 1)
	u.form.attach(labelNew("Команды после:"), 0, 5, 1, 1)
	u.form.attach(postScroll, 1, 5, 1, 1)

	u.rootC, _ = gtk.CheckButtonNewWithLabel("Выполнять с правами root (su)")
	u.form.attach(u.rootC, 1, 6, 1, 1)
	rightBox.PackStart(u.form.w, false, false, 0)

	formBtns := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
	btnAdd := buttonNew("Добавить задачу")
	btnAdd.Connect("clicked", func() { u.addOrUpdate(false) })
	btnApply := buttonNew("Обновить выбранную")
	btnApply.Connect("clicked", func() { u.addOrUpdate(true) })
	formBtns.PackStart(btnAdd, true, true, 0)
	formBtns.PackStart(btnApply, true, true, 0)
	rightBox.PackStart(formBtns, false, false, 0)

	hint := labelNew("Выберите хосты на вкладке «Хосты». Если ничего не выбрано — задача пойдёт на все видимые хосты.")
	hsc, _ := hint.GetStyleContext()
	hsc.AddClass("ramuted")
	hint.SetLineWrap(true)
	rightBox.PackStart(hint, false, false, 2)

	root.PackStart(leftCard, true, true, 0)
	root.PackStart(rightCard, false, false, 0)

	u.refresh()
	u.widget = root
	glibIdle(func() { u.updateFormVisibility() })
	return u
}

func entryNew(placeholder string) *gtk.Entry {
	e, _ := gtk.EntryNew()
	e.SetPlaceholderText(placeholder)
	return e
}

func tvSet(t *gtk.TextView, height int) {
	t.SetWrapMode(gtk.WRAP_WORD_CHAR)
	t.SetSizeRequest(-1, height)
}

func scrolled(w gtk.IWidget, height int) *gtk.ScrolledWindow {
	s, _ := gtk.ScrolledWindowNew(nil, nil)
	s.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	if w != nil {
		s.Add(w)
	}
	s.SetSizeRequest(-1, height)
	return s
}

func (u *PlaybookUI) currentType() string {
	return pbTypes[u.typeCombo.GetActive()].Key
}

func (u *PlaybookUI) setType(t string) {
	for i, tt := range pbTypes {
		if tt.Key == t {
			u.typeCombo.SetActive(i)
			return
		}
	}
	u.typeCombo.SetActive(0)
}

func (u *PlaybookUI) updateFormVisibility() {
	t := u.currentType()
	setRow := func(row int, show bool) {
		for col := 0; col <= 1; col++ {
			c, err := u.form.w.GetChildAt(col, row)
			if err != nil || c == nil {
				continue
			}
			// NB: never assert IWidget to *gtk.Widget — the concrete wrapper is
			// *gtk.Label/*gtk.Entry/etc. Use the promoted SetVisible method instead.
			if lv, ok := c.(interface{ SetVisible(bool) }); ok {
				lv.SetVisible(show)
			}
		}
	}
	setRow(0, t != "edit")
	setRow(1, t == "copy" || t == "replace" || t == "replace_with_cmds")
	setRow(2, t == "edit")
	setRow(3, t == "edit")
	setRow(4, t == "edit")
	setRow(5, t == "replace_with_cmds")
	// «Обзор…» нужен только там, где источник — локальный файл
	if u.sourceBtn != nil {
		u.sourceBtn.SetVisible(t == "script" || t == "copy" || t == "replace" || t == "replace_with_cmds")
	}
	// source row label text
	if l0, _ := u.form.w.GetChildAt(0, 0); l0 != nil {
		if l, ok := l0.(*gtk.Label); ok {
			if t == "command" {
				l.SetText("Команда:")
			} else {
				l.SetText("Источник:")
			}
		}
	}
}

func (u *PlaybookUI) formToTask() PlaybookTask {
	t := PlaybookTask{
		Type:           u.currentType(),
		Source:         entText(u.sourceE),
		Dest:           entText(u.destE),
		Search:         entText(u.searchE),
		Replace:        entText(u.replaceE),
		RestartService: entText(u.serviceE),
		Root:           u.rootC.GetActive(),
	}
	if t.Type == "replace_with_cmds" {
		s := tvText(u.postE)
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				t.PostCommands = append(t.PostCommands, line)
			}
		}
	}
	return t
}

func (u *PlaybookUI) taskToForm(t PlaybookTask) {
	u.setType(t.Type)
	u.sourceE.SetText(t.Source)
	u.destE.SetText(t.Dest)
	u.searchE.SetText(t.Search)
	u.replaceE.SetText(t.Replace)
	u.serviceE.SetText(t.RestartService)
	u.rootC.SetActive(t.Root)
	tvSetText(u.postE, strings.Join(t.PostCommands, "\n"))
	u.updateFormVisibility()
}

func (u *PlaybookUI) clearForm() {
	u.sourceE.SetText("")
	u.destE.SetText("")
	u.searchE.SetText("")
	u.replaceE.SetText("")
	u.serviceE.SetText("")
	u.rootC.SetActive(false)
	tvSetText(u.postE, "")
}

func (u *PlaybookUI) addOrUpdate(update bool) {
	t := u.formToTask()
	// validation
	if t.Type != "edit" && strings.TrimSpace(t.Source) == "" {
		warnDialog(app.window, "Укажите источник (команду или путь)")
		return
	}
	if (t.Type == "copy" || t.Type == "replace" || t.Type == "replace_with_cmds") && strings.TrimSpace(t.Dest) == "" {
		warnDialog(app.window, "Укажите назначение")
		return
	}
	if t.Type == "edit" && (strings.TrimSpace(t.Search) == "" || strings.TrimSpace(t.Source) == "") {
		warnDialog(app.window, "Для редактирования укажите файл и текст поиска")
		return
	}
	if update {
		idx := selectedInt(u.tree, 0)
		if idx < 0 {
			warnDialog(app.window, "Задача не выбрана")
			return
		}
		if idx < len(store.State.Playbook) {
			store.State.Playbook[idx] = t
		}
	} else {
		store.State.Playbook = append(store.State.Playbook, t)
		u.clearForm()
	}
	_ = store.Save()
	u.refresh()
}

func (u *PlaybookUI) removeSelected() {
	idx := selectedInt(u.tree, 0)
	if idx >= 0 && idx < len(store.State.Playbook) {
		store.State.Playbook = append(store.State.Playbook[:idx], store.State.Playbook[idx+1:]...)
		_ = store.Save()
		u.refresh()
	}
}

func (u *PlaybookUI) move(dir int) {
	idx := selectedInt(u.tree, 0)
	j := idx + dir
	if idx < 0 || j < 0 || j >= len(store.State.Playbook) {
		return
	}
	pb := store.State.Playbook
	pb[idx], pb[j] = pb[j], pb[idx]
	_ = store.Save()
	u.refresh()
}

func (u *PlaybookUI) loadSelectedIntoForm() {
	idx := selectedInt(u.tree, 0)
	if idx >= 0 && idx < len(store.State.Playbook) {
		u.taskToForm(store.State.Playbook[idx])
	}
}

func (u *PlaybookUI) refresh() {
	if u.st == nil {
		return
	}
	u.st.Clear()
	for i, t := range store.State.Playbook {
		iter := u.st.Append()
		_ = u.st.Set(iter, []int{0, 1}, []interface{}{fmt.Sprintf("%d", i+1), t.Describe()})
	}
}

func (u *PlaybookUI) runPlaybook() {
	ips := app.hostsUI.targetHosts()
	if len(ips) == 0 {
		warnDialog(app.window, "Нет хостов для выполнения")
		return
	}
	if len(store.State.Playbook) == 0 {
		warnDialog(app.window, "Список задач пуст")
		return
	}
	if !needCreds() {
		return
	}
	// dangerous tasks confirmation
	for _, t := range store.State.Playbook {
		if t.isDangerous() {
			if !confirmDanger(app.window, t.Describe(), len(ips)) {
				return
			}
		}
	}
	tasks := make([]PlaybookTask, len(store.State.Playbook))
	copy(tasks, store.State.Playbook)
	logf("accent", "🚀 Playbook: %d задач на %d хост(ов)", len(tasks), len(ips))
	go runPlaybookOnHosts(ips, tasks, "Playbook")
}

func (u *PlaybookUI) saveAsTemplate() {
	if len(store.State.Playbook) == 0 {
		warnDialog(app.window, "Список задач пуст")
		return
	}
	name, ok := entryDialog(app.window, "Новый шаблон", "Имя шаблона", "")
	if !ok || strings.TrimSpace(name) == "" {
		return
	}
	name = strings.TrimSpace(name)
	desc, _ := entryDialog(app.window, "Описание", "Краткое описание (опц.)", "")
	if store.TemplateByName(name) != nil {
		if !confirmDialog(app.window, "Шаблон «"+name+"» уже существует. Перезаписать?") {
			return
		}
		for i := range store.State.Templates {
			if store.State.Templates[i].Name == name {
				store.State.Templates[i].Tasks = append([]PlaybookTask(nil), store.State.Playbook...)
				store.State.Templates[i].Description = desc
			}
		}
	} else {
		store.State.Templates = append(store.State.Templates, Template{
			Name: name, Description: desc,
			Tasks: append([]PlaybookTask(nil), store.State.Playbook...),
		})
	}
	_ = store.Save()
	app.tplUI.refresh()
	logf("ok", "💾 Шаблон «%s» сохранён (%d задач)", name, len(store.State.Playbook))
}

// runPlaybookOnHosts executes a task list sequentially on each host (parallel across hosts).
func runPlaybookOnHosts(ips []string, tasks []PlaybookTask, label string) {
	runParallel(ips, label, func(ip string) (string, error) {
		creds := buildCreds(ip)
		cl, err := Connect(creds)
		if err != nil {
			return "", err
		}
		defer cl.Close()
		var logAll strings.Builder
		for i, t := range tasks {
			out, err := execTask(cl, ip, t, creds)
			logAll.WriteString(fmt.Sprintf("[%d/%d] %s\n%s\n", i+1, len(tasks), t.Describe(), strings.TrimSpace(out)))
			if err != nil {
				logAll.WriteString(fmt.Sprintf("ОШИБКА: %v\n", err))
				return logAll.String(), fmt.Errorf("задача %d (%s): %v", i+1, t.Describe(), err)
			}
		}
		return logAll.String(), nil
	})
}

// execTask performs a single PlaybookTask over an established connection.
func execTask(cl *ssh.Client, ip string, t PlaybookTask, creds SSHCreds) (string, error) {
	timeout := store.State.Settings.ExecTimeout
	root := t.Root || (store.State.UseRoot && creds.User != "root")
	run := func(cmd string) (string, error) {
		if root && creds.User != "root" {
			if creds.RootPass == "" {
				return "", fmt.Errorf("нужен пароль root (su)")
			}
			return ExecRoot(cl, cmd, creds.RootPass, timeout)
		}
		return Exec(cl, cmd, timeout)
	}
	switch t.Type {
	case "command":
		return run(t.Source)
	case "script":
		tmp := fmt.Sprintf("/tmp/.ra_pb_%d.sh", nowMs())
		if err := uploadThenSuRoot(cl, t.Source, tmp, rootPassFor(creds, root), timeout); err != nil {
			return "", err
		}
		defer Exec(cl, "rm -f "+shellQuote(tmp), 15)
		return run("sh " + shellQuote(tmp))
	case "copy":
		if root && creds.User != "root" {
			if err := uploadThenSuRoot(cl, t.Source, t.Dest, creds.RootPass, timeout); err != nil {
				return "", err
			}
			return run("chmod 644 " + shellQuote(t.Dest))
		}
		if err := scpUpload(cl, t.Source, t.Dest); err != nil {
			return "", err
		}
		return "скопировано: " + t.Dest, nil
	case "replace", "replace_with_cmds":
		bak := fmt.Sprintf("%s.ra_bak_%d", t.Dest, nowMs())
		if _, err := run(fmt.Sprintf("cp -f %s %s 2>/dev/null; true", shellQuote(t.Dest), shellQuote(bak))); err != nil {
			return "", err
		}
		if root && creds.User != "root" {
			if err := uploadThenSuRoot(cl, t.Source, t.Dest, creds.RootPass, timeout); err != nil {
				return "", err
			}
		} else {
			if err := scpUpload(cl, t.Source, t.Dest); err != nil {
				return "", err
			}
		}
		out := fmt.Sprintf("файл заменён (backup: %s)", bak)
		if t.Type == "replace_with_cmds" {
			for _, c := range t.PostCommands {
				cout, err := run(c)
				out += fmt.Sprintf("\n$ %s\n%s", c, strings.TrimSpace(cout))
				if err != nil {
					return out, fmt.Errorf("пост-команда: %w", err)
				}
			}
		}
		return out, nil
	case "edit":
		if strings.TrimSpace(t.Search) == "" {
			return "", fmt.Errorf("пустой текст поиска")
		}
		// remote sed replacement (root-aware)
		sed := fmt.Sprintf("sed -i 's|%s|%s|g' %s", sedEscape(t.Search), sedEscape(t.Replace), shellQuote(t.Source))
		out, err := run(sed)
		if err != nil {
			return out, err
		}
		if t.RestartService != "" {
			rout, err := run("systemctl restart " + shellQuote(t.RestartService))
			out += "\n" + rout
			if err != nil {
				return out, fmt.Errorf("restart %s: %w", t.RestartService, err)
			}
			out += fmt.Sprintf("\nслужба %s перезапущена", t.RestartService)
		}
		return out, nil
	}
	return "", fmt.Errorf("неизвестный тип задачи: %s", t.Type)
}

func sedEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `|`, `\|`)
	s = strings.ReplaceAll(s, `'`, `'\''`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func rootPassFor(creds SSHCreds, root bool) string {
	if root {
		return creds.RootPass
	}
	return ""
}

func nowMs() int64 {
	return time.Now().UnixNano()
}
