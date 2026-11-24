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
//   - редактирование имени/описания и состава задач (v1.11.0),
//   - запуск на выбранных хостах и загрузка в Плейбук для правок.
type TemplatesUI struct {
        widget gtk.IWidget
        tree   *gtk.TreeView
        st     *gtk.ListStore
        detail *gtk.TextView
}

const (
        TCOL_NAME  = 0
        TCOL_DESC  = 1 // reserved; detail pane renders markup instead
        TCOL_RAW   = 2 // скрытая колонка с ЧИСТЫМ именем (v1.13.2)
        // NB: NOT a valid column of the 2-col tree — see refresh()
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

        // v1.13.2: все кнопки вкладки — ОДИН компактный ряд сверху (.tbtn),
        // как на «Цепочках» и «Планировщике». Раньше два ряда сидели в нижней
        // части карточки — при прокручиваемой странице они оказывались под
        // сгибом и были не видны. Ряд всегда виден, кнопки одинаковые.
        toolbar := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        btnRun := buttonNew("Запустить")
        sr, _ := btnRun.GetStyleContext()
        sr.AddClass("suggested")
        addClass(btnRun, "tbtn")
        btnRun.SetTooltipText("Выполнить задачи выбранного шаблона на выбранных хостах")
        btnRun.Connect("clicked", func() { u.runSelected() })
        btnLoad := buttonNew("В Плейбук")
        addClass(btnLoad, "tbtn")
        btnLoad.SetTooltipText("Загрузить задачи выбранного шаблона в редактор Плейбук для доработки")
        btnLoad.Connect("clicked", func() { u.loadToPlaybook() })
        btnDel := buttonNew("Удалить")
        addClass(btnDel, "tbtn")
        btnDel.SetTooltipText("Удалить выбранный шаблон (Delete в списке — то же)")
        btnDel.Connect("clicked", func() { u.deleteSelected() })
        btnNew := buttonNew("Новый")
        addClass(btnNew, "tbtn")
        btnNew.SetTooltipText("Создать пустой шаблон с одной командой")
        btnNew.Connect("clicked", func() { u.createNew() })
        btnFromPb := buttonNew("Из Плейбука")
        addClass(btnFromPb, "tbtn")
        btnFromPb.SetTooltipText("Создать шаблон из текущего списка задач Плейбука (сохранить как шаблон)")
        btnFromPb.Connect("clicked", func() {
                if app.pbUI != nil {
                        app.pbUI.saveAsTemplate()
                        u.refresh()
                }
        })
        btnEdit := buttonNew("Редактировать")
        addClass(btnEdit, "tbtn")
        btnEdit.SetTooltipText("Изменить имя, описание и задачи выбранного шаблона\n(двойной щелчок по шаблону в списке — то же самое)")
        btnEdit.Connect("clicked", func() { u.editSelected() })
        toolbar.PackStart(btnRun, false, false, 0)
        toolbar.PackStart(btnLoad, false, false, 0)
        toolbar.PackStart(btnDel, false, false, 0)
        toolbar.PackStart(btnNew, false, false, 0)
        toolbar.PackStart(btnFromPb, false, false, 0)
        toolbar.PackStart(btnEdit, false, false, 0)
        root.PackStart(toolbar, false, false, 0)

        lists := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)

        // ---- left: template list + buttons ----
        leftCard, leftBox := cardBox("Шаблоны задач")
        // v1.13.0 (жалоба: «цвет текста одинаковый, монотонный, не читаемый»):
        // имя шаблона — жирным, вторая строка — приглушённая сводка
        // (число задач, типы, root) — список перестал быть одноцветным.
        // v1.13.2: у ListStore ТРИ колонки (третья — скрытая TCOL_RAW с чистым
        // именем); в TreeView отображаются только первые две.
        u.tree, u.st = makeTreeM([]string{"Шаблон", "Задач"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING}, map[int]bool{0: true})
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

        // двойной щелчок — редактирование
        u.tree.Connect("button-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
                eb := gdk.EventButtonNewFromEvent(ev)
                if eb.Type() == gdk.EVENT_2BUTTON_PRESS && eb.Button() == 1 {
                        u.editSelected()
                        return true
                }
                return false
        })

        lists.PackStart(leftCard, true, true, 0)

        // ---- right: composition ----
        rightCard, rightBox := cardBox("Состав шаблона")
        // NB: раньше состав показывался в GtkLabel: длинные команды не
        // влезали — строка обрезалась краем виджета и прокрутки не было.
        // Теперь это монопространственный TextView с переносом по словам и
        // прокруткой: виден весь состав при любой длине.
        u.detail, _ = gtk.TextViewNew()
        u.detail.SetEditable(false)
        u.detail.SetCursorVisible(false)
        u.detail.SetWrapMode(gtk.WRAP_WORD_CHAR)
        u.detail.SetMonospace(true)
        u.detail.SetLeftMargin(10)
        u.detail.SetRightMargin(10)
        u.detail.SetTopMargin(8)
        u.detail.SetBottomMargin(8)
        addClass(u.detail, "rpdetail")
        if db, err := u.detail.GetBuffer(); err == nil {
                dc := detailColors()
                // v1.13.0: состав раскрашен — заголовок акцентный, тип задачи
                // своим цветом (команда — синий, скрипт — янтарный, копия —
                // зелёный, правка — красный), строка шага полужирная.
                db.CreateTag("h1", map[string]interface{}{"weight": 700, "scale": 1.18, "foreground": dc.sec})
                db.CreateTag("step", map[string]interface{}{"weight": 600, "foreground": dc.text})
                db.CreateTag("dim", map[string]interface{}{"foreground": dc.dim})
                db.CreateTag("num", map[string]interface{}{"weight": 700, "foreground": dc.sec})
                db.CreateTag("root", map[string]interface{}{"foreground": dc.warn})
                db.CreateTag("tcommand", map[string]interface{}{"weight": 600, "foreground": dc.cmd})
                db.CreateTag("tscript", map[string]interface{}{"weight": 600, "foreground": dc.warn})
                db.CreateTag("tcopy", map[string]interface{}{"weight": 600, "foreground": dc.ok})
                db.CreateTag("tedit", map[string]interface{}{"weight": 600, "foreground": dc.fail})
                db.CreateTag("mono", map[string]interface{}{"family": "Monospace", "foreground": dc.text})
        }
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
        dc := detailColors()
        for _, t := range store.State.Templates {
                iter := u.st.Append()
                // NB: the tree has exactly 2 columns (Шаблон, Задач); col 0 is
                // Pango-markup: жирное имя + приглушённая сводка снизу.
                kinds := map[string]int{}
                hasRoot := false
                for _, tk := range t.Tasks {
                        kinds[tk.Type]++
                        if tk.Root {
                                hasRoot = true
                        }
                }
                order := []string{"command", "script", "copy", "replace", "replace_with_cmds", "edit"}
                ru := map[string]string{"command": "команды", "script": "скрипты", "copy": "копирование",
                        "replace": "замена", "replace_with_cmds": "замена+команды", "edit": "правка"}
                var kindsTxt []string
                for _, k := range order {
                        if n := kinds[k]; n > 0 {
                                kindsTxt = append(kindsTxt, fmt.Sprintf("%d %s", n, ru[k]))
                        }
                }
                sum := fmt.Sprintf("%d задач", len(t.Tasks))
                if len(t.Tasks) == 1 {
                        sum = "1 задача"
                }
                if len(kindsTxt) > 0 {
                        sum += " · " + strings.Join(kindsTxt, ", ")
                }
                if hasRoot {
                        sum += " · root"
                }
                markup := fmt.Sprintf("<b>%s</b>\n<span foreground=\"%s\" size=\"9000\">%s</span>",
                        escapeMarkup(t.Name), dc.dim, escapeMarkup(sum))
                // v1.13.2: в колонке 0 живёт МАРКАП (жирное имя + сводка) —
                // поиск по ней никогда не находил шаблон (TemplateByName(маркап)
                // = nil: не работали «Запустить/Изменить/Удалить» и состав).
                // Чистое имя — в скрытой колонке TCOL_RAW.
                _ = u.st.Set(iter, []int{TCOL_NAME, 1, TCOL_RAW},
                        []interface{}{markup, fmt.Sprintf("%d", len(t.Tasks)), t.Name})
        }
}

