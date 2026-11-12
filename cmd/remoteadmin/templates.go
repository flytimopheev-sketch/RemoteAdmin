package main

import (
        "fmt"
        "strings"

        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

// TemplatesUI — library of reusable task sets.
//
// A template is a saved sequence of playbook tasks (command/script/copy/…)
// that can be re-run on any host selection in one click. The tab offers:
//   - готовые пресеты при первом запуске,
//   - «Новый шаблон» (пустой шаблон с первой командой),
//   - «Создать из Плейбука» (сохранить текущий список задач),
//   - запуск на выбранных хостах и загрузка в Плейбук для правок.
type TemplatesUI struct {
        widget gtk.IWidget
        tree   *gtk.TreeView
        st     *gtk.ListStore
        detail *gtk.Label
}

const (
        TCOL_NAME  = 0
        TCOL_DESC  = 1 // reserved; detail pane renders markup instead
        TCOL_COUNT = 2 // NB: NOT a valid column of the 2-col tree — see refresh()
)

// seedPresetTemplates fills an empty template library with useful presets
// (РЕД ОС-oriented). Runs once; the flag is stored in state.
func seedPresetTemplates() {
        if !store.State.PresetsSeeded && len(store.State.Templates) == 0 {
                store.State.Templates = append(store.State.Templates,
                        Template{
                                Name:        "Обновление системы",
                                Description: "Полное обновление пакетов через dnf (нужен root)",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "dnf -y update", Root: true},
                                },
                        },
                        Template{
                                Name:        "Сбор информации о системе",
                                Description: "Имя хоста, ядро, ОС, CPU, RAM, диск, аптайм",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "hostname; uname -r; grep PRETTY_NAME /etc/os-release | head -1; nproc; free -m | awk 'NR==2{print \"RAM: \" $3 \"/\" $2 \" МБ\"}'; df -h / | awk 'NR==2{print \"Диск /: \" $3 \"/\" $2 \" (\" $5 \")\"}'; uptime -p"},
                                },
                        },
                        Template{
                                Name:        "Проверка дисков",
                                Description: "Заполнение ФС и ошибки SMART",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "df -h; echo; smartctl -H /dev/sda 2>/dev/null || echo 'smartctl недоступен'"},
                                },
                        },
                        Template{
                                Name:        "Перезапуск SSH",
                                Description: "sshd restart + проверка статуса (root)",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "systemctl restart sshd && systemctl is-active sshd", Root: true},
                                },
                        },
                        Template{
                                Name:        "Очистка кэша dnf",
                                Description: "dnf clean all + место на диске до/после",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "df -h / | tail -1; dnf clean all >/dev/null 2>&1; df -h / | tail -1", Root: true},
                                },
                        },
                )
                store.State.PresetsSeeded = true
                _ = store.Save()
                logf("info", "📚 Добавлены готовые шаблоны: %d шт. (их можно изменить под себя)", len(store.State.Templates))
        }

        // вторая волна пресетов (v1.7.0): добавляется и тем, у кого старые
        // шаблоны уже есть — по имени дубликаты не создаются
        if !store.State.PresetsSeededV2 {
                v2 := []Template{
                        {
                                Name:        "Диагностика сети",
                                Description: "Интерфейсы, маршруты, DNS и проверка выхода в интернет",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "ip -br addr; echo; ip route; echo; grep -v '^#' /etc/resolv.conf 2>/dev/null; echo; ping -c 2 -W 2 8.8.8.8 || echo 'выход в интернет недоступен'"},
                                },
                        },
                        {
                                Name:        "Нагрузка и процессы",
                                Description: "Средняя нагрузка, топ процессов по CPU, память",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "uptime; echo; ps aux --sort=-%cpu | head -8; echo; free -m | awk 'NR==2{print \"RAM занято: \"$3\"/\"$2\" МБ\"}'; echo; vmstat 1 2 2>/dev/null | tail -2"},
                                },
                        },
                        {
                                Name:        "Проверка сервисов",
                                Description: "Найдёт упавшие сервисы (systemctl --failed) и статус sshd",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "systemctl --failed --no-pager; echo; systemctl is-active sshd && echo 'sshd работает' || echo 'sshd НЕ работает'"},
                                },
                        },
                        {
                                Name:        "Последние входы",
                                Description: "Кто и когда входил на сервер (last, who, пользователи)",
                                Tasks: []PlaybookTask{
                                        {Type: "command", Source: "last -a -n 12 2>/dev/null; echo; who; echo; echo 'Учётные записи людей:'; awk -F: '$3>=1000 {print $1}' /etc/passwd"},
                                },
                        },
                }
                added := 0
                for _, t := range v2 {
                        if store.TemplateByName(t.Name) == nil {
                                store.State.Templates = append(store.State.Templates, t)
                                added++
                        }
                }
                store.State.PresetsSeededV2 = true
                if added > 0 {
                        _ = store.Save()
                        logf("ok", "📚 Добавлены новые шаблоны (%d шт.): Диагностика сети, Нагрузка и процессы, Проверка сервисов, Последние входы", added)
                }
        }
}

