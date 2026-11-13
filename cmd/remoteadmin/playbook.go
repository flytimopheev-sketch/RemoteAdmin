package main

// playbook.go — вкладка «Плейбук»: редактор последовательности задач.
//
// v1.7.0:
//   * подсказка (tooltip) с ПОЛНЫМ текстом команды при наведении на строку;
//   * перетаскивание задач мышью (drag-and-drop) в дополнение к стрелкам;
//   * подсветка синтаксиса bash в редакторе команд;
//   * кнопка «Проверить синтаксис» (кавычки/скобки/блоки if-fi/do-done);
//   * выбор хостов-получателей прямо во вкладке (кнопка «Цель: …»);
//   * переменные {{ var }} — глобальные и по хостам (кнопка «Переменные…»);
//   * галочка «Прервать при ошибке» + условие выполнения каждой задачи;
//   * новые типы задач: «Служба systemctl» и «Пакеты dnf/yum».

import (
        "fmt"
        "net"
        "os"
        "path/filepath"
        "strings"
        "sync"
        "time"

        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
        "golang.org/x/crypto/ssh"
)

type pbType struct {
        Key string
        Ru  string
}

var pbTypes = []pbType{
        {"command", "Команда"},
        {"script", "Скрипт"},
        {"copy", "Копировать"},
        {"replace", "Заменить файл"},
        {"replace_with_cmds", "Заменить + команды"},
        {"edit", "Редактировать"},
        {"service", "Служба systemctl"},
        {"package", "Пакеты dnf/yum"},
}

func pbTypeRu(key string) string {
        for _, t := range pbTypes {
                if t.Key == key {
                        return t.Ru
                }
        }
        return key
}

// serviceActions / pkgActions — подписи и ключи действий для комбо «Действие».
var serviceActions = []struct{ Key, Ru string }{
        {"restart", "Перезапустить (restart)"},
        {"start", "Запустить (start)"},
        {"stop", "Остановить (stop)"},
        {"reload", "Перечитать конфигурацию (reload)"},
        {"status", "Показать статус (status)"},
        {"enable", "Включить автозапуск (enable)"},
        {"disable", "Выключить автозапуск (disable)"},
}

var pkgActions = []struct{ Key, Ru string }{
        {"install", "Установить (install)"},
        {"remove", "Удалить (remove)"},
        {"upgrade", "Обновить все пакеты (upgrade)"},
        {"info", "Информация о пакете (info)"},
}

var runIfOptions = []struct{ Key, Ru string }{
        {"always", "Всегда"},
        {"on_success", "Только если предыдущая успешна"},
        {"on_failure", "Только если предыдущая упала"},
}

// колонки таблицы задач
const (
        pbColNum  = 0
        pbColDesc = 1
        pbColFull = 2 // скрытая: полный текст для tooltip
)

// PlaybookUI is the task sequence editor tab.
type PlaybookUI struct {
        widget      gtk.IWidget
        tree        *gtk.TreeView
        st          *gtk.ListStore
        typeCombo   *gtk.ComboBoxText
        actionCombo *gtk.ComboBoxText
        runIfCombo  *gtk.ComboBoxText
        form        *simpleGrid
        sourceE     *gtk.Entry
        sourceBtn   *gtk.Button // «Обзор…» — выбрать локальный скрипт/файл
        cmdEd       *bashEditor // редактор команды с подсветкой bash
        srcRow      *gtk.Box    // строка «источник» (вне типа «Команда»)
        cmdScroll   *gtk.ScrolledWindow
        destE       *gtk.Entry
        searchE     *gtk.Entry
        replaceE    *gtk.Entry
        serviceE    *gtk.Entry
        postEd      *bashEditor
        rootC       *gtk.CheckButton
        hostBtn     *gtk.Button
        srcLabel    *gtk.Label
        rowLabels   map[string]*gtk.Label // подписи строк формы (скрываются вместе с полями)
        hostLbl     string
        hostPop     *gtk.Popover
        updatingAct bool

        emptyLbl *gtk.Label // пустое состояние списка задач

        // --- v1.9.0: метаданные, цель, динамический запуск, настройки выполнения ---
        metaNameE    *gtk.Entry
        metaDescE    *gtk.Entry
        tgtLbl       *gtk.Label
        runBtn       *gtk.Button
        runHint      *gtk.Label
        parS         *gtk.SpinButton
        runTO        *gtk.SpinButton
        errPolC      *gtk.ComboBoxText
        stepNameE    *gtk.Entry
        stepTimeoutS *gtk.SpinButton
        rcS          *gtk.SpinButton
        allowRetryC  *gtk.CheckButton
        retryCntS    *gtk.SpinButton
        retryDelS    *gtk.SpinButton
        metaSched    bool
}

