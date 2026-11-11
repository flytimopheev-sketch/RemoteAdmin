package main

import (
	"time"

        "fmt"
        "strings"

        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

// SchedUI — вкладка «Планировщик»: списки расписаний с включением/выключением.
type SchedUI struct {
        widget gtk.IWidget
        tree   *gtk.TreeView
        st     *gtk.ListStore
        hidden int // индекс скрытой колонки с именем расписания
}

const (
        SCOL_NAME  = 0
        SCOL_TPL   = 1
        SCOL_GRP   = 2
        SCOL_CRON  = 3
        SCOL_NEXT  = 4
        SCOL_STAT  = 5
        SCOL_HNAME = 6 // hidden: исходное имя
)

func newSchedUI() *SchedUI {
        u := &SchedUI{}
        root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

        intro := labelNew("Планировщик запускает шаблоны задач по расписанию (cron). " +
                "Пример: «*/15 * * * *» — каждые 15 минут, «30 3 * * *» — ежедневно в 03:30.")
        iclass, _ := intro.GetStyleContext()
        iclass.AddClass("ramuted")
        intro.SetXAlign(0)
        intro.SetLineWrap(true)
        root.PackStart(intro, false, false, 0)

        toolbar := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        btnAdd := buttonNew("Новое расписание")
        addClass(btnAdd, "suggested-action")
        btnAdd.Connect("clicked", func() { u.addDialog() })
        btnEdit := buttonNew("✎ Изменить")
        btnEdit.Connect("clicked", func() { u.editSelected() })
        btnDel := buttonNew("Удалить")
        btnDel.Connect("clicked", func() { u.deleteSelected() })
        btnRun := buttonNew("Запустить сейчас")
        btnRun.SetTooltipText("Выполнить выбранное расписание немедленно, не дожидаясь времени cron")
        btnRun.Connect("clicked", func() { u.runNow() })
        btnTgl := buttonNew("Вкл/Выкл")
        btnTgl.SetTooltipText("Включить или выключить выбранные расписания")
        btnTgl.Connect("clicked", func() { u.toggleSelected() })
        toolbar.PackStart(btnAdd, false, false, 0)
        toolbar.PackStart(btnEdit, false, false, 0)
        toolbar.PackStart(btnDel, false, false, 0)
        toolbar.PackEnd(btnTgl, false, false, 0)
        toolbar.PackEnd(btnRun, false, false, 0)
        root.PackStart(toolbar, false, false, 0)

        u.tree, u.st = makeTree([]string{"Название", "Шаблон", "Группа", "Cron", "Следующий запуск", "Статус", ""},
                []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING})
        hideCol(u.tree, SCOL_HNAME)
        sc := scrolled(u.tree, 0)
        sc.SetVExpand(true)
        root.PackStart(sc, true, true, 0)

        u.refresh()
        u.widget = root
        return u
}

func (u *SchedUI) selectedIndex() string {
        m, iter, ok := selIter(u.tree)
        if !ok {
                return ""
        }
        return iterGetString(m, iter, SCOL_HNAME)
}

func (u *SchedUI) refresh() {
        if u.st == nil {
                return
        }
        u.st.Clear()
        for i := range store.State.Schedules {
                s := store.State.Schedules[i]
                status := "⏸ выключено"
                if s.Enabled {
                        status = "▶ включено"
                }
                iter := u.st.Append()
                _ = u.st.Set(iter,
                        []int{SCOL_NAME, SCOL_TPL, SCOL_GRP, SCOL_CRON, SCOL_NEXT, SCOL_STAT, SCOL_HNAME},
                        []interface{}{s.Name, s.Template, s.Group, s.Cron, nextRunText(s), status, s.Name})
        }
}

func (u *SchedUI) addDialog() {
        u.editDialog(nil)
}

func (u *SchedUI) editSelected() {
        name := u.selectedIndex()
        if name == "" {
                warnDialog(app.window, "Выберите расписание в списке")
                return
        }
        for i := range store.State.Schedules {
                if store.State.Schedules[i].Name == name {
                        u.editDialog(&store.State.Schedules[i])
                        return
                }
        }
}