func newTemplatesUI() *TemplatesUI {
        seedPresetTemplates()

        u := &TemplatesUI{}
        root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

        // ---- what is this tab ----
        introCard, introBox := cardBox("")
        intro := labelNew("Шаблоны — сохранённые наборы задач (команды, скрипты, копирование файлов) для типовых операций. " +
                "Выберите шаблон, отметьте хосты на вкладке «Хосты» и нажмите «Запустить». " +
                "Кнопка «В Плейбук» загружает задачи шаблона в редактор для доработки; «Создать из Плейбука» сохраняет текущий список задач как новый шаблон.")
        intro.SetLineWrap(true)
        intro.SetXAlign(0)
        isc, _ := intro.GetStyleContext()
        isc.AddClass("ramuted")
        introBox.PackStart(intro, false, false, 0)
        root.PackStart(introCard, false, false, 0)

        lists := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)

        // ---- left: template list + buttons ----
        leftCard, leftBox := cardBox("Шаблоны задач")
        u.tree, u.st = makeTree([]string{"Имя", "Задач"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING})
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

        btns := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnRun := buttonNew("Запустить")
        sr, _ := btnRun.GetStyleContext()
        sr.AddClass("suggested")
        btnRun.SetTooltipText("Выполнить задачи шаблона на выбранных хостах")
        btnRun.Connect("clicked", func() { u.runSelected() })
        btnLoad := buttonNew("В Плейбук")
        btnLoad.SetTooltipText("Загрузить задачи шаблона в редактор Плейбук для доработки")
        btnLoad.Connect("clicked", func() { u.loadToPlaybook() })
        btnDel := buttonNew("🗑")
        btnDel.SetTooltipText("Удалить выбранный шаблон")
        btnDel.Connect("clicked", func() { u.deleteSelected() })
        btns.PackStart(btnRun, true, true, 0)
        btns.PackStart(btnLoad, true, true, 0)
        btns.PackStart(btnDel, false, false, 0)
        leftBox.PackStart(btns, false, false, 0)

        createRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnNew := buttonNew("Новый шаблон")
        btnNew.SetTooltipText("Создать пустой шаблон с одной командой")
        btnNew.Connect("clicked", func() { u.createNew() })
        btnFromPb := buttonNew("Создать из Плейбука")
        btnFromPb.SetTooltipText("Сохранить текущий список задач Плейбука как шаблон")
        btnFromPb.Connect("clicked", func() {
                if app.pbUI != nil {
                        app.pbUI.saveAsTemplate()
                        u.refresh()
                }
        })
        createRow.PackStart(btnNew, true, true, 0)
        createRow.PackStart(btnFromPb, true, true, 0)
        leftBox.PackStart(createRow, false, false, 0)

        lists.PackStart(leftCard, true, true, 0)

        // ---- right: composition ----
        rightCard, rightBox := cardBox("Состав шаблона")
        u.detail = labelNew("Выберите шаблон слева — здесь появится список его задач.")
        u.detail.SetXAlign(0)
        u.detail.SetLineWrap(true)
        u.detail.SetVAlign(gtk.ALIGN_START)
        dsc := scrolled(u.detail, 0)
        dsc.SetVExpand(true)
        rightBox.PackStart(dsc, true, true, 0)

        hint := labelNew("Хосты берутся из выбора на вкладке «Хосты» (если ничего не выбрано — все видимые).")
        hint.SetLineWrap(true)
        hhsc, _ := hint.GetStyleContext()
        hhsc.AddClass("ramuted")
        rightBox.PackStart(hint, false, false, 0)

        lists.PackStart(rightCard, true, true, 0)
        root.PackStart(lists, true, true, 0)

        u.refresh()
        u.widget = root
        return u
}

func (u *TemplatesUI) refresh() {
        if u.st == nil {
                return
        }
        u.st.Clear()
        for _, t := range store.State.Templates {
                iter := u.st.Append()
                // NB: the tree has exactly 2 columns (Имя, Задач); the old constant
                // TCOL_COUNT=2 pointed past the model and GTK silently dropped the
                // value (Gtk-CRITICAL), leaving the count column empty.
                _ = u.st.Set(iter, []int{TCOL_NAME, 1}, []interface{}{t.Name, fmt.Sprintf("%d", len(t.Tasks))})
        }
}