func newPlaybookUI() *PlaybookUI {
        u := &PlaybookUI{}
        root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

        // ===== v1.9.0: карточка меты — название, цель, запуск, настройки =====
        metaCard, metaBox := cardBox("")
        u.metaNameE = entryNew("Название плейбука (напр. «Проверка состояния дисков»)")
        u.metaNameE.SetText(store.State.PlaybookName)
        u.metaDescE = entryNew("Описание: что делает плейбук и когда его запускать")
        u.metaDescE.SetText(store.State.PlaybookDescription)
        u.connectMetaSave(u.metaNameE)
        u.connectMetaSave(u.metaDescE)
        metaBox.PackStart(u.metaNameE, false, false, 2)
        metaBox.PackStart(u.metaDescE, false, false, 2)

        u.tgtLbl = labelNew("Цель: —")
        u.tgtLbl.SetXAlign(0)
        addClass(u.tgtLbl, "ratitle")
        metaBox.PackStart(u.tgtLbl, false, false, 2)

        // ===== нижняя область: список задач + форма =====

        // ===== left: task list =====
        leftCard, leftBox := cardBox("Последовательность задач")
        u.tree, u.st = makeTreeM([]string{"#", "Задача", ""}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING}, map[int]bool{pbColDesc: true})
        hideCol(u.tree, pbColFull)
        // подсказка с ПОЛНЫМ текстом задачи (команда не обрезается)
        u.tree.SetTooltipColumn(pbColFull)
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

        // пустое состояние: вместо пустого прямоугольника — подсказка
        u.emptyLbl = labelNew("Список задач пуст.\nВыберите тип задачи справа и нажмите «Добавить задачу».\nИли начните с шаблона на вкладке «Шаблоны».")
        addClass(u.emptyLbl, "raempty")
        u.emptyLbl.SetJustify(gtk.JUSTIFY_CENTER)
        leftBox.PackStart(u.emptyLbl, false, false, 8)

        // ---- drag-and-drop: перетаскивание задач мышью ----
        u.setupDnD()

        btnRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnUp := buttonNew("↑")
        btnUp.SetTooltipText("Переместить задачу вверх (можно просто перетащить мышью)")
        btnUp.Connect("clicked", func() { u.move(-1) })
        btnDown := buttonNew("↓")
        btnDown.SetTooltipText("Переместить задачу вниз")
        btnDown.Connect("clicked", func() { u.move(1) })
        btnDel := buttonNew("🗑")
        btnDel.SetTooltipText("Удалить задачу (Delete)")
        btnDel.Connect("clicked", func() { u.removeSelected() })
        btnRow.PackStart(btnUp, false, false, 0)
        btnRow.PackStart(btnDown, false, false, 0)
        btnRow.PackStart(btnDel, false, false, 0)
        // «Очистить всё» — в стороне от кнопки запуска (во избежание случайного
        // клика): иконка корзины в строке инструментов списка + подтверждение.
        btnClearAll := buttonNew("🗑 Очистить всё")
        ctxCls, _ := btnClearAll.GetStyleContext()
        ctxCls.AddClass("destructive")
        btnClearAll.SetTooltipText("Удалить ВСЕ задачи плейбука (с подтверждением)")
        btnClearAll.Connect("clicked", func() {
                n := len(store.State.Playbook)
                if n == 0 {
                        warnDialog(app.window, "Список задач и так пуст")
                        return
                }
                msg := fmt.Sprintf("Удалить все %d задач плейбука?\n\nДействие необратимо (шаблоны останутся).", n)
                if confirmDialog(app.window, msg) {
                        store.State.Playbook = nil
                        _ = store.Save()
                        u.refresh()
                        logf("warn", "🗑 Список задач плейбука очищен (%d задач)", n)
                }
        })
        btnRow.PackEnd(btnClearAll, false, false, 0)
        dndHint := labelNew("↷ задачи можно перетаскивать мышью")
        addClass(dndHint, "ramuted")
        dndHint.SetXAlign(0)
        btnRow.PackStart(dndHint, true, true, 0)
        leftBox.PackStart(btnRow, false, false, 0)

        // ---- run controls (v1.9.0: в карточке меты, динамическая кнопка) ----
        runRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        u.runBtn = buttonNew("▶ Запустить плейбук")
        scr, _ := u.runBtn.GetStyleContext()
        scr.AddClass("suggested")
        u.runBtn.SetTooltipText("Выполнить задачи на выбранных хостах")
        u.runBtn.Connect("clicked", func() { u.runPlaybook() })

        // «Проверить перед запуском»: параметры, доступность хостов, права
        btnPreflight := buttonNew("Проверить перед запуском")
        btnPreflight.SetTooltipText("Проверить заполненность параметров, доступность хостов и наличие прав.\n\nПроверка не гарантирует безопасность произвольных команд — просмотрите команды перед запуском.")
        btnPreflight.Connect("clicked", func() { u.preflight() })

        // кнопка выбора хостов-получателей прямо здесь (без переключения вкладок)
        u.hostBtn = buttonNew("Цель: все")
        u.hostBtn.SetTooltipText("Выбрать хосты для запуска плейбука")
        u.hostBtn.Connect("clicked", func() { u.showHostPicker() })

        // «Запланировать» — будильник рядом с запуском: создаёт шаблон из
        // текущего плейбука и открывает диалог расписания (вкладка «Планировщик»)
        btnSched := buttonNew("⏰ Запланировать")
        btnSched.SetTooltipText("Запланировать регулярный запуск этого плейбука\n(план сохраняется как шаблон → расписание с cron)")
        btnSched.Connect("clicked", func() { u.schedulePlaybook() })

        runRow.PackStart(u.runBtn, true, true, 0)
        runRow.PackStart(btnPreflight, false, false, 0)
        runRow.PackStart(u.hostBtn, true, true, 0)
        runRow.PackStart(btnSched, false, false, 0)
        metaBox.PackStart(runRow, false, false, 2)

        // подсказка: почему запуск недоступен / готовность
        u.runHint = labelNew("")
        u.runHint.SetXAlign(0)
        u.runHint.SetLineWrap(true)
        addClass(u.runHint, "ramuted")
        metaBox.PackStart(u.runHint, false, false, 0)

        // настройки выполнения всего запуска (v1.9.0)
        setRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        setRow.PackStart(labelNew("Параллельно хостов:"), false, false, 0)
        u.parS, _ = gtk.SpinButtonNewWithRange(1, 64, 1)
        u.parS.SetValue(float64(orInt(store.State.PlaybookParallel, 3)))
        u.parS.SetTooltipText("Сколько хостов обрабатывать одновременно")
        setRow.PackStart(u.parS, false, false, 0)
        setRow.PackStart(labelNew("Общий таймаут, с (0 — без):"), false, false, 0)
        u.runTO, _ = gtk.SpinButtonNewWithRange(0, 86400, 60)
        u.runTO.SetValue(float64(store.State.PlaybookRunTimeoutSec))
        setRow.PackStart(u.runTO, false, false, 0)
        setRow.PackStart(labelNew("При ошибке:"), false, false, 0)
        u.errPolC, _ = gtk.ComboBoxTextNew()
        u.errPolC.AppendText("Остановиться")
        u.errPolC.AppendText("Продолжить")
        u.errPolC.AppendText("Спросить")
        switch store.State.effectiveErrPolicy() {
        case "continue":
                u.errPolC.SetActive(1)
        case "ask":
                u.errPolC.SetActive(2)
        default:
                u.errPolC.SetActive(0)
        }
        u.errPolC.SetTooltipText("Политика ошибки: остановиться / продолжить / спросить у пользователя")
        setRow.PackStart(u.errPolC, false, false, 0)
        btnVars := buttonNew("Переменные…")
        btnVars.SetTooltipText("Переменные {{ var }}: глобальные и для конкретных хостов")
        btnVars.Connect("clicked", func() { showPlaybookVarsDialog() })
        btnSecrets := buttonNew("🔐 Секреты…")
        btnSecrets.SetTooltipText("Менеджер секретов: пароли/токены хранятся зашифрованными\nи подставляются по ссылке {{ secret:имя }}")
        btnSecrets.Connect("clicked", func() { showSecretsManager() })
        btnTpl := buttonNew("Сохранить как шаблон")
        btnTpl.Connect("clicked", func() { u.saveAsTemplate() })
        setRow.PackStart(btnVars, false, false, 0)
        setRow.PackStart(btnSecrets, false, false, 0)
        setRow.PackStart(btnTpl, false, false, 0)
        metaBox.PackStart(setRow, false, false, 2)
        u.connectRunSettings()

        // ===== right: task form =====
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

        // источник: Entry+Обзор для файлов/путей, TextView с подсветкой для команд
        u.sourceE = entryNew("Источник / команда")
        u.sourceBtn = buttonNew("Обзор…")
        u.sourceBtn.SetTooltipText("Выбрать локальный файл или скрипт")
        u.sourceBtn.Connect("clicked", func() {
                fn := fileChooser(app.window, "Выберите скрипт или файл", gtk.FILE_CHOOSER_ACTION_OPEN, "")
                if fn != "" {
                        u.sourceE.SetText(fn)
                }
        })
        u.srcRow = boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        u.srcRow.PackStart(u.sourceE, true, true, 0)
        u.srcRow.PackStart(u.sourceBtn, false, false, 0)
        // редактор команды с подсветкой bash
        u.cmdEd = newBashEditor(64)
        u.cmdEd.tv.SetTooltipText("Введите команду; поддерживаются переменные {{ var }}, подсветка bash включена")
        u.cmdScroll = scrolled(u.cmdEd.tv, 64)
        srcStack := boxNew(gtk.ORIENTATION_VERTICAL, 0)
        srcStack.PackStart(u.srcRow, false, false, 0)
        srcStack.PackStart(u.cmdScroll, false, false, 0)

        u.destE = entryNew("Назначение (напр. /etc/app.conf)")
        u.searchE = entryNew("Найти текст")
        u.replaceE = entryNew("Заменить на")
        u.serviceE = entryNew("Служба для перезапуска (опц.)")
        u.postEd = newBashEditor(52)
        u.postEd.tv.SetTooltipText("По одной команде на строку; подсветка и проверка синтаксиса")
        postScroll := scrolled(u.postEd.tv, 52)

        u.actionCombo, _ = gtk.ComboBoxTextNew()
        u.actionCombo.Connect("changed", func() {
                if u.updatingAct {
                        return
                }
        })
        u.fillActions()

        u.runIfCombo, _ = gtk.ComboBoxTextNew()
        for _, r := range runIfOptions {
                u.runIfCombo.AppendText(r.Ru)
        }
        u.runIfCombo.SetActive(0)
        u.runIfCombo.SetTooltipText("Условие выполнения задачи (аналог ignore_errors / when из Ansible)")

        u.rootC, _ = gtk.CheckButtonNewWithLabel("Выполнять с правами root (su)")

        u.rowLabels = map[string]*gtk.Label{}
        mkRow := func(key, title string, row int, field gtk.IWidget) {
                lab := labelNew(title)
                u.rowLabels[key] = lab
                u.form.attach(lab, 0, row, 1, 1)
                u.form.attach(field, 1, row, 1, 1)
        }
        lbl0 := labelNew("Источник:")
        u.srcLabel = lbl0
        // v1.9.0: осмысленное название шага — первая строка формы
        u.stepNameE = entryNew("Название шага (напр. «Собрать SMART-показатели»)")
        mkRow("stepname", "Название шага:", 0, u.stepNameE)
        u.form.attach(lbl0, 0, 1, 1, 1)
        u.form.attach(srcStack, 1, 1, 1, 1)
        mkRow("dest", "Назначение:", 2, u.destE)
        mkRow("find", "Найти:", 3, u.searchE)
        mkRow("repl", "Заменить:", 4, u.replaceE)
        mkRow("svc", "Служба:", 5, u.serviceE)
        mkRow("post", "Команды после:", 6, postScroll)
        mkRow("act", "Действие:", 7, u.actionCombo)
        // условие + поле кода возврата (rc:N) в одной строке
        runIfRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        u.rcS, _ = gtk.SpinButtonNewWithRange(0, 255, 1)
        u.rcS.SetTooltipText("Шаг запустится, если предыдущий вернёт этот код возврата")
        runIfRow.PackStart(u.runIfCombo, true, true, 0)
        runIfRow.PackStart(u.rcS, false, false, 0)
        mkRow("runif", "Выполнять:", 8, runIfRow)
        u.form.attach(u.rootC, 1, 9, 1, 1)
        rightBox.PackStart(u.form.w, false, false, 0)

        // v1.9.0: таймаут шага и повторы — в раскрытии
        expCtl, _ := gtk.ExpanderNew("Таймаут, повторы и разрешение повторного запуска")
        expGrid := mkGrid()
        expGrid.attach(labelNew("Таймаут шага, с (0 — общий):"), 0, 0, 1, 1)
        u.stepTimeoutS, _ = gtk.SpinButtonNewWithRange(0, 86400, 30)
        expGrid.attach(u.stepTimeoutS, 1, 0, 1, 1)
        u.allowRetryC, _ = gtk.CheckButtonNewWithLabel("Разрешён повторный запуск (авто-повторы и «Повторить неудачные»)")
        u.allowRetryC.SetTooltipText("Приложение не решает за вас, безопасно ли повторять команду:\nповторы работают только для задач с этим флагом")
        expGrid.attach(u.allowRetryC, 1, 1, 1, 1)
        expGrid.attach(labelNew("Авто-повторов:"), 0, 2, 1, 1)
        u.retryCntS, _ = gtk.SpinButtonNewWithRange(0, 10, 1)
        expGrid.attach(u.retryCntS, 1, 2, 1, 1)
        expGrid.attach(labelNew("Пауза между, с:"), 0, 3, 1, 1)
        u.retryDelS, _ = gtk.SpinButtonNewWithRange(0, 600, 5)
        u.retryDelS.SetValue(3)
        expGrid.attach(u.retryDelS, 1, 3, 1, 1)
        expCtl.Add(expGrid.w)
        rightBox.PackStart(expCtl, false, false, 0)

        formBtns := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnAdd := buttonNew("Добавить задачу")
        btnAdd.Connect("clicked", func() { u.addOrUpdate(false) })
        btnApply := buttonNew("Обновить выбранную")
        btnApply.Connect("clicked", func() { u.addOrUpdate(true) })
        btnCheck := buttonNew("Проверить синтаксис")
        btnCheck.SetTooltipText("Проверить кавычки, скобки и парные блоки if/fi, for/do-done, case/esac")
        btnCheck.Connect("clicked", func() { u.checkSyntax() })
        formBtns.PackStart(btnAdd, true, true, 0)
        formBtns.PackStart(btnApply, true, true, 0)
        formBtns.PackStart(btnCheck, true, true, 0)
        rightBox.PackStart(formBtns, false, false, 0)

        hint := labelNew("Переменные: {{ hostname }} {{ ip }} {{ user }} {{ date }} и свои из «Переменные…»;\nпароли — через {{ secret:имя }} из «Секреты…» (не пишутся в журнал)")
        addClass(hint, "ramuted")
        hint.SetLineWrap(true)
        rightBox.PackStart(hint, false, false, 2)

        root.PackStart(metaCard, false, false, 0)
        // левая и правая панели регулируются мышью: границу можно тянуть
        // влево-вправо, положение запоминается между сеансами
        root.PackStart(makeHPaned("playbook-lr", leftCard, rightCard, 520), true, true, 0)

        u.refresh()
        u.widget = root
        glibIdle(func() { u.updateFormVisibility() })
        u.refreshTargetLine()
        u.startTargetTicker()
        return u
}