func (u *TemplatesUI) selected() *Template {
        m, iter, ok := selIter(u.tree)
        if !ok {
                return nil
        }
        // v1.13.2: имя берём из скрытой колонки TCOL_RAW — в колонке 0 маркап
        name := iterGetString(m, iter, TCOL_RAW)
        return store.TemplateByName(name)
}

func (u *TemplatesUI) showDetail() {
        t := u.selected()
        buf, berr := u.detail.GetBuffer()
        if berr != nil || buf == nil {
                return
        }
        buf.SetText("")
        ins := func(text, tag string) {
                iter := buf.GetEndIter()
                if tag != "" {
                        buf.InsertWithTagByName(iter, text, tag)
                } else {
                        buf.Insert(iter, text)
                }
        }
        if t == nil {
                ins("Выберите шаблон слева — здесь появится список его задач.\n", "dim")
                ins("Двойной щелчок по шаблону или кнопка «Редактировать шаблон» — изменить состав.\n", "dim")
                return
        }
        ins(t.Name+"\n", "h1")
        if t.Description != "" {
                ins(t.Description+"\n", "dim")
        }
        ins("\n", "")
        if len(t.Tasks) == 0 {
                ins("(шаблон пуст — откройте редактор и добавьте задачи)\n", "dim")
        }
        for i, task := range t.Tasks {
                ins(fmt.Sprintf("%d. ", i+1), "num")
                ins(taskTypeLabel(task.Type), taskTypeTag(task.Type))
                ins(" ", "")
                ins(task.StepTitle()+"\n", "step")
                if cmd := task.stepCommand(); strings.TrimSpace(cmd) != "" {
                        ins("   "+strings.ReplaceAll(cmd, "\n", "\n   ")+"\n", "mono")
                }
                if task.Root {
                        ins("   с правами root (su)\n", "root")
                }
        }
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
                if iterGetString(m, iter, TCOL_RAW) == name {
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

// editSelected открывает редактор выбранного шаблона (v1.11.0).
func (u *TemplatesUI) editSelected() {
        t := u.selected()
        if t == nil {
                warnDialog(app.window, "Сначала выберите шаблон в списке")
                return
        }
        nt, ok := templateEditDialog(app.window, *t)
        if !ok {
                return
        }
        // замена по старому имени (имя тоже могло измениться)
        for i := range store.State.Templates {
                if store.State.Templates[i].Name == t.Name {
                        store.State.Templates[i] = nt
                        break
                }
        }
        _ = store.Save()
        u.refresh()
        u.selectByName(nt.Name)
        logf("ok", "✎ Шаблон «%s» изменён (%d задач)", nt.Name, len(nt.Tasks))
}

// templateEditDialog — редактор шаблона: имя, описание и состав задач.
// Задачи можно добавлять, изменять (в отдельном окне с многострочным
// редактором команды), удалять и передвигать.
func templateEditDialog(parent *gtk.Window, src Template) (Template, bool) {
        t := Template{
                Name:        src.Name,
                Description: src.Description,
                Tasks:       append([]PlaybookTask(nil), src.Tasks...),
        }
        dlg, err := gtk.DialogNew()
        if err != nil {
                return src, false
        }
        dlg.SetTitle("Редактор шаблона")
        dlg.SetTransientFor(parent)
        dlg.SetModal(true)
        dlg.SetDefaultSize(640, 540)
        registerModal(dlg)
        defer unregisterModal()
        area, _ := dlg.GetContentArea()
        area.SetMarginTop(12)
        area.SetMarginBottom(6)
        area.SetMarginStart(14)
        area.SetMarginEnd(14)

        rowName := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        lblName := labelNew("Имя:")
        nameE, _ := gtk.EntryNew()
        nameE.SetText(t.Name)
        nameE.SetHExpand(true)
        rowName.PackStart(lblName, false, false, 0)
        rowName.PackStart(nameE, true, true, 0)
        area.PackStart(rowName, false, false, 4)

        rowDesc := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)
        lblDesc := labelNew("Описание:")
        descE, _ := gtk.EntryNew()
        descE.SetText(t.Description)
        descE.SetHExpand(true)
        rowDesc.PackStart(lblDesc, false, false, 0)
        rowDesc.PackStart(descE, true, true, 0)
        area.PackStart(rowDesc, false, false, 4)

        lt := labelNew("Задачи шаблона (порядок = порядок выполнения):")
        lt.SetXAlign(0)
        area.PackStart(lt, false, false, 6)

        tree, st := makeTree([]string{"№", "Задача"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING})
        tree.SetSizeRequest(-1, 150)
        tsc := scrolled(tree, 150)
        area.PackStart(tsc, true, true, 0)

        fillTasks := func() {
                st.Clear()
                for i, task := range t.Tasks {
                        iter := st.Append()
                        _ = st.Set(iter, []int{0, 1}, []interface{}{fmt.Sprintf("%d", i+1), task.StepTitle() + "\n" + task.stepCommand()})
                }
        }
        fillTasks()

        btnRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        btnAdd := buttonNew("Добавить")
        btnEdit := buttonNew("Изменить")
        btnDel := buttonNew("Удалить")
        btnUp := buttonNew("↑")
        btnDown := buttonNew("↓")
        btnUp.SetTooltipText("Передвинуть задачу выше")
        btnDown.SetTooltipText("Передвинуть задачу ниже")
        btnRow.PackStart(btnAdd, false, false, 0)
        btnRow.PackStart(btnEdit, false, false, 0)
        btnRow.PackStart(btnDel, false, false, 0)
        btnRow.PackEnd(btnDown, false, false, 0)
        btnRow.PackEnd(btnUp, false, false, 0)
        area.PackStart(btnRow, false, false, 4)

        selIdx := func() int {
                m, iter, ok := selIter(tree)
                if !ok {
                        return -1
                }
                return atoiSafe(iterGetString(m, iter, 0)) - 1
        }

        btnAdd.Connect("clicked", func() {
                nt, ok := templateTaskDialog(dlg, PlaybookTask{Type: "command"})
                if !ok {
                        return
                }
                t.Tasks = append(t.Tasks, nt)
                fillTasks()
        })
        btnEdit.Connect("clicked", func() {
                i := selIdx()
                if i < 0 || i >= len(t.Tasks) {
                        warnDialog(app.window, "Выберите задачу в списке")
                        return
                }
                nt, ok := templateTaskDialog(dlg, t.Tasks[i])
                if !ok {
                        return
                }
                t.Tasks[i] = nt
                fillTasks()
        })
        btnDel.Connect("clicked", func() {
                i := selIdx()
                if i < 0 || i >= len(t.Tasks) {
                        return
                }
                t.Tasks = append(t.Tasks[:i], t.Tasks[i+1:]...)
                fillTasks()
        })
        move := func(delta int) {
                i := selIdx()
                j := i + delta
                if i < 0 || j < 0 || j >= len(t.Tasks) {
                        return
                }
                t.Tasks[i], t.Tasks[j] = t.Tasks[j], t.Tasks[i]
                fillTasks()
        }
        btnUp.Connect("clicked", func() { move(-1) })
        btnDown.Connect("clicked", func() { move(1) })

        dlg.AddButton("Отмена", gtk.RESPONSE_CANCEL)
        okBtn, _ := dlg.AddButton("Сохранить", gtk.RESPONSE_OK)
        if sb, err := okBtn.GetStyleContext(); err == nil {
                sb.AddClass("suggested-action")
        }
        dlg.SetDefaultResponse(gtk.RESPONSE_OK)
        dlg.ShowAll()
        res := dlg.Run()
        dlg.Destroy()
        if res != gtk.RESPONSE_OK {
                return src, false
        }
        t.Name = strings.TrimSpace(entText(nameE))
        t.Description = strings.TrimSpace(entText(descE))
        if t.Name == "" {
                t.Name = src.Name
        }
        return t, true
}

// templateTaskDialog — редактор ОДНОЙ задачи шаблона. Источник — многострочный
// монопространственный TextView: длинные команды видны целиком.
func templateTaskDialog(parent *gtk.Dialog, src PlaybookTask) (PlaybookTask, bool) {
        t := src
        dlg, err := gtk.DialogNew()
        if err != nil {
                return src, false
        }
        dlg.SetTitle("Задача шаблона")
        dlg.SetTransientFor(parent)
        dlg.SetModal(true)
        dlg.SetDefaultSize(560, 460)
        registerModal(dlg)
        defer unregisterModal()
        area, _ := dlg.GetContentArea()
        area.SetMarginTop(12)
        area.SetMarginBottom(6)
        area.SetMarginStart(14)
        area.SetMarginEnd(14)

        addLabelField := func(text string) {
                l := labelNew(text)
                l.SetXAlign(0)
                area.PackStart(l, false, false, 4)
        }

        addLabelField("Название шага (для списка задач; можно оставить пустым):")
        nameE, _ := gtk.EntryNew()
        nameE.SetText(t.Name)
        area.PackStart(nameE, false, false, 2)

        addLabelField("Тип задачи:")
        typeC, _ := gtk.ComboBoxTextNew()
        for _, s := range []string{"Команда", "Скрипт (путь к файлу на этом ПК)", "Копирование файла"} {
                typeC.AppendText(s)
        }
        switch t.Type {
        case "script":
                typeC.SetActive(1)
        case "copy":
                typeC.SetActive(2)
        default:
                typeC.SetActive(0)
        }
        area.PackStart(typeC, false, false, 2)

        srcLbl := labelNew("Команда:")
        srcLbl.SetXAlign(0)
        area.PackStart(srcLbl, false, false, 4)

        srcTv, _ := gtk.TextViewNew()
        srcTv.SetWrapMode(gtk.WRAP_WORD_CHAR)
        srcTv.SetMonospace(true)
        srcTv.SetSizeRequest(-1, 140)
        tvSet(srcTv, 140)
        tvSetText(srcTv, t.Source)
        srcSc := scrolled(srcTv, 140)
        area.PackStart(srcSc, true, true, 0)

        destLbl := labelNew("Назначение на сервере (для копирования):")
        destLbl.SetXAlign(0)
        destE, _ := gtk.EntryNew()
        destE.SetText(t.Dest)
        area.PackStart(destLbl, false, false, 4)
        area.PackStart(destE, false, false, 2)

        rootC, _ := gtk.CheckButtonNewWithLabel("Выполнять с повышением прав (su → root)")
        rootC.SetActive(t.Root)
        area.PackStart(rootC, false, false, 6)

        updVis := func() {
                switch typeC.GetActive() {
                case 0:
                        srcLbl.SetText("Команда:")
                        setVis(srcSc, true)
                        setVis(destLbl, false)
                        setVis(destE, false)
                case 1:
                        srcLbl.SetText("Путь к скрипту на этом ПК:")
                        setVis(srcSc, true)
                        setVis(destLbl, false)
                        setVis(destE, false)
                default:
                        srcLbl.SetText("Локальный файл:")
                        setVis(srcSc, true)
                        setVis(destLbl, true)
                        setVis(destE, true)
                }
        }
        typeC.Connect("changed", updVis)
        updVis()

        dlg.AddButton("Отмена", gtk.RESPONSE_CANCEL)
        okBtn, _ := dlg.AddButton("Сохранить", gtk.RESPONSE_OK)
        if sb, err := okBtn.GetStyleContext(); err == nil {
                sb.AddClass("suggested-action")
        }
        dlg.SetDefaultResponse(gtk.RESPONSE_OK)
        dlg.ShowAll()
        res := dlg.Run()
        if res == gtk.RESPONSE_OK {
                t.Name = strings.TrimSpace(entText(nameE))
                t.Root = rootC.GetActive()
                t.Dest = strings.TrimSpace(entText(destE))
                switch typeC.GetActive() {
                case 1:
                        t.Type = "script"
                case 2:
                        t.Type = "copy"
                default:
                        t.Type = "command"
                }
                t.Source = tvText(srcTv)
        }
        dlg.Destroy()
        if res != gtk.RESPONSE_OK {
                return src, false
        }
        return t, true
}

var _ = glib.TimeoutAdd

// taskTypeLabel/taskTypeTag — цветная маркировка типа задачи (v1.13.0).
func taskTypeLabel(t string) string {
        switch t {
        case "command":
                return "команда"
        case "script":
                return "скрипт"
        case "copy":
                return "копия"
        case "replace":
                return "замена"
        case "replace_with_cmds":
                return "замена+команды"
        case "edit":
                return "правка"
        }
        return t
}

func taskTypeTag(t string) string {
        switch t {
        case "command":
                return "tcommand"
        case "script":
                return "tscript"
        case "copy":
                return "tcopy"
        case "edit":
                return "tedit"
        }
        return "dim"
}