// editDialog — создание/правка одного расписания.
func (u *SchedUI) editDialog(orig *Schedule) {
        isNew := orig == nil
        d, err := gtk.DialogNewWithButtons(
                map[bool]string{true: "Новое расписание", false: "Изменить расписание"}[isNew],
                app.window, gtk.DIALOG_MODAL,
                []interface{}{"Сохранить", gtk.RESPONSE_ACCEPT},
                []interface{}{"Отмена", gtk.RESPONSE_CANCEL},
        )
        if err != nil {
                return
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(520, 420)

        box, _ := d.GetContentArea()
        box.SetMarginStart(14)
        box.SetMarginEnd(14)
        box.SetMarginTop(10)
        box.SetMarginBottom(6)

        grid := mkGrid()
        row := 0

        eName := entryNew("Например: ежедневная проверка дисков")
        eCron := entryNew("*/15 * * * *")
        cTpl, _ := gtk.ComboBoxTextNew()
        cGrp, _ := gtk.ComboBoxTextNew()

        if isNew {
                grid.attach(labelNew("Название:"), 0, row, 1, 1)
                grid.attach(eName, 1, row, 1, 1)
                row++
        } else {
                grid.attach(labelNew("Название:"), 0, row, 1, 1)
                grid.attach(labelNew(orig.Name), 1, row, 1, 1)
                row++
        }

        // шаблоны
        tpls := []string{}
        for _, t := range store.State.Templates {
                tpls = append(tpls, t.Name)
                cTpl.AppendText(t.Name)
        }
        if len(tpls) == 0 {
                warnDialog(app.window, "Сначала создайте шаблон задач на вкладке «Шаблоны»")
                return
        }
        cTpl.SetActive(0)
        if orig != nil {
                for i, t := range tpls {
                        if t == orig.Template {
                                cTpl.SetActive(i)
                        }
                }
        }
        grid.attach(labelNew("Шаблон задач:"), 0, row, 1, 1)
        grid.attach(cTpl, 1, row, 1, 1)
        row++

        // группы
        groups := []string{"Все хосты"}
        groups = append(groups, store.Groups()...)
        for _, g := range groups {
                cGrp.AppendText(g)
        }
        cGrp.SetActive(0)
        if orig != nil {
                for i, g := range groups {
                        if g == orig.Group {
                                cGrp.SetActive(i)
                        }
                }
        }
        grid.attach(labelNew("Группа хостов:"), 0, row, 1, 1)
        grid.attach(cGrp, 1, row, 1, 1)
        row++

        grid.attach(labelNew("Cron-выражение:"), 0, row, 1, 1)
        grid.attach(eCron, 1, row, 1, 1)
        row++

        hint := labelNew("")
        iclass, _ := hint.GetStyleContext()
        iclass.AddClass("ramuted")
        hint.SetXAlign(0)
        hint.SetLineWrap(true)
        humanize := func() {
                expr := entText(eCron)
                if next, err := cronNext(expr, time.Now()); err == nil {
                        hint.SetText(fmt.Sprintf("Расшифровка: %s\nСледующий запуск: %s", cronHuman(expr), next.Format("02.01.2006 15:04")))
                } else {
                        hint.SetText("Ошибка: " + err.Error())
                }
        }
        eCron.Connect("changed", humanize)
        grid.attach(hint, 1, row, 1, 1)
        row++

        chEn, _ := gtk.CheckButtonNewWithLabel("Расписание включено")
        if isNew {
                chEn.SetActive(true)
        } else {
                chEn.SetActive(orig.Enabled)
        }
        grid.attach(chEn, 1, row, 1, 1)
        row++

        box.PackStart(grid.w, false, false, 6)

        // пресеты для быстрого ввода
        presets := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        presets.PackStart(labelNew("Готовые:"), false, false, 2)
        addPreset := func(label, expr string) {
                b := buttonNew(label)
                b.SetTooltipText(expr)
                b.Connect("clicked", func() { eCron.SetText(expr) })
                presets.PackStart(b, false, false, 0)
        }
        addPreset("каждые 15 мин", "*/15 * * * *")
        addPreset("каждый час", "0 * * * *")
        addPreset("ежедневно 03:30", "30 3 * * *")
        addPreset("по будням 09:00", "0 9 * * 1-5")
        box.PackStart(presets, false, false, 0)

        if orig != nil {
                eCron.SetText(orig.Cron)
        }
        humanize()
        box.ShowAll()

        if d.Run() == gtk.RESPONSE_ACCEPT {
                expr := strings.TrimSpace(entText(eCron))
                if _, err := parseCron(expr); err != nil {
                        errDialog(app.window, "Cron-выражение неверно:\n"+err.Error())
                        return
                }
                if isNew {
                        name := strings.TrimSpace(entText(eName))
                        if name == "" {
                                warnDialog(app.window, "Укажите название расписания")
                                return
                        }
                        if store.ScheduleByName(name) != nil {
                                warnDialog(app.window, "Расписание с таким названием уже есть")
                                return
                        }
                        store.State.Schedules = append(store.State.Schedules, Schedule{
                                Name: name, Template: cTpl.GetActiveText(), Group: cGrp.GetActiveText(),
                                Cron: expr, Enabled: chEn.GetActive(),
                        })
                        logf("ok", "⏰ Создано расписание «%s»: %s → %s (%s)", name, cTpl.GetActiveText(), cGrp.GetActiveText(), expr)
                } else {
                        orig.Template = cTpl.GetActiveText()
                        orig.Group = cGrp.GetActiveText()
                        orig.Cron = expr
                        orig.Enabled = chEn.GetActive()
                        logf("ok", "⏰ Изменено расписание «%s»", orig.Name)
                }
                _ = store.Save()
                u.refresh()
        }
}

func (u *SchedUI) deleteSelected() {
        name := u.selectedIndex()
        if name == "" {
                warnDialog(app.window, "Выберите расписание в списке")
                return
        }
        if !confirmDialog(app.window, fmt.Sprintf("Удалить расписание «%s»?", name)) {
                return
        }
        store.RemoveSchedule(name)
        _ = store.Save()
        u.refresh()
        logf("info", "🗑 Расписание «%s» удалено", name)
}

func (u *SchedUI) toggleSelected() {
        name := u.selectedIndex()
        if name == "" {
                warnDialog(app.window, "Выберите расписание в списке")
                return
        }
        for i := range store.State.Schedules {
                if store.State.Schedules[i].Name == name {
                        store.State.Schedules[i].Enabled = !store.State.Schedules[i].Enabled
                        st := "включено"
                        if !store.State.Schedules[i].Enabled {
                                st = "выключено"
                        }
                        logf("info", "⏰ Расписание «%s» — %s", name, st)
                        break
                }
        }
        _ = store.Save()
        u.refresh()
}

func (u *SchedUI) runNow() {
        name := u.selectedIndex()
        if name == "" {
                warnDialog(app.window, "Выберите расписание в списке")
                return
        }
        for i := range store.State.Schedules {
                if store.State.Schedules[i].Name == name {
                        s := store.State.Schedules[i]
                        s.LastRun = time.Now().Format("2006-01-02 15:04:05")
                        go runScheduled(s, time.Now())
                        u.refresh()
                        return
                }
        }
}