func (u *TemplatesUI) selected() *Template {
        m, iter, ok := selIter(u.tree)
        if !ok {
                return nil
        }
        name := iterGetString(m, iter, TCOL_NAME)
        return store.TemplateByName(name)
}

func (u *TemplatesUI) showDetail() {
        t := u.selected()
        if t == nil {
                u.detail.SetText("Выберите шаблон слева — здесь появится список его задач.")
                return
        }
        var b strings.Builder
        b.WriteString("<b>" + t.Name + "</b>\n")
        if t.Description != "" {
                b.WriteString("<span foreground=\"#9aa0a6\">" + t.Description + "</span>\n")
        }
        b.WriteString("\n")
        if len(t.Tasks) == 0 {
                b.WriteString("(шаблон пуст — загрузите его в Плейбук и добавьте задачи)\n")
        }
        for i, task := range t.Tasks {
                fmt.Fprintf(&b, "%d. %s\n", i+1, task.Describe())
                if task.Root {
                        b.WriteString("   <span foreground=\"#fbbf24\">с правами root</span>\n")
                }
        }
        u.detail.SetMarkup(b.String())
}

func (u *TemplatesUI) deleteSelected() {
        t := u.selected()
        if t == nil {
                warnDialog(app.window, "Сначала выберите шаблон в списке")
                return
        }
        if !confirmDialog(app.window, "Удалить шаблон «"+t.Name+"»?") {
                return
        }
        out := store.State.Templates[:0]
        for _, tt := range store.State.Templates {
                if tt.Name != t.Name {
                        out = append(out, tt)
                }
        }
        store.State.Templates = out
        _ = store.Save()
        u.refresh()
        u.showDetail()
        logf("ok", "🗑 Шаблон «%s» удалён", t.Name)
}

func (u *TemplatesUI) loadToPlaybook() {
        t := u.selected()
        if t == nil {
                warnDialog(app.window, "Сначала выберите шаблон в списке")
                return
        }
        if len(t.Tasks) == 0 {
                warnDialog(app.window, "Шаблон пустой")
                return
        }
        store.State.Playbook = append([]PlaybookTask(nil), t.Tasks...)
        _ = store.Save()
        app.pbUI.refresh()
        logf("ok", "✎ Шаблон «%s» загружен в Плейбук (%d задач) — вкладка «Плейбук» для правок", t.Name, len(t.Tasks))
}

// createNew makes a fresh template with one command task.
func (u *TemplatesUI) createNew() {
        name, ok := entryDialog(app.window, "Новый шаблон", "Имя шаблона", "")
        if !ok {
                return
        }
        name = strings.TrimSpace(name)
        if name == "" {
                return
        }
        if store.TemplateByName(name) != nil {
                warnDialog(app.window, "Шаблон с таким именем уже есть")
                return
        }
        desc, _ := entryDialog(app.window, "Описание", "Краткое описание (опционально)", "")
        cmd, ok := entryDialog(app.window, "Первая задача", "Команда (потом можно добавить задачи через «В Плейбук»)", "")
        if !ok {
                cmd = ""
        }
        t := Template{Name: name, Description: strings.TrimSpace(desc)}
        if strings.TrimSpace(cmd) != "" {
                t.Tasks = []PlaybookTask{{Type: "command", Source: strings.TrimSpace(cmd)}}
        }
        store.State.Templates = append(store.State.Templates, t)
        _ = store.Save()
        u.refresh()
        u.selectByName(name)
        logf("ok", "💾 Шаблон «%s» создан", name)
}

// select programmatically selects a template row by name.
func (u *TemplatesUI) selectByName(name string) {
        m, ok := treeModelOf(u.tree)
        if !ok {
                return
        }
        sel, err := u.tree.GetSelection()
        if err != nil {
                return
        }
        iter, ok2 := m.GetIterFirst()
        for ok2 {
                if iterGetString(m, iter, TCOL_NAME) == name {
                        sel.SelectIter(iter)
                        return
                }
                ok2 = m.IterNext(iter)
        }
}

func (u *TemplatesUI) runSelected() {
        t := u.selected()
        if t == nil {
                warnDialog(app.window, "Сначала выберите шаблон в списке")
                return
        }
        if len(t.Tasks) == 0 {
                warnDialog(app.window, "Шаблон пустой — нечего выполнять")
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
        for _, task := range t.Tasks {
                if task.isDangerous() && !confirmDanger(app.window, task.Describe(), len(ips)) {
                        return
                }
        }
        logf("accent", "🚀 Шаблон «%s»: %d задач на %d хост(ов)", t.Name, len(t.Tasks), len(ips))
        go runPlaybookOnHosts(ips, t.Tasks, "Шаблон: "+t.Name)
}

var _ = glib.TimeoutAdd