// fillActions заполняет комбо «Действие» в зависимости от типа задачи.
func (u *PlaybookUI) fillActions() {
        u.updatingAct = true
        defer func() { u.updatingAct = false }()
        u.actionCombo.RemoveAll()
        t := u.currentType()
        switch t {
        case "service":
                for _, a := range serviceActions {
                        u.actionCombo.AppendText(a.Ru)
                }
                u.actionCombo.SetActive(0)
        case "package":
                for _, a := range pkgActions {
                        u.actionCombo.AppendText(a.Ru)
                }
                u.actionCombo.SetActive(0)
        }
}

func entryNew(placeholder string) *gtk.Entry {
        e, _ := gtk.EntryNew()
        e.SetPlaceholderText(placeholder)
        return e
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
        if u.typeCombo == nil {
                return "command"
        }
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

func (u *PlaybookUI) setAction(k string) {
        list := pkgActions
        if u.currentType() == "service" {
                list = serviceActions
        }
        for i, a := range list {
                if a.Key == k {
                        u.actionCombo.SetActive(i)
                        return
                }
        }
        u.actionCombo.SetActive(0)
}

func (u *PlaybookUI) currentAction() string {
        t := u.currentType()
        idx := u.actionCombo.GetActive()
        switch t {
        case "service":
                if idx >= 0 && idx < len(serviceActions) {
                        return serviceActions[idx].Key
                }
        case "package":
                if idx >= 0 && idx < len(pkgActions) {
                        return pkgActions[idx].Key
                }
        }
        return ""
}

func (u *PlaybookUI) currentRunIf() string {
        idx := u.runIfCombo.GetActive()
        if idx > 0 && idx < len(runIfOptions) {
                if runIfOptions[idx].Key == "rc" {
                        return fmt.Sprintf("rc:%d", u.rcS.GetValueAsInt())
                }
                return runIfOptions[idx].Key
        }
        return "always"
}

func (u *PlaybookUI) setRunIf(k string) {
        if strings.HasPrefix(k, "rc:") {
                u.runIfCombo.SetActive(3) // «При коде возврата…»
                n := 0
                fmt.Sscanf(strings.TrimPrefix(k, "rc:"), "%d", &n)
                u.rcS.SetValue(float64(n))
                return
        }
        for i, r := range runIfOptions {
                if r.Key == k {
                        u.runIfCombo.SetActive(i)
                        return
                }
        }
        u.runIfCombo.SetActive(0)
}

func setVis(w gtk.IWidget, show bool) {
        if lv, ok := w.(interface{ SetVisible(bool) }); ok {
                lv.SetVisible(show)
        }
}

func (u *PlaybookUI) updateFormVisibility() {
        t := u.currentType()
        setVis(u.srcRow, t != "command" && t != "edit")
        setVis(u.cmdScroll, t == "command")
        vis := u.rowLabels
        hideShow := func(key string, show bool) {
                if l, ok := vis[key]; ok {
                        setVis(l, show)
                }
        }
        copyRow := t == "copy" || t == "replace" || t == "replace_with_cmds"
        setVis(u.destE, copyRow)
        hideShow("dest", copyRow)
        setVis(u.searchE, t == "edit")
        hideShow("find", t == "edit")
        setVis(u.replaceE, t == "edit")
        hideShow("repl", t == "edit")
        setVis(u.serviceE, t == "edit")
        hideShow("svc", t == "edit")
        if par, perr := u.postEd.tv.GetParent(); perr == nil && par != nil {
                setVis(par, t == "replace_with_cmds")
        }
        hideShow("post", t == "replace_with_cmds")
        actRow := t == "service" || t == "package"
        setVis(u.actionCombo, actRow)
        hideShow("act", actRow)
        u.fillActions()
        // v1.9.0: поле кода возврата видно только для «При коде возврата…»
        if u.rcS != nil && u.runIfCombo != nil {
                setVis(u.rcS, u.runIfCombo.GetActive() == 3)
        }
        // подпись строки источника по типу задачи
        if u.srcLabel != nil {
                switch t {
                case "command":
                        u.srcLabel.SetText("Команда:")
                case "service":
                        u.srcLabel.SetText("Служба:")
                case "package":
                        u.srcLabel.SetText("Пакеты:")
                default:
                        u.srcLabel.SetText("Источник:")
                }
        }
}

// formToTask собирает задачу из формы.
func (u *PlaybookUI) formToTask() PlaybookTask {
        t := PlaybookTask{
                Type:           u.currentType(),
                Action:         u.currentAction(),
                Name:           strings.TrimSpace(entText(u.stepNameE)),
                TimeoutSec:     u.stepTimeoutS.GetValueAsInt(),
                AllowRetry:     u.allowRetryC.GetActive(),
                RetryCount:     u.retryCntS.GetValueAsInt(),
                RetryDelaySec:  u.retryDelS.GetValueAsInt(),
                Dest:           entText(u.destE),
                Search:         entText(u.searchE),
                Replace:        entText(u.replaceE),
                RestartService: entText(u.serviceE),
                Root:           u.rootC.GetActive(),
                RunIf:          u.currentRunIf(),
        }
        if t.Type == "command" {
                t.Source = u.cmdEd.text()
        } else {
                t.Source = entText(u.sourceE)
        }
        if t.Type == "replace_with_cmds" {
                s := u.postEd.text()
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
        u.setAction(t.Action)
        u.stepNameE.SetText(t.Name)
        u.stepTimeoutS.SetValue(float64(t.TimeoutSec))
        u.allowRetryC.SetActive(t.AllowRetry)
        u.retryCntS.SetValue(float64(t.RetryCount))
        u.retryDelS.SetValue(float64(orInt(t.RetryDelaySec, 3)))
        if t.Type == "command" {
                u.cmdEd.setText(t.Source)
                u.sourceE.SetText("")
        } else {
                u.sourceE.SetText(t.Source)
                u.cmdEd.setText("")
        }
        u.destE.SetText(t.Dest)
        u.searchE.SetText(t.Search)
        u.replaceE.SetText(t.Replace)
        u.serviceE.SetText(t.RestartService)
        u.rootC.SetActive(t.Root)
        u.setRunIf(t.RunIf)
        u.postEd.setText(strings.Join(t.PostCommands, "\n"))
        u.updateFormVisibility()
}

func (u *PlaybookUI) clearForm() {
        u.stepNameE.SetText("")
        u.stepTimeoutS.SetValue(0)
        u.allowRetryC.SetActive(false)
        u.retryCntS.SetValue(0)
        u.retryDelS.SetValue(3)
        u.sourceE.SetText("")
        u.cmdEd.setText("")
        u.destE.SetText("")
        u.searchE.SetText("")
        u.replaceE.SetText("")
        u.serviceE.SetText("")
        u.rootC.SetActive(false)
        u.runIfCombo.SetActive(0)
        u.postEd.setText("")
}

func (u *PlaybookUI) addOrUpdate(update bool) {
        t := u.formToTask()
        // validation
        if t.Type != "edit" && strings.TrimSpace(t.Source) == "" {
                warnDialog(app.window, "Укажите источник (команду, службу, пакеты или путь)")
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
        if t.Type == "package" && t.Action != "upgrade" && t.Action != "info" && strings.TrimSpace(t.Source) == "" {
                warnDialog(app.window, "Укажите пакеты (через пробел или запятую)")
                return
        }
        if update {
                idx := selectedInt(u.tree, pbColNum)
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
        idx := selectedInt(u.tree, pbColNum)
        if idx >= 0 && idx < len(store.State.Playbook) {
                store.State.Playbook = append(store.State.Playbook[:idx], store.State.Playbook[idx+1:]...)
                _ = store.Save()
                u.refresh()
        }
}

func (u *PlaybookUI) move(dir int) {
        idx := selectedInt(u.tree, pbColNum)
        j := idx + dir
        if idx < 0 || j < 0 || j >= len(store.State.Playbook) {
                return
        }
        pb := store.State.Playbook
        pb[idx], pb[j] = pb[j], pb[idx]
        _ = store.Save()
        u.refresh()
        u.selectRow(j)
}

func (u *PlaybookUI) selectRow(idx int) {
        m, ok := anyTreeModel(u.st)
        if !ok {
                return
        }
        path, err := gtk.TreePathNewFromString(fmt.Sprintf("%d", idx))
        if err != nil {
                return
        }
        iter, err := m.GetIter(path)
        if err != nil || iter == nil {
                return
        }
        sel, _ := u.tree.GetSelection()
        sel.SelectIter(iter)
}

// dropMove переносит задачу с позиции src на позицию вставки.
func (u *PlaybookUI) dropMove(srcIdx int, dstPath *gtk.TreePath, pos gtk.TreeViewDropPosition) {
        tasks := store.State.Playbook
        if srcIdx < 0 || srcIdx >= len(tasks) {
                return
        }
        insert := len(tasks) - 1 // сброс в пустую область — в конец
        if dstPath != nil {
                idxs := dstPath.GetIndices()
                if len(idxs) > 0 {
                        insert = idxs[0]
                        if pos == gtk.TREE_VIEW_DROP_AFTER || pos == gtk.TREE_VIEW_DROP_INTO_OR_AFTER {
                                insert++
                        }
                }
        }
        if insert > srcIdx {
                insert--
        }
        if insert == srcIdx || insert < 0 {
                return
        }
        t := tasks[srcIdx]
        tasks = append(tasks[:srcIdx], tasks[srcIdx+1:]...)
        tasks = append(tasks[:insert], append([]PlaybookTask{t}, tasks[insert:]...)...)
        store.State.Playbook = tasks
        _ = store.Save()
        u.refresh()
        u.selectRow(insert)
        logf("info", "↷ Задача перемещена: позиция %d → %d", srcIdx+1, insert+1)
}

// setupDnD включает перетаскивание задач мышью (источник и приёмник — одно дерево).
func (u *PlaybookUI) setupDnD() {
        te, _ := gtk.TargetEntryNew("text/plain", gtk.TARGET_SAME_APP, 0)
        teVal := *te
        // COPY: с ACTION_MOVE встроенный обработчик tree-view удаляет строку
        // из модели ПОСЛЕ нашего refresh() — тот же класс краха, что и в hosts.go.
        u.tree.DragSourceSet(gdk.BUTTON1_MASK, []gtk.TargetEntry{teVal}, gdk.ACTION_COPY)
        u.tree.DragDestSet(gtk.DEST_DEFAULT_HIGHLIGHT|gtk.DEST_DEFAULT_MOTION, []gtk.TargetEntry{teVal}, gdk.ACTION_COPY)

        var dropPath *gtk.TreePath
        var dropPos = gtk.TREE_VIEW_DROP_BEFORE
        u.tree.Connect("drag-motion", func(tv *gtk.TreeView, _ *gdk.DragContext, x, y int, _ uint32) bool {
                p, pos := treeViewDestRowAtPosEx(tv, x, y)
                tv.SetDragDestRow(p, pos)
                dropPath, dropPos = p, pos
                return p != nil
        })
        u.tree.Connect("drag-drop", func(tv *gtk.TreeView, ctx *gdk.DragContext, _, _ int, tm uint32) bool {
                // источник — текущее выделение (оно остаётся на перетаскиваемой строке)
                src := selectedInt(u.tree, pbColNum)
                path, pos := dropPath, dropPos
                glibIdle(func() { u.dropMove(src, path, pos) })
                dragFinish(ctx, true, false, tm)
                _ = tv
                return true
        })
}

func (u *PlaybookUI) loadSelectedIntoForm() {
        idx := selectedInt(u.tree, pbColNum)
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
                // v1.9.0: имя шага жирным + команда моноширинным второй строкой
                cmd := t.stepCommand()
                if len(cmd) > 110 {
                        cmd = cmd[:110] + "…"
                }
                title := t.StepTitle()
                markup := fmt.Sprintf("<b>%d. %s</b>\n<span font=\"Monospace 8.5\" foreground=\"#8fa0c0\">$ %s</span>",
                        i+1, escapeMarkup(title), escapeMarkup(cmd))
                // задача с повышенными правами — явная янтарная отметка
                if t.Root {
                        markup += " <span background=\"#f59e0b\" foreground=\"#0b0e14\"> 🔑root </span>"
                }
                _ = u.st.Set(iter, []int{pbColNum, pbColDesc, pbColFull},
                        []interface{}{fmt.Sprintf("%d", i+1), markup, t.DescribeFull() + "\n\n" + taskTip(t)})
        }
        if u.emptyLbl != nil {
                u.emptyLbl.SetVisible(len(store.State.Playbook) == 0)
        }
        u.updateHostBtn()
}

// taskTip — сводка настроек шага для tooltip.
func taskTip(t PlaybookTask) string {
        parts := []string{"тип: " + pbTypeRu(t.Type)}
        if t.TimeoutSec > 0 {
                parts = append(parts, fmt.Sprintf("таймаут: %d с", t.TimeoutSec))
        }
        if t.RunIf != "" && t.RunIf != "always" {
                parts = append(parts, "условие: "+runIfRu(t.RunIf))
        }
        if t.AllowRetry {
                parts = append(parts, fmt.Sprintf("повтор разрешён (до %d раз, пауза %d с)", t.RetryCount, orInt(t.RetryDelaySec, 3)))
        } else {
                parts = append(parts, "повтор запрещён")
        }
        if t.Root {
                parts = append(parts, "права: root (su)")
        }
        return strings.Join(parts, " · ")
}

// ---------- проверка синтаксиса ----------

// checkSyntax проверяет введённый текст (команда / пост-команды / скрипт)
// и показывает отчёт.
func (u *PlaybookUI) checkSyntax() {
        t := u.currentType()
        switch t {
        case "command":
                src := u.cmdEd.text()
                problems := checkBashSyntax(src)
                infoDialog(app.window, syntaxReportText("команда", problems))
        case "replace_with_cmds":
                var problems []string
                for i, line := range strings.Split(u.postEd.text(), "\n") {
                        line = strings.TrimSpace(line)
                        if line == "" {
                                continue
                        }
                        for _, p := range checkBashSyntax(line) {
                                problems = append(problems, fmt.Sprintf("команда %d: %s", i+1, p))
                        }
                }
                infoDialog(app.window, syntaxReportText("пост-команды", problems))
        case "script":
                fn := entText(u.sourceE)
                content, err := readScriptPreview(fn)
                if err != nil {
                        warnDialog(app.window, "Не удалось прочитать скрипт: "+err.Error())
                        return
                }
                problems := checkBashSyntax(content)
                infoDialog(app.window, syntaxReportText(filepath.Base(fn), problems))
        case "service":
                if strings.TrimSpace(entText(u.sourceE)) == "" {
                        infoDialog(app.window, "Укажите имя службы (напр. nginx).")
                } else {
                        infoDialog(app.window, "Проблем не найдено — служба задана.")
                }
        case "package":
                if strings.TrimSpace(entText(u.sourceE)) == "" {
                        infoDialog(app.window, "Укажите пакеты (через пробел или запятую).")
                } else {
                        infoDialog(app.window, "Проблем не найдено — пакеты заданы.")
                }
        default:
                infoDialog(app.window, "Для этого типа проверка не требуется — заполните поля источника и назначения.")
        }
}

// ---------- выбор хостов ----------

// updateHostBtn обновляет подпись кнопки «Цель: …».
func (u *PlaybookUI) updateHostBtn() {
        if u.hostBtn == nil {
                return
        }
        n := len(store.State.PlaybookHosts)
        total := len(store.State.Hosts)
        switch {
        case n == 0:
                u.hostLbl = fmt.Sprintf("Цель: все (%d)", total)
        case n == 1:
                u.hostLbl = "Цель: 1 хост"
        default:
                u.hostLbl = fmt.Sprintf("Цель: %d хоста(ов)", n)
        }
        u.hostBtn.SetLabel(u.hostLbl)
}

// showHostPicker открывает поповер со списком хостов (галочки).
func (u *PlaybookUI) showHostPicker() {
        if u.hostPop != nil {
                // пересоздаём поповер каждый раз: список хостов мог измениться
                u.hostPop.Destroy()
                u.hostPop = nil
        }
        pop, err := gtk.PopoverNew(u.hostBtn)
        if err != nil {
                return
        }
        pop.SetModal(true)
        box := boxNew(gtk.ORIENTATION_VERTICAL, 2)
        box.SetMarginStart(8)
        box.SetMarginEnd(8)
        box.SetMarginTop(6)
        box.SetMarginBottom(6)

        head := labelNew("Хосты для запуска плейбука")
        addClass(head, "ratitle")
        head.SetXAlign(0)
        box.PackStart(head, false, false, 2)

        selected := map[string]bool{}
        for _, ip := range store.State.PlaybookHosts {
                selected[ip] = true
        }

        var checks []*gtk.CheckButton
        scroll := boxNew(gtk.ORIENTATION_VERTICAL, 1)
        for _, h := range store.State.Hosts {
                ip := h.IP
                cb, _ := gtk.CheckButtonNewWithLabel(h.DisplayName() + "  (" + ip + ")")
                cb.SetActive(selected[ip])
                cb.Connect("toggled", func() {
                        if cb.GetActive() {
                                selected[ip] = true
                        } else {
                                delete(selected, ip)
                        }
                })
                checks = append(checks, cb)
                scroll.PackStart(cb, false, false, 0)
        }
        if len(store.State.Hosts) == 0 {
                box.PackStart(labelNew("Список хостов пуст"), false, false, 2)
        }
        scs, _ := gtk.ScrolledWindowNew(nil, nil)
        scs.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
        scs.Add(scroll)
        scs.SetSizeRequest(300, 260)
        box.PackStart(scs, true, true, 2)

        btnRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnAll := buttonNew("Выбрать все")
        btnAll.Connect("clicked", func() {
                for _, cb := range checks {
                        cb.SetActive(true)
                }
        })
        btnNone := buttonNew("Снять все")
        btnNone.Connect("clicked", func() {
                for _, cb := range checks {
                        cb.SetActive(false)
                }
        })
        btnOK := buttonNew("Готово")
        sOK, _ := btnOK.GetStyleContext()
        sOK.AddClass("suggested")
        btnOK.Connect("clicked", func() {
                var ips []string
                for _, h := range store.State.Hosts {
                        if selected[h.IP] {
                                ips = append(ips, h.IP)
                        }
                }
                store.State.PlaybookHosts = ips
                _ = store.Save()
                u.updateHostBtn()
                pop.Popdown()
        })
        btnRow.PackStart(btnAll, true, true, 0)
        btnRow.PackStart(btnNone, true, true, 0)
        btnRow.PackStart(btnOK, true, true, 0)
        box.PackStart(btnRow, false, false, 2)

        box.ShowAll()
        pop.Add(box)
        u.hostPop = pop
        pop.Popup()
}

// targetHostsForPlaybook — выбранные в плейбуке хосты или все видимые.
func (u *PlaybookUI) targetHostsForPlaybook() []string {
        if len(store.State.PlaybookHosts) > 0 {
                var out []string
                for _, ip := range store.State.PlaybookHosts {
                        if store.FindHost(ip) != nil {
                                out = append(out, ip)
                        }
                }
                if len(out) > 0 {
                        return out
                }
        }
        return app.hostsUI.targetHosts()
}

// ---------- v1.9.0: мета, цель, сводка, проверка, форма переменных ----------

// connectMetaSave сохраняет название/описание с задержкой (debounce).
func (u *PlaybookUI) connectMetaSave(e *gtk.Entry) {
        e.Connect("changed", func() {
                if u.metaSched {
                        return
                }
                u.metaSched = true
                glibTimeout(600, func() bool {
                        u.metaSched = false
                        u.saveMeta()
                        return false
                })
        })
}

// saveMeta записывает название/описание и настройки запуска в состояние.
func (u *PlaybookUI) saveMeta() {
        if store == nil {
                return
        }
        store.State.PlaybookName = strings.TrimSpace(entText(u.metaNameE))
        store.State.PlaybookDescription = strings.TrimSpace(entText(u.metaDescE))
        if u.parS != nil {
                store.State.PlaybookParallel = u.parS.GetValueAsInt()
                store.State.PlaybookRunTimeoutSec = u.runTO.GetValueAsInt()
                switch u.errPolC.GetActive() {
                case 1:
                        store.State.PlaybookErrPolicy = "continue"
                case 2:
                        store.State.PlaybookErrPolicy = "ask"
                default:
                        store.State.PlaybookErrPolicy = "stop"
                }
        }
        _ = store.Save()
}

// connectRunSettings — сохранение настроек запуска при изменении.
func (u *PlaybookUI) connectRunSettings() {
        save := func() { u.saveMeta() }
        u.parS.Connect("value-changed", save)
        u.runTO.Connect("value-changed", save)
        u.errPolC.Connect("changed", save)
}

// startTargetTicker — периодическое обновление строки «Цель: …».
func (u *PlaybookUI) startTargetTicker() {
        var tick func() bool
        tick = func() bool {
                if u.widget == nil {
                        return false
                }
                u.refreshTargetLine()
                glibTimeout(1500, tick)
                return false
        }
        glibTimeout(1500, tick)
}

// refreshTargetLine пересчитывает цель, доступность хостов и состояние кнопки.
func (u *PlaybookUI) refreshTargetLine() {
        if u.tgtLbl == nil || store == nil {
                return
        }
        ips := u.targetHostsForPlaybook()
        available := 0
        for _, ip := range ips {
                if h := store.FindHost(ip); h != nil && h.Status == "ok" {
                        available++
                }
        }
        src := "выбраны вручную"
        if len(store.State.PlaybookHosts) == 0 {
                src = "все видимые"
        }
        tasksN := len(store.State.Playbook)
        tgt := fmt.Sprintf("Цель: %s · %d хост(ов) · %d доступны · задач: %d", src, len(ips), available, tasksN)
        if name := strings.TrimSpace(store.State.PlaybookName); name != "" {
                tgt = fmt.Sprintf("«%s» · %s", escapeMarkup(name), tgt)
        }
        // среды выбранных хостов видны прямо в строке цели
        seenEnv := map[string]bool{}
        for _, ip := range ips {
                h := store.FindHost(ip)
                if h == nil || h.Env == "" || seenEnv[h.Env] {
                        continue
                }
                seenEnv[h.Env] = true
                tgt += " <span background=\"" + envColor(h.Env) + "\" foreground=\"#0b0e14\"> " + escapeMarkup(h.Env) + " </span>"
        }
        u.tgtLbl.SetUseMarkup(true)
        u.tgtLbl.SetMarkup(tgt)

        var reasons []string
        switch {
        case tasksN == 0:
                reasons = append(reasons, "плейбук пуст — добавьте задачи справа")
        case len(ips) == 0:
                reasons = append(reasons, "хосты не выбраны")
        }
        if len(ips) > 0 && available == 0 {
                reasons = append(reasons, "ни один из выбранных хостов не прошёл проверку доступности (выполните «Ping» на вкладке «Хосты»)")
        }
        label := "▶ Запустить плейбук"
        if len(ips) > 0 {
                label = fmt.Sprintf("▶ Запустить на %d хостах", len(ips))
        }
        glibIdle(func() {
                u.runBtn.SetLabel(label)
                if len(reasons) == 0 {
                        u.runBtn.SetSensitive(true)
                        u.runHint.SetText("Готово к запуску: от пользователя «" + u.currentUser() + "»" + u.rootSuffix())
                } else {
                        u.runBtn.SetSensitive(false)
                        u.runHint.SetText("Запуск недоступен: " + strings.Join(reasons, "; "))
                }
        })
}

func (u *PlaybookUI) currentUser() string {
        if s := strings.TrimSpace(store.State.Username); s != "" {
                return s
        }
        return "не задан"
}

func (u *PlaybookUI) rootSuffix() string {
        n := 0
        for _, t := range store.State.Playbook {
                if t.Root {
                        n++
                }
        }
        if n > 0 {
                return fmt.Sprintf(", с root (su): %d задач", n)
        }
        return ""
}

// orInt — значение или запасное, если ноль.
func orInt(v, def int) int {
        if v != 0 {
                return v
        }
        return def
}

// preflight — «Проверить перед запуском»: параметры, переменные, доступы,
// доступность хостов. Не гарантирует безопасность произвольных команд.
func (u *PlaybookUI) preflight() {
        tasks := store.State.Playbook
        var issues []string
        var warns []string

        if len(tasks) == 0 {
                issues = append(issues, "плейбук пуст — добавьте хотя бы одну задачу")
        }
        for i, t := range tasks {
                pref := fmt.Sprintf("задача %d («%s»)", i+1, t.StepTitle())
                if t.Type != "edit" && strings.TrimSpace(t.Source) == "" {
                        issues = append(issues, pref+": не заполнен источник")
                }
                if (t.Type == "copy" || t.Type == "replace" || t.Type == "replace_with_cmds") && strings.TrimSpace(t.Dest) == "" {
                        issues = append(issues, pref+": не указано назначение")
                }
                if t.Type == "edit" && strings.TrimSpace(t.Search) == "" {
                        issues = append(issues, pref+": пустой текст поиска")
                }
                if t.Type == "service" && strings.TrimSpace(t.Source) == "" {
                        issues = append(issues, pref+": не указано имя службы")
                }
                if t.Type == "package" && t.Action != "upgrade" && t.Action != "info" && strings.TrimSpace(t.Source) == "" {
                        issues = append(issues, pref+": не указаны пакеты")
                }
        }
        // переменные: ссылки {{…}} должны быть объявлены или иметь встроенное значение
        for _, ref := range collectVarRefs(tasks) {
                if !store.varExists(ref) {
                        issues = append(issues, "переменная {{"+ref+"}} используется в задачах, но не задана (кнопка «Переменные…»)")
                }
        }
        // доступы
        if store.State.Username == "" {
                issues = append(issues, "не указан логин SSH (панель «Действия»)")
        }
        if store.Password() == "" && store.State.KeyPath == "" {
                issues = append(issues, "не указан пароль или путь к SSH-ключу")
        }
        // root
        rootNeeded := false
        for _, t := range tasks {
                if t.Root {
                        rootNeeded = true
                }
        }
        if rootNeeded && store.RootPassword() == "" && store.State.Username != "root" {
                issues = append(issues, "задачи требуют root (su), но пароль root не задан")
        }
        // хосты
        ips := u.targetHostsForPlaybook()
        if len(ips) == 0 {
                issues = append(issues, "хосты не выбраны")
        }
        if len(ips) > 0 && len(tasks) > 0 && store.State.Username != "" {
                unreachable := probeHosts(ips, 3*time.Second)
                for _, ip := range unreachable {
                        issues = append(issues, "хост "+ip+": нет соединения (TCP "+portOf(ip)+")")
                }
                if len(unreachable) == 0 {
                        warns = append(warns, "все выбранные хосты доступны по сети")
                }
        }

        var b strings.Builder
        if len(issues) == 0 {
                b.WriteString("<b>Проблем не найдено.</b>\n\n")
        } else {
                b.WriteString(fmt.Sprintf("<b>Найдено проблем: %d</b>\n\n", len(issues)))
                for _, s := range issues {
                        b.WriteString("• " + escapeMarkup(s) + "\n")
                }
        }
        if len(warns) > 0 {
                b.WriteString("\n")
                for _, s := range warns {
                        b.WriteString("✔ " + escapeMarkup(s) + "\n")
                }
        }
        b.WriteString("\n<span foreground=\"#fbbf24\">Проверка не гарантирует безопасность произвольных команд: она проверяет заполненность параметров, доступность хостов и наличие прав, но не оценивает последствия команд.</span>")
        infoDialogMarkup(app.window, "Проверка перед запуском", b.String())
}

func portOf(ip string) string {
        if _, p, ok := splitHostPort(ip); ok {
                return fmt.Sprintf("%d", p)
        }
        return "22"
}

// probeHosts — быстрая проверка TCP-доступности; возвращает недоступные.
func probeHosts(ips []string, d time.Duration) []string {
        type res struct {
                ip string
                ok bool
        }
        out := make([]res, len(ips))
        var wg sync.WaitGroup
        sem := make(chan struct{}, 8)
        for i, ip := range ips {
                wg.Add(1)
                go func(i int, ip string) {
                        defer wg.Done()
                        sem <- struct{}{}
                        defer func() { <-sem }()
                        host, port := ip, 22
                        if h, p, ok := splitHostPort(ip); ok {
                                host, port = h, p
                        }
                        c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), d)
                        if err == nil {
                                _ = c.Close()
                        }
                        out[i] = res{ip, err == nil}
                }(i, ip)
        }
        wg.Wait()
        var bad []string
        for _, r := range out {
                if !r.ok {
                        bad = append(bad, r.ip)
                }
        }
        return bad
}

// buildSummaryText — текст сводки перед выполнением.
func (u *PlaybookUI) buildSummaryText(ips []string, tasks []PlaybookTask) string {
        var b strings.Builder
        name := strings.TrimSpace(store.State.PlaybookName)
        if name == "" {
                name = "Без названия"
        }
        b.WriteString(fmt.Sprintf("<b>%s</b>\n", escapeMarkup(name)))
        if strings.TrimSpace(store.State.PlaybookDescription) != "" {
                b.WriteString("<span foreground=\"#8b93a5\">" + escapeMarkup(store.State.PlaybookDescription) + "</span>\n")
        }
        b.WriteString("\n<b>Задачи:</b>\n")
        for i, t := range tasks {
                line := fmt.Sprintf("  %d. %s", i+1, escapeMarkup(t.StepTitle()))
                if t.Root {
                        line += " <span foreground=\"#fbbf24\">[root]</span>"
                }
                if t.AllowRetry {
                        line += " <span foreground=\"#8fa0c0\">[повтор]</span>"
                }
                b.WriteString(line + "\n")
                b.WriteString("     <span font=\"Monospace 8.5\" foreground=\"#8fa0c0\">" + escapeMarkup(truncStr(t.stepCommand(), 120)) + "</span>\n")
        }
        b.WriteString(fmt.Sprintf("\n<b>Цели:</b> %d хост(ов)\n  ", len(ips)))
        for i, ip := range ips {
                if i == 10 {
                        b.WriteString(fmt.Sprintf("… и ещё %d", len(ips)-10))
                        break
                }
                if i > 0 {
                        b.WriteString(", ")
                }
                b.WriteString(ip)
        }
        b.WriteString("\n\n<b>Пользователь:</b> " + escapeMarkup(u.currentUser()) + u.rootSuffixLine())
        b.WriteString(fmt.Sprintf("\n<b>Выполнение:</b> параллельно до %d хостов · политика ошибки: %s", store.State.effectiveParallel(), errPolicyRu(store.State.effectiveErrPolicy())))
        if store.State.PlaybookRunTimeoutSec > 0 {
                b.WriteString(fmt.Sprintf(" · общий таймаут: %s", formatDur(int64(store.State.PlaybookRunTimeoutSec)*1000)))
        }
        if refs := collectVarRefs(tasks); len(refs) > 0 {
                b.WriteString("\n<b>Переменные:</b> " + strings.Join(refs, ", "))
        }
        return b.String()
}

func (u *PlaybookUI) rootSuffixLine() string {
        n := 0
        for _, t := range store.State.Playbook {
                if t.Root {
                        n++
                }
        }
        if n > 0 {
                return fmt.Sprintf(" · задач с root (su): %d", n)
        }
        return ""
}

func truncStr(s string, n int) string {
        if len(s) > n {
                return s[:n] + "…"
        }
        return s
}

// infoDialogMarkup — информационное окно с Pango-markup.
func infoDialogMarkup(parent *gtk.Window, title, markup string) {
        if parent == nil && app != nil {
                parent = app.window
        }
        d, err := gtk.DialogNewWithButtons(title, parent, gtk.DIALOG_MODAL,
                []interface{}{"ОК", gtk.RESPONSE_OK})
        if err != nil {
                return
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(620, 420)
        box, _ := d.GetContentArea()
        lbl := labelNew("")
        lbl.SetMarkup(markup)
        lbl.SetXAlign(0)
        lbl.SetLineWrap(true)
        sc := scrolled(lbl, 0)
        sc.SetVExpand(true)
        box.PackStart(sc, true, true, 8)
        box.ShowAll()
        d.Run()
}

// markupConfirm — подтверждение с Pango-markup и своими подписями кнопок.
func markupConfirm(parent *gtk.Window, title, markup, okLabel, cancelLabel string) bool {
        if parent == nil && app != nil {
                parent = app.window
        }
        d, err := gtk.DialogNewWithButtons(title, parent, gtk.DIALOG_MODAL,
                []interface{}{okLabel, gtk.RESPONSE_ACCEPT},
                []interface{}{cancelLabel, gtk.RESPONSE_CANCEL},
        )
        if err != nil {
                return false
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(640, 520)
        box, _ := d.GetContentArea()
        lbl := labelNew("")
        lbl.SetMarkup(markup)
        lbl.SetXAlign(0)
        lbl.SetLineWrap(true)
        sc := scrolled(lbl, 0)
        sc.SetVExpand(true)
        box.PackStart(sc, true, true, 8)
        box.ShowAll()
        return d.Run() == gtk.RESPONSE_ACCEPT
}

// varsForm — диалог ввода значений переменных перед запуском.
// Значения живут только в сессии запуска (не сохраняются в конфиг).
func (u *PlaybookUI) varsForm(refs []string) (map[string]string, bool) {
        d, err := gtk.DialogNewWithButtons(
                "Значения переменных", app.window, gtk.DIALOG_MODAL,
                []interface{}{"Запустить", gtk.RESPONSE_ACCEPT},
                []interface{}{"Отмена", gtk.RESPONSE_CANCEL},
        )
        if err != nil {
                return nil, false
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(480, 130+len(refs)*40)
        box, _ := d.GetContentArea()
        box.SetMarginStart(10)
        box.SetMarginEnd(10)
        box.SetMarginTop(8)
        box.SetMarginBottom(6)

        hint := labelNew("Значения действуют для этого запуска (сохранённые — в «Переменные…»); секреты — {{ secret:имя }}.")
        addClass(hint, "ramuted")
        hint.SetLineWrap(true)
        hint.SetXAlign(0)
        box.PackStart(hint, false, false, 4)

        type vr struct {
                name string
                e    *gtk.Entry
        }
        var rows []vr
        for _, ref := range refs {
                defVal := ""
                if store.State.PlaybookVars != nil {
                        if v, ok := store.State.PlaybookVars[ref]; ok {
                                defVal = v
                        }
                }
                row := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
                lbl := labelNew("{{" + ref + "}}")
                lbl.SetWidthChars(18)
                lbl.SetXAlign(0)
                e := entryNew("значение")
                e.SetText(defVal)
                row.PackStart(lbl, false, false, 0)
                row.PackStart(e, true, true, 0)
                box.PackStart(row, false, false, 3)
                rows = append(rows, vr{ref, e})
        }
        box.ShowAll()

        if d.Run() != gtk.RESPONSE_ACCEPT {
                return nil, false
        }
        out := map[string]string{}
        for _, r := range rows {
                out[r.name] = entText(r.e)
        }
        return out, true
}

// ---------- запуск через движок v1.9.0 ----------

func (u *PlaybookUI) runPlaybook() {
        u.saveMeta()
        ips := u.targetHostsForPlaybook()
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
        tasks := make([]PlaybookTask, len(store.State.Playbook))
        copy(tasks, store.State.Playbook)

        // сколько задач потребуют повышенных прав — видно заранее, до запуска
        rootTasks := 0
        for _, t := range tasks {
                if t.Root || store.State.UseRoot {
                        rootTasks++
                }
        }
        // отдельное подтверждение массового изменения (галочка + среды)
        if !massGuard(ips, fmt.Sprintf("Плейбук: %d задач", len(tasks)), rootTasks) {
                return
        }

        // сводка перед выполнением
        if !markupConfirm(app.window, "Сводка перед выполнением", u.buildSummaryText(ips, tasks), "Запустить", "Отмена") {
                return
        }

        // форма переменных (если есть ссылки {{…}} без сохранённых значений)
        vars := map[string]string{}
        if refs := collectVarRefs(tasks); len(refs) > 0 {
                var ok bool
                vars, ok = u.varsForm(refs)
                if !ok {
                        return
                }
        }

        // опасные задачи — отдельное подтверждение
        for _, t := range tasks {
                if t.isDangerous() {
                        if !confirmDanger(app.window, t.Describe(), len(ips)) {
                                return
                        }
                }
        }

        label := strings.TrimSpace(store.State.PlaybookName)
        if label == "" {
                label = "Playbook"
        }
        s := buildRunSession(ips, tasks, label, vars)
        logf("accent", "🚀 «%s»: %d задач на %d хост(ов), параллельно до %d", label, len(tasks), len(ips), s.Parallel)
        go s.runSession()
}

// saveAsTemplate сохраняет текущий плейбук как шаблон. Возвращает имя
// шаблона ("" — сохранение не состоялось).
func (u *PlaybookUI) saveAsTemplate() (string, bool) {
        if len(store.State.Playbook) == 0 {
                warnDialog(app.window, "Список задач пуст")
                return "", false
        }
        name, ok := entryDialog(app.window, "Новый шаблон", "Имя шаблона", "")
        if !ok || strings.TrimSpace(name) == "" {
                return "", false
        }
        name = strings.TrimSpace(name)
        desc, _ := entryDialog(app.window, "Описание", "Краткое описание (опц.)", "")
        if store.TemplateByName(name) != nil {
                if !confirmDialog(app.window, "Шаблон «"+name+"» уже существует. Перезаписать?") {
                        return "", false
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
        return name, true
}

// schedulePlaybook — кнопка «⏰ Запланировать» рядом с запуском: превращает
// текущий плейбук в шаблон и открывает диалог расписания с выбранным
// шаблоном. Не нужно переходить на вкладку «Планировщик» вручную.
func (u *PlaybookUI) schedulePlaybook() {
        if len(store.State.Playbook) == 0 {
                warnDialog(app.window, "Список задач пуст — нечего планировать")
                return
        }
        tplName := ""
        // если плейбук совпадает с существующим шаблоном — используем его
        for _, t := range store.State.Templates {
                if tasksEqual(t.Tasks, store.State.Playbook) {
                        tplName = t.Name
                        break
                }
        }
        if tplName == "" {
                if !confirmDialog(app.window, "Расписание запускает ШАБЛОН задач.\nСохранить текущий плейбук как шаблон и продолжить?") {
                        return
                }
                name, ok := u.saveAsTemplate()
                if !ok {
                        return
                }
                tplName = name
        }
        if app.schedUI != nil {
                app.schedUI.editDialogPrefill(nil, tplName)
        }
}

// tasksEqual — поверхностное сравнение двух списков задач.
func tasksEqual(a, b []PlaybookTask) bool {
        if len(a) != len(b) {
                return false
        }
        for i := range a {
                if a[i].Type != b[i].Type || a[i].Source != b[i].Source || a[i].Action != b[i].Action ||
                        a[i].Dest != b[i].Dest || a[i].Search != b[i].Search || a[i].Replace != b[i].Replace {
                        return false
                }
        }
        return true
}

// runPlaybookOnHosts executes a task list sequentially on each host (parallel
// across hosts). Учитывает условие выполнения (RunIf) и политику ошибок.
func runPlaybookOnHosts(ips []string, tasks []PlaybookTask, label string) {
        continueOnError := store.State.PlaybookContinueOnError
        runParallel(ips, label, func(ip string) (string, error) {
                creds := buildCreds(ip)
                cl, err := Connect(creds)
                if err != nil {
                        return "", err
                }
                defer cl.Close()
                var logAll strings.Builder
                prevErr := false
                var firstErr error
                for i, raw := range tasks {
                        // подстановка переменных {{ var }} с учётом хоста
                        t := store.expandTaskVars(ip, raw)
                        // условие выполнения задачи
                        if t.RunIf == "on_success" && prevErr {
                                logAll.WriteString(fmt.Sprintf("[%d/%d] %s — ПРОПУЩЕНО (нужна успешная предыдущая)\n", i+1, len(tasks), t.Describe()))
                                continue
                        }
                        if t.RunIf == "on_failure" && !prevErr {
                                logAll.WriteString(fmt.Sprintf("[%d/%d] %s — ПРОПУЩЕНО (нужна ошибочная предыдущая)\n", i+1, len(tasks), t.Describe()))
                                continue
                        }
                        out, err := execTask(cl, ip, t, creds)
                        logAll.WriteString(fmt.Sprintf("[%d/%d] %s\n%s\n", i+1, len(tasks), t.Describe(), strings.TrimSpace(out)))
                        if err != nil {
                                firstErr = fmt.Errorf("задача %d (%s): %v", i+1, t.Describe(), err)
                                prevErr = true
                                logAll.WriteString(fmt.Sprintf("ОШИБКА: %v\n", err))
                                if !continueOnError {
                                        return logAll.String(), firstErr
                                }
                                logAll.WriteString("(продолжено: включён режим «продолжать при ошибке»)\n")
                                continue
                        }
                        prevErr = false
                }
                return logAll.String(), firstErr
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
                // скрипт читаем локально, подставляем переменные {{ var }} для хоста,
                // грузим во временный файл и выполняем
                content, err := os.ReadFile(t.Source)
                if err != nil {
                        return "", fmt.Errorf("скрипт недоступен: %v", err)
                }
                expanded := store.expandVars(ip, string(content))
                tmpLocal := filepath.Join(os.TempDir(), fmt.Sprintf(".ra_scr_%d_%d.sh", os.Getpid(), time.Now().UnixNano()%100000))
                if err := os.WriteFile(tmpLocal, []byte(expanded), 0755); err != nil {
                        return "", err
                }
                defer os.Remove(tmpLocal)
                tmp := fmt.Sprintf("/tmp/.ra_pb_%d.sh", nowMs())
                if err := uploadThenSuRoot(cl, tmpLocal, tmp, rootPassFor(creds, root), timeout); err != nil {
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
        case "service":
                if strings.TrimSpace(t.Source) == "" {
                        return "", fmt.Errorf("укажите имя службы")
                }
                act := t.Action
                if act == "" {
                        act = "restart"
                }
                out, err := run("systemctl " + act + " " + shellQuote(t.Source))
                if err != nil {
                        return out, fmt.Errorf("systemctl %s %s: %v", act, t.Source, err)
                }
                if strings.TrimSpace(out) == "" {
                        out = fmt.Sprintf("служба %s: %s — выполнено", t.Source, serviceActionRu(act))
                }
                return out, nil
        case "package":
                if strings.TrimSpace(t.Source) == "" {
                        return "", fmt.Errorf("укажите пакеты")
                }
                act := t.Action
                if act == "" {
                        act = "install"
                }
                pkgs := strings.Fields(strings.ReplaceAll(t.Source, ",", " "))
                quoted := make([]string, len(pkgs))
                for i, p := range pkgs {
                        quoted[i] = shellQuote(p)
                }
                pkgList := strings.Join(quoted, " ")
                // определяем менеджер пакетов: dnf (РЕД ОС) → yum → apt-get
                mgrOut, _ := run("(command -v dnf >/dev/null 2>&1 && echo dnf) || (command -v yum >/dev/null 2>&1 && echo yum) || (command -v apt-get >/dev/null 2>&1 && echo apt-get) || echo dnf")
                mgr := strings.TrimSpace(mgrOut)
                if mgr == "" {
                        mgr = "dnf"
                }
                var cmd string
                switch {
                case mgr == "apt-get" && act == "remove":
                        cmd = "DEBIAN_FRONTEND=noninteractive apt-get -y remove " + pkgList
                case mgr == "apt-get" && act == "upgrade":
                        cmd = "DEBIAN_FRONTEND=noninteractive apt-get -y upgrade"
                case mgr == "apt-get" && act == "info":
                        cmd = "apt-cache show " + pkgList + " 2>/dev/null | head -20"
                case mgr == "apt-get":
                        cmd = "DEBIAN_FRONTEND=noninteractive apt-get -y install " + pkgList
                case act == "remove":
                        cmd = mgr + " -y remove " + pkgList
                case act == "upgrade":
                        cmd = mgr + " -y upgrade"
                case act == "info":
                        cmd = mgr + " info " + pkgList
                default:
                        cmd = mgr + " -y install " + pkgList
                }
                out, err := run(cmd)
                if err != nil {
                        return out, fmt.Errorf("%s %s %s: %v", mgr, act, t.Source, err)
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
