package main

import (
        "fmt"
        "os"
        "path/filepath"
        "sort"
        "strings"

        "github.com/gotk3/gotk3/gdk"
        "github.com/gotk3/gotk3/glib"
        "github.com/gotk3/gotk3/gtk"
)

// HostsUI holds the left panel: groups + host list + quick actions.
type HostsUI struct {
        widget     gtk.IWidget
        search     *gtk.SearchEntry
        favOnlyC   *gtk.CheckButton
        favOnly    bool
        hostTree   *gtk.TreeView
        hostStore  *gtk.ListStore
        groupTree  *gtk.TreeView
        groupStore *gtk.ListStore
        filter     string
        addEntry   *gtk.Entry
        lastGroup  string // последняя выбранная группа (для восстановления сессии)
}

// host store columns
const (
        HCOL_STATUS = 0 // emoji badge
        HCOL_DISP   = 1 // «Хост»: имя (жирным) + IP — markup
        HCOL_IP     = 2 // реальный адрес (скрытая колонка)
        HCOL_GROUP  = 3 // группа (видимая)
        HCOL_COLOR  = 4 // цвет статуса (скрытая)
        HCOL_TAGS   = 5 // метки («#prod #web») — видимая
        HCOL_FAV    = 6 // ★ избранное — видимая
)

// group store columns
const (
        GCOL_NAME  = 0 // Pango-markup: цветная точка + имя
        GCOL_COUNT = 1
        GCOL_RAW   = 2 // скрытая колонка с «чистым» именем группы
)

// groupPalette — цвета, которыми подсвечиваются группы (детерминированно
// по имени: одна и та же группа всегда одного цвета в любом списке).
var groupPalette = []string{
        "#3b82f6", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6", "#14b8a6",
        "#f472b6", "#0ea5e9", "#84cc16", "#f97316", "#6366f1", "#d946ef",
}

// groupColor возвращает стабильный цвет группы по её имени.
func groupColor(name string) string {
        if name == "Общие" {
                return "#94a3b8"
        }
        sum := 0
        for _, r := range name {
                sum = (sum*31 + int(r)) & 0x7fffffff
        }
        return groupPalette[sum%len(groupPalette)]
}

func newHostsUI() *HostsUI {
        u := &HostsUI{}
        root := boxNew(gtk.ORIENTATION_HORIZONTAL, 8)

        // ===== left: groups =====
        leftCard, leftBox := cardBox("Группы")
        leftBox.SetSizeRequest(180, -1)

        u.groupTree, u.groupStore = makeTreeM([]string{"Группа", "Всего", ""},
                []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING}, map[int]bool{0: true})
        hideCol(u.groupTree, GCOL_RAW)
        sel, _ := u.groupTree.GetSelection()
        sel.SetMode(gtk.SELECTION_SINGLE)
        sel.Connect("changed", func() { u.refreshHostList() })
        sg, _ := gtk.ScrolledWindowNew(nil, nil)
        sg.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
        sg.Add(u.groupTree)
        sg.SetVExpand(true)
        leftBox.PackStart(sg, true, true, 0)

        gbtns := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnAddGroup := buttonNew("Новая группа")
        btnAddGroup.Connect("clicked", func() {
                name, ok := entryDialog(app.window, "Новая группа", "Имя группы", "")
                if !ok {
                        return
                }
                name = strings.TrimSpace(name)
                if name == "" {
                        return
                }
                if err := store.AddGroup(name); err != nil {
                        warnDialog(app.window, err.Error())
                        return
                }
                _ = store.Save()
                u.refreshAll()
                // select the fresh group so hosts added next land in it
                u.selectGroup(name)
                logf("ok", "➕ Группа «%s» создана (перетащите хосты или добавьте их при выбранной группе)", name)
        })
        btnRenGroup := buttonNew("✎")
        btnRenGroup.SetTooltipText("Переименовать выбранную группу")
        btnRenGroup.Connect("clicked", func() { u.renameSelectedGroup() })
        btnDelGroup := buttonNew("🗑")
        btnDelGroup.SetTooltipText("Удалить группу (хосты → Общие)")
        btnDelGroup.Connect("clicked", func() {
                g, ok := u.selectedGroup()
                if !ok {
                        return
                }
                if g == "Общие" {
                        warnDialog(app.window, "Системную группу «Общие» удалить нельзя")
                        return
                }
                if !confirmDialog(app.window, "Удалить группу «"+g+"»? Хосты будут перемещены в «Общие».") {
                        return
                }
                for i := range store.State.Hosts {
                        if store.State.Hosts[i].Group == g {
                                store.State.Hosts[i].Group = "Общие"
                        }
                }
                store.RemoveGroup(g)
                _ = store.Save()
                u.refreshAll()
                logf("ok", "🗑 Группа «%s» удалена (хосты перемещены в «Общие»)", g)
        })
        gbtns.PackStart(btnAddGroup, true, true, 0)
        gbtns.PackStart(btnRenGroup, false, false, 0)
        gbtns.PackStart(btnDelGroup, false, false, 0)
        leftBox.PackStart(gbtns, false, false, 0)

        hint := labelNew("↷ Перетащите хост на группу")
        scH, _ := hint.GetStyleContext()
        scH.AddClass("ramuted")
        hint.SetXAlign(0)
        hint.SetLineWrap(true)
        hint.SetSizeRequest(160, -1)
        leftBox.PackStart(hint, false, false, 2)

        // DnD: groups accept host IPs.
        // NB: COPY only — with ACTION_MOVE GtkTreeView's default drag-data-delete
        // handler removes rows from the source ListStore AFTER our handler has
        // cleared and refilled it (stale path), which corrupts the C model and
        // crashes the app (GC-timing dependent, see user report).
        te, _ := gtk.TargetEntryNew("text/plain", gtk.TARGET_SAME_APP, 0)
        teVal := *te
        u.groupTree.DragDestSet(gtk.DEST_DEFAULT_HIGHLIGHT|gtk.DEST_DEFAULT_MOTION, []gtk.TargetEntry{teVal}, gdk.ACTION_COPY)
        // The row highlighted during motion (what the user SEES) is what the
        // drop uses. Resolving the row again from the drop-event coordinates
        // proved unreliable, so drag-motion records the path here.
        var dragDestPath *gtk.TreePath
        u.groupTree.Connect("drag-motion", func(_ *gtk.TreeView, ctx *gdk.DragContext, x, y int, tm uint32) bool {
                // Returning true with DEST_DEFAULT_MOTION makes GTK call
                // gdk_drag_status(COPY) (the actions from DragDestSet) —
                // without this the drop is rejected.
                path := treeViewDestRowAtPos(u.groupTree, x, y)
                if os.Getenv("RA_DEBUG") != "" {
                        s := "none"
                        if path != nil {
                                s = path.String()
                        }
                        fmt.Printf("[dnd] motion (%d,%d) dest-path=%s\n", x, y, s)
                }
                if path != nil {
                        u.groupTree.SetDragDestRow(path, gtk.TREE_VIEW_DROP_INTO_OR_AFTER)
                        dragDestPath = path
                        return true
                }
                u.groupTree.SetDragDestRow(nil, gtk.TREE_VIEW_DROP_INTO_OR_AFTER)
                dragDestPath = nil
                return false
        })
        u.groupTree.Connect("drag-drop", func(tv *gtk.TreeView, ctx *gdk.DragContext, x, y int, tm uint32) bool {
                dbg(fmt.Sprintf("[dnd] drag-drop (%d,%d)", x, y))
                // gotk3 does not wrap gtk_drag_get_data and its automatic
                // DEST_DEFAULT_DROP path never delivers data in practice, so
                // request the selection explicitly.
                dragGetData(tv, ctx, gdk.GdkAtomIntern("text/plain", false), tm)
                return true
        })
        u.groupTree.Connect("drag-data-received", func(_ *gtk.TreeView, ctx *gdk.DragContext, x, y int, data *gtk.SelectionData, info uint, tm uint32) {
                dbg(fmt.Sprintf("[dnd] drag-data-received (%d,%d)", x, y))
                txt := data.GetText()
                dbg(fmt.Sprintf("[dnd]   text=%q", txt))
                if txt == "" {
                        return
                }
                dragFinish(ctx, true, false, tm)
                path := dragDestPath
                // Extract everything we need and defer the model mutation:
                // no ListStore changes are allowed while the DnD machinery
                // is still walking the trees.
                glibIdle(func() { u.applyHostMove(txt, path, x, y) })
        })

        // ===== middle: hosts =====
        midCard, midBox := cardBox("Хосты")

        searchRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
        u.search, _ = gtk.SearchEntryNew()
        u.search.SetPlaceholderText("Поиск хостов и групп (Ctrl+F)…")
        u.search.Connect("changed", func() {
                u.filter = strings.ToLower(entText(&u.search.Entry))
                u.refreshHostList()
        })
        searchRow.PackStart(u.search, true, true, 0)
        u.favOnlyC, _ = gtk.CheckButtonNewWithLabel("★")
        u.favOnlyC.SetTooltipText("Показывать только избранные хосты")
        u.favOnlyC.Connect("toggled", func() {
                u.favOnly = u.favOnlyC.GetActive()
                u.refreshHostList()
        })
        searchRow.PackStart(u.favOnlyC, false, false, 0)
        midBox.PackStart(searchRow, false, false, 0)

        u.hostTree, u.hostStore = makeHostTree()
        hideCol(u.hostTree, HCOL_IP)
        hideCol(u.hostTree, HCOL_COLOR)
        selH, _ := u.hostTree.GetSelection()
        selH.SetMode(gtk.SELECTION_MULTIPLE)
        // выбор хостов мгновенно отражается в строке цели плейбука
        selH.Connect("changed", func() {
                glibIdle(func() {
                        if app != nil && app.pbUI != nil {
                                app.pbUI.refreshTargetLine()
                        }
                })
        })

        // двойной клик — редактирование имени/адреса/группы
        u.hostTree.Connect("row-activated", func(_ *gtk.TreeView, path *gtk.TreePath, _ *gtk.TreeViewColumn) {
                if m, ok := treeModelOf(u.hostTree); ok {
                        if iter, err := m.GetIter(path); err == nil {
                                u.editHostDialog(iterGetString(m, iter, HCOL_IP))
                        }
                }
        })

        // Delete key removes selected hosts
        u.hostTree.Connect("key-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
                ke := gdk.EventKeyNewFromEvent(ev)
                if ke.KeyVal() == gdk.KEY_Delete {
                        u.deleteSelected()
                        return true
                }
                return false
        })

        // DnD source: selected IPs (COPY only, see comment above).
        // NB: GtkTreeView applies the pressed row's selection on RELEASE, so a
        // drag started immediately after a press carries an empty selection —
        // fall back to the row under the press point.
        var dragPressX, dragPressY = -1, -1
        u.hostTree.Connect("button-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
                be := gdk.EventButtonNewFromEvent(ev)
                dragPressX, dragPressY = int(be.X()), int(be.Y())
                return false
        })
        u.hostTree.DragSourceSet(gdk.BUTTON1_MASK, []gtk.TargetEntry{teVal}, gdk.ACTION_COPY)
        u.hostTree.Connect("drag-begin", func(_ *gtk.TreeView, ctx *gdk.DragContext) {
                dbg("[dnd] drag-begin")
        })
        u.hostTree.Connect("drag-data-get", func(_ *gtk.TreeView, ctx *gdk.DragContext, data *gtk.SelectionData, info uint, tm uint32) {
                dbg("[dnd] drag-data-get (source)")
                ips := u.selectedHosts()
                dbg(fmt.Sprintf("[dnd]   selected=%v press=(%d,%d)", ips, dragPressX, dragPressY))
                if len(ips) == 0 && dragPressX >= 0 {
                        path, _, _, _, ok := u.hostTree.GetPathAtPos(dragPressX, dragPressY)
                        dbg(fmt.Sprintf("[dnd]   fallback GetPathAtPos(%d,%d) ok=%v path=%v", dragPressX, dragPressY, ok, path))
                        if ok && path != nil {
                                if m, ok2 := treeModelOf(u.hostTree); ok2 {
                                        iter, err := m.GetIter(path)
                                        dbg(fmt.Sprintf("[dnd]   GetIter err=%v", err))
                                        if err == nil {
                                                ips = append(ips, iterGetString(m, iter, HCOL_IP))
                                        }
                                } else {
                                        dbg("[dnd]   treeModelOf failed")
                                }
                        }
                }
                if len(ips) == 0 {
                        // NB: gotk3's SelectionData.SetText panics on an empty
                        // string (&text[0]); an empty drag (press on free space
                        // below the rows) must simply carry no data.
                        return
                }
                data.SetText(strings.Join(ips, "\n"))
        })

        sh, _ := gtk.ScrolledWindowNew(nil, nil)
        sh.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
        sh.Add(u.hostTree)
        sh.SetVExpand(true)
        midBox.PackStart(sh, true, true, 0)

        addRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        u.addEntry, _ = gtk.EntryNew()
        u.addEntry.SetPlaceholderText("IP или список через запятую/пробел")
        addRow.PackStart(u.addEntry, true, true, 0)
        btnAdd := buttonNew("Добавить")
        btnAdd.GetStyleContext()
        btnAdd.Connect("clicked", func() { u.addFromEntry() })
        u.addEntry.Connect("activate", func() { u.addFromEntry() })
        btnEdit := buttonNew("✎ Изменить")
        btnEdit.SetTooltipText("Изменить имя, адрес и группу выбранного хоста (или двойной клик по строке)")
        btnEdit.Connect("clicked", func() {
                ips := u.selectedHosts()
                if len(ips) == 0 {
                        warnDialog(app.window, "Выберите хост в списке")
                        return
                }
                u.editHostDialog(ips[0])
        })
        btnDel := buttonNew("Удалить")
        btnDel.Connect("clicked", func() { u.deleteSelected() })
        addRow.PackStart(btnEdit, false, false, 0)
        addRow.PackStart(btnAdd, false, false, 0)
        addRow.PackStart(btnDel, false, false, 0)
        midBox.PackStart(addRow, false, false, 0)

        // ===== right: quick actions =====
        rightCard, rightBox := cardBox("Действия")

        credGrid := mkGrid()
        userE, _ := gtk.EntryNew()
        userE.SetPlaceholderText("Пользователь SSH")
        passE, _ := gtk.EntryNew()
        passE.SetPlaceholderText("Пароль")
        passE.SetVisibility(false)
        rootE, _ := gtk.EntryNew()
        rootE.SetPlaceholderText("Пароль root (su)")
        rootE.SetVisibility(false)
        useRootC, _ := gtk.CheckButtonNewWithLabel("Повышать до root (su)")
        useRootC.SetActive(store.State.UseRoot)
        keyE, _ := gtk.EntryNew()
        keyE.SetPlaceholderText("Путь к SSH-ключу (опц.)")
        keyPassE, _ := gtk.EntryNew()
        keyPassE.SetPlaceholderText("Пароль ключа (опц.)")
        keyPassE.SetVisibility(false)

        credGrid.attach(labelNew("Логин:"), 0, 0, 1, 1)
        credGrid.attach(userE, 1, 0, 1, 1)
        credGrid.attach(labelNew("Пароль:"), 0, 1, 1, 1)
        credGrid.attach(passE, 1, 1, 1, 1)
        credGrid.attach(useRootC, 1, 2, 1, 1)
        credGrid.attach(labelNew("Root:"), 0, 3, 1, 1)
        credGrid.attach(rootE, 1, 3, 1, 1)
        credGrid.attach(labelNew("Ключ:"), 0, 4, 1, 1)
        credGrid.attach(keyE, 1, 4, 1, 1)
        credGrid.attach(keyPassE, 1, 5, 1, 1)

        userE.SetText(store.State.Username)
        keyE.SetText(store.State.KeyPath)

        rightBox.PackStart(credGrid.w, false, false, 0)

        saveCred := buttonNew("Сохранить доступы")
        saveCred.Connect("clicked", func() {
                store.State.Username = entText(userE)
                store.SetPassword(entText(passE))
                store.SetRootPassword(entText(rootE))
                store.State.UseRoot = useRootC.GetActive()
                store.State.KeyPath = entText(keyE)
                store.SetKeyPassphrase(entText(keyPassE))
                _ = store.Save()
                logf("ok", "💾 Доступы сохранены (пароли зашифрованы AES-GCM)")
        })
        rightBox.PackStart(saveCred, false, false, 0)

        sep1, _ := gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL)
        rightBox.PackStart(sep1, false, false, 2)

        qrow1 := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnPing := buttonNew("Ping")
        btnPing.Connect("clicked", func() { runPingAction(u.selectedHosts()) })
        btnInfo := buttonNew("Инфо о системе")
        btnInfo.Connect("clicked", func() { runSysInfoAction(u.selectedHosts()) })
        qrow1.PackStart(btnPing, true, true, 0)
        qrow1.PackStart(btnInfo, true, true, 0)
        rightBox.PackStart(qrow1, false, false, 0)

        cmdE, _ := gtk.EntryNew()
        cmdE.SetPlaceholderText("Быстрая команда… (↑↓ — история)")
        rightBox.PackStart(cmdE, false, false, 0)
        hist := newHistory()
        cmdE.Connect("key-press-event", func(_ *gtk.Entry, ev *gdk.Event) bool {
                ke := gdk.EventKeyNewFromEvent(ev)
                switch ke.KeyVal() {
                case gdk.KEY_Return, gdk.KEY_KP_Enter:
                        cmd := entText(cmdE)
                        if strings.TrimSpace(cmd) == "" {
                                return true
                        }
                        ips := u.selectedHosts()
                        if len(ips) == 0 {
                                warnDialog(app.window, "Отметьте хосты в списке слева — на них уйдёт команда")
                                return true
                        }
                        // массовое изменение — отдельное подтверждение
                        if !massGuard(ips, "Быстрая команда: "+cmd, 0) {
                                return true
                        }
                        hist.push(cmd)
                        go runQuickCommand(ips, cmd)
                        cmdE.SetText("")
                        return true
                case gdk.KEY_Up:
                        cmdE.SetText(hist.up())
                        return true
                case gdk.KEY_Down:
                        cmdE.SetText(hist.down())
                        return true
                }
                return false
        })

        qrow2 := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnScript := buttonNew("Скрипт…")
        addClass(btnScript, "suggested")
        btnScript.SetTooltipText("Выбрать скрипт, посмотреть его и подтвердить запуск на выбранных хостах")
        btnScript.Connect("clicked", func() { u.scriptRunDialog() })
        btnRepl := buttonNew("Заменить файл…")
        btnRepl.SetTooltipText("Выбрать локальный файл, указать удалённый путь и подтвердить замену на выбранных хостах")
        btnRepl.Connect("clicked", func() { u.replaceRunDialog() })
        qrow2.PackStart(btnScript, true, true, 0)
        qrow2.PackStart(btnRepl, true, true, 0)
        rightBox.PackStart(qrow2, false, false, 0)

        sep2, _ := gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL)
        rightBox.PackStart(sep2, false, false, 2)

        irow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
        btnImp := buttonNew("Импорт файла")
        btnImp.SetTooltipText("Импорт хостов из файла: Ansible inventory (INI/YAML) или простой список по одному в строке")
        btnImp.Connect("clicked", func() { importAnsibleDialog() })
        btnRem := buttonNew("Remmina")
        btnRem.SetTooltipText("Импорт подключений Remmina из ~/.config/remmina (имя, адрес, группа)")
        btnRem.Connect("clicked", func() { importRemminaDialog() })
        btnPaste := buttonNew("Вставить список")
        btnPaste.SetTooltipText("Добавить хосты, вставив список адресов текстом")
        btnPaste.Connect("clicked", func() { importPasteDialog() })
        btnExp := buttonNew("Экспорт")
        btnExp.SetTooltipText("Экспорт в Ansible inventory (INI/YAML)")
        btnExp.Connect("clicked", func() { exportAnsibleDialog() })
        irow.PackStart(btnImp, true, true, 0)
        irow.PackStart(btnRem, true, true, 0)
        irow.PackStart(btnPaste, true, true, 0)
        irow.PackStart(btnExp, true, true, 0)
        rightBox.PackStart(irow, false, false, 0)

        // панели регулируются мышью: границы можно тянуть влево-вправо,
        // положение запоминается между сеансами
        root.PackStart(makeHPaned("hosts-groups",
                leftCard,
                makeHPaned("hosts-actions", midCard, rightCard, 480),
                190), true, true, 0)

        u.refreshAll()
        u.widget = root
        return u
}

// ---------- grid helper ----------

type simpleGrid struct {
        w *gtk.Grid
}

func mkGrid() *simpleGrid {
        g, _ := gtk.GridNew()
        g.SetRowSpacing(3)
        g.SetColumnSpacing(6)
        return &simpleGrid{w: g}
}

func (g *simpleGrid) attach(w gtk.IWidget, x, y, wd, h int) {
        g.w.Attach(w, x, y, wd, h)
}

// ---------- tree helper ----------

func makeTree(titles []string, types []glib.Type) (*gtk.TreeView, *gtk.ListStore) {
        return makeTreeM(titles, types, nil)
}

// makeTreeM — makeTree с поддержкой Pango-markup в указанных колонках
// (например, чтобы имя хоста было жирным, а IP — приглушённым).
func makeTreeM(titles []string, types []glib.Type, markupCols map[int]bool) (*gtk.TreeView, *gtk.ListStore) {
        tv, err := gtk.TreeViewNew()
        if err != nil {
                return nil, nil
        }
        store, err := gtk.ListStoreNew(types...)
        if err != nil {
                return nil, nil
        }
        tv.SetModel(store)
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
                if i == HCOL_DISP || i == GCOL_NAME {
                        col.SetExpand(true)
                }
                // NB: foreground для скрытой цветовой колонки хостов привязывается
                // ТОЛЬКО в makeHostTree; здесь нельзя — у SFTP-деревьев пустые
                // заголовки тоже на индексе 4, и GTK читал имена файлов как цвета.
                tv.AppendColumn(col)
        }
        return tv, store
}

// makeHostTree — дерево списка хостов: markup-имя + скрытые колонки
// адреса и цвета статуса (foreground привязан только здесь).
func makeHostTree() (*gtk.TreeView, *gtk.ListStore) {
        tv, err := gtk.TreeViewNew()
        if err != nil {
                return nil, nil
        }
        st, err := gtk.ListStoreNew(glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING,
                glib.TYPE_STRING, glib.TYPE_STRING)
        if err != nil {
                return nil, nil
        }
        tv.SetModel(st)
        tv.SetHeadersVisible(true)
        titles := []string{"", "Хост", "", "Группа", "", "Метки", ""}
        for i, t := range titles {
                col, _ := gtk.TreeViewColumnNew()
                col.SetTitle(t)
                cell, _ := gtk.CellRendererTextNew()
                col.PackStart(cell, true)
                switch {
                case i == HCOL_DISP:
                        col.AddAttribute(cell, "markup", i)
                        col.SetExpand(true)
                case i == HCOL_GROUP:
                        col.AddAttribute(cell, "markup", i)
                case i == HCOL_COLOR:
                        col.AddAttribute(cell, "foreground", i)
                default:
                        col.AddAttribute(cell, "text", i)
                }
                tv.AppendColumn(col)
        }
        return tv, st
}

func hideCol(tv *gtk.TreeView, idx int) {
        n := 0
        for c := tv.GetColumns(); c != nil; c = c.Next() {
                if n == idx {
                        if col, ok := c.Data().(*gtk.TreeViewColumn); ok && col != nil {
                                col.SetVisible(false)
                        }
                        return
                }
                n++
        }
}

// ---------- data refresh ----------

func (u *HostsUI) refreshAll() {
        u.refreshGroups()
        u.refreshHostList()
}

func (u *HostsUI) refreshGroups() {
        if u.groupStore == nil {
                return
        }
        u.groupStore.Clear()
        counts := store.GroupCounts()
        groups := store.Groups()
        // ensure default group visible
        hasDef := false
        for _, g := range groups {
                if g == "Общие" {
                        hasDef = true
                }
        }
        if !hasDef {
                groups = append([]string{"Общие"}, groups...)
        }
        sort.Strings(groups[1:])
        for _, g := range groups {
                iter := u.groupStore.Append()
                mk := fmt.Sprintf("<span foreground=\"%s\">●</span>  %s",
                        groupColor(g), xmlEscapeStr(g))
                _ = u.groupStore.Set(iter, []int{GCOL_NAME, GCOL_COUNT, GCOL_RAW},
                        []interface{}{mk, fmt.Sprintf("%d", counts[g]), g})
        }
}

func (u *HostsUI) refreshHostList() {
        if u.hostStore == nil {
                return
        }
        u.hostStore.Clear()
        selGroup, hasGroup := u.selectedGroup()
        hosts := make([]HostEntry, len(store.State.Hosts))
        copy(hosts, store.State.Hosts)
        sort.Slice(hosts, func(i, j int) bool {
                if hosts[i].Group != hosts[j].Group {
                        return hosts[i].Group < hosts[j].Group
                }
                return hosts[i].DisplayName() < hosts[j].DisplayName()
        })
        for _, h := range hosts {
                if hasGroup && selGroup != "Все хосты" && h.Group != selGroup {
                        continue
                }
                if u.favOnly && !h.Fav {
                        continue
                }
                if u.filter != "" && !hostMatchesFilter(h, u.filter) {
                        continue
                }
                emoji, color := statusView(h.Status)
                disp := xmlEscapeStr(h.DisplayName())
                if strings.TrimSpace(h.Name) != "" {
                        disp = "<b>" + disp + "</b>  <span size=\"smaller\" foreground=\"#8b93a5\">" + xmlEscapeStr(h.IP) + "</span>"
                }
                if h.Fav {
                        disp = "★ " + disp
                }
                // среда рядом с хостом: цветной бейдж «Тест»/«Продакшен»
                disp += envBadge(h.Env)
                tags := ""
                for i, t := range h.Tags {
                        if i > 0 {
                                tags += " "
                        }
                        tags += "#" + t
                }
                // группа подсвечивается тем же цветом, что и в списке групп слева
                groupMk := fmt.Sprintf("<span foreground=\"%s\" size=\"smaller\">● %s</span>",
                        groupColor(h.Group), xmlEscapeStr(h.Group))
                iter := u.hostStore.Append()
                _ = u.hostStore.Set(iter, []int{HCOL_STATUS, HCOL_DISP, HCOL_IP, HCOL_GROUP, HCOL_COLOR, HCOL_TAGS, HCOL_FAV},
                        []interface{}{emoji, disp, h.IP, groupMk, color, tags, map[bool]string{true: "★", false: ""}[h.Fav]})
        }
}

func hostMatchesFilter(h HostEntry, filter string) bool {
        for _, s := range []string{h.IP, h.Name, h.Group, h.Env} {
                if strings.Contains(strings.ToLower(s), filter) {
                        return true
                }
        }
        // метки: «prod» или «#prod» — подстрока без решётки ищется в каждой метке
        for _, t := range h.Tags {
                if strings.Contains(strings.ToLower(t), strings.TrimPrefix(filter, "#")) {
                        return true
                }
        }
        return false
}

func xmlEscapeStr(s string) string {
        r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
        return r.Replace(s)
}

func statusView(status string) (string, string) {
        switch status {
        case "ok":
                return "●", "#34d399"
        case "fail":
                return "●", "#f87171"
        case "warn":
                return "●", "#fbbf24"
        case "run":
                return "◐", "#60a5fa"
        }
        return "○", "#667089"
}

func (u *HostsUI) selectedGroup() (string, bool) {
        if u.groupTree == nil {
                return "", false
        }
        sel, err := u.groupTree.GetSelection()
        if err != nil {
                return "", false
        }
        mI, iter, ok := sel.GetSelected()
        if !ok {
                u.lastGroup = ""
                return "Все хосты", true // default: all hosts
        }
        m, ok2 := anyTreeModel(mI)
        if !ok2 {
                return "", false
        }
        g := iterGetString(m, iter, GCOL_RAW)
        if g == "" { // совместимость со старыми сохранениями
                g = iterGetString(m, iter, GCOL_NAME)
        }
        u.lastGroup = g
        return g, true
}

// selectGroup programmatically selects a group row by name ("Все хосты"
// clears the selection).
func (u *HostsUI) selectGroup(name string) {
        if u.groupTree == nil {
                return
        }
        m, ok := treeModelOf(u.groupTree)
        if !ok {
                return
        }
        sel, err := u.groupTree.GetSelection()
        if err != nil {
                return
        }
        if name == "" || name == "Все хосты" {
                sel.UnselectAll()
                return
        }
        iter, ok2 := m.GetIterFirst()
        for ok2 {
                if iterGetString(m, iter, GCOL_NAME) == name {
                        sel.SelectIter(iter)
                        return
                }
                ok2 = m.IterNext(iter)
        }
}

func (u *HostsUI) groupAtPos(x, y int) (string, bool) {
        path, _, _, _, ok2 := u.groupTree.GetPathAtPos(x, y)
        if !ok2 || path == nil {
                return "", false
        }
        m, ok := treeModelOf(u.groupTree)
        if !ok {
                return "", false
        }
        iter, err := m.GetIter(path)
        if err != nil {
                return "", false
        }
        s := iterGetString(m, iter, GCOL_RAW)
        if s == "" {
                s = iterGetString(m, iter, GCOL_NAME) // совместимость со старыми сохранениями
        }
        if s == "" || s == "Все хосты" {
                return "", false
        }
        return s, true
}

// dbg prints debug lines to stdout when RA_DEBUG is set (autotest support).
func dbg(msg string) {
        if os.Getenv("RA_DEBUG") != "" {
                fmt.Println(msg)
        }
}

// groupFromPath resolves a group name for an already-computed tree path.
func (u *HostsUI) groupFromPath(path *gtk.TreePath) (string, bool) {
        if path == nil {
                return "", false
        }
        m, ok := treeModelOf(u.groupTree)
        if !ok {
                return "", false
        }
        iter, err := m.GetIter(path)
        if err != nil {
                return "", false
        }
        s := iterGetString(m, iter, GCOL_RAW)
        if s == "" {
                s = iterGetString(m, iter, GCOL_NAME) // совместимость со старыми сохранениями
        }
        if s == "" || s == "Все хосты" {
                return "", false
        }
        return s, true
}

// requireSelection проверяет, что хосты выделены, и подсказывает, как это сделать.
func (u *HostsUI) requireSelection() ([]string, bool) {
        ips := u.selectedHosts()
        if len(ips) == 0 {
                warnDialog(app.window, "Сначала отметьте хосты в списке (Ctrl/Shift + клик — несколько строк).\n\n"+
                        "Так безопаснее: действие не попадёт на машины, которые вы не планировали затронуть.")
                return nil, false
        }
        return ips, true
}

// scriptRunDialog — двухшаговый запуск скрипта: выбор файла → предпросмотр
// и список целей → явное подтверждение. Раньше кнопка сразу запускала
// скрипт на выделенных хостах, что было опасно и непредсказуемо.
func (u *HostsUI) scriptRunDialog() {
        ips, ok := u.requireSelection()
        if !ok {
                return
        }
        if !needCreds() {
                return
        }
        fn := fileChooser(app.window, "Выберите скрипт", gtk.FILE_CHOOSER_ACTION_OPEN, "")
        if fn == "" {
                return
        }
        preview := ""
        size := ""
        if data, err := os.ReadFile(fn); err == nil {
                size = fmt.Sprintf("%d КБ", len(data)/1024+1)
                txt := string(data)
                lines := strings.Split(txt, "\n")
                if len(lines) > 80 {
                        txt = strings.Join(lines[:80], "\n") + "\n… (показаны первые 80 строк)"
                }
                if len(txt) > 8000 {
                        txt = txt[:8000] + "\n…"
                }
                preview = txt
        } else {
                preview = "(не удалось прочитать файл)"
        }
        d, derr := gtk.DialogNewWithButtons("Запуск скрипта", app.window, gtk.DIALOG_MODAL,
                []interface{}{"▶ Запустить на хостах", gtk.RESPONSE_ACCEPT},
                []interface{}{"Отмена", gtk.RESPONSE_CANCEL},
        )
        if derr != nil || d == nil {
                return
        }
        defer d.Destroy()
        registerModal(d)
        d.SetDefaultSize(640, 560)
        box, _ := d.GetContentArea()
        box.SetMarginStart(12)
        box.SetMarginEnd(12)
        box.SetMarginTop(10)
        box.SetMarginBottom(6)

        head := labelNew("")
        head.SetMarkup(fmt.Sprintf("<b>%s</b>  <span foreground=\"#8b93a5\">(%s)</span>\n"+
                "<span foreground=\"#8b93a5\">Будет выполнен на %d хост(ах):</span> <b>%s</b>",
                xmlEscapeStr(filepath.Base(fn)), size, len(ips), xmlEscapeStr(strings.Join(ips, ", "))))
        head.SetXAlign(0)
        head.SetLineWrap(true)
        box.PackStart(head, false, false, 4)

        pv := labelNew("Содержимое скрипта:")
        addClass(pv, "ratitle")
        pv.SetXAlign(0)
        box.PackStart(pv, false, false, 2)

        tv, _ := gtk.TextViewNew()
        tv.SetEditable(false)
        tv.SetMonospace(true)
        tvSet(tv, 120)
        tvSetText(tv, preview)
        psc := scrolled(tv, 0)
        psc.SetVExpand(true)
        box.PackStart(psc, true, true, 4)

        warn := labelNew("Скрипт загружается во временный файл на каждом хосте и выполняется через sh." +
                map[bool]string{true: " Включено повышение до root (su).", false: ""}[store.State.UseRoot])
        addClass(warn, "ramuted")
        warn.SetXAlign(0)
        warn.SetLineWrap(true)
        box.PackStart(warn, false, false, 4)
        box.ShowAll()

        if d.Run() == gtk.RESPONSE_ACCEPT {
                if !massGuard(ips, "Скрипт "+filepath.Base(fn), map[bool]int{true: 1, false: 0}[store.State.UseRoot]) {
                        return
                }
                logf("info", "📜 Скрипт %s на %d хост(ов) — подтверждено", filepath.Base(fn), len(ips))
                go runQuickScript(ips, fn)
        }
}

// replaceRunDialog — замена файла с явным подтверждением: файл, путь,
// список хостов и предупреждение о перезаписи в одном потоке действий.
func (u *HostsUI) replaceRunDialog() {
        ips, ok := u.requireSelection()
        if !ok {
                return
        }
        if !needCreds() {
                return
        }
        fn := fileChooser(app.window, "Локальный файл для замены", gtk.FILE_CHOOSER_ACTION_OPEN, "")
        if fn == "" {
                return
        }
        dst, ok := entryDialog(app.window, "Заменить файл", "Удалённый путь (напр. /etc/hosts)", "/etc/")
        if !ok || strings.TrimSpace(dst) == "" {
                return
        }
        dst = strings.TrimSpace(dst)
        if !confirmDanger(app.window, "замена файла "+filepath.Base(fn)+" → "+dst+"\n\nХосты: "+strings.Join(ips, ", "), len(ips)) {
                return
        }
        if !massGuard(ips, "Замена файла "+filepath.Base(fn)+" → "+dst, map[bool]int{true: 1, false: 0}[store.State.UseRoot]) {
                return
        }
        logf("warn", "🔄 Замена файла %s → %s на %d хост(ах) — подтверждено", filepath.Base(fn), dst, len(ips))
        go runQuickReplace(ips, fn, dst)
}

// restoreSession восстанавливает выбранную группу и выделение хостов.
func (u *HostsUI) restoreSession(s SessionState) {
        if s.Group != "" && s.Group != "Все хосты" {
                u.selectGroup(s.Group)
        }
        if len(s.SelectedHosts) == 0 || u.hostTree == nil {
                return
        }
        sel, err := u.hostTree.GetSelection()
        if err != nil {
                return
        }
        m, ok := treeModelOf(u.hostTree)
        if !ok {
                return
        }
        want := map[string]bool{}
        for _, ip := range s.SelectedHosts {
                want[ip] = true
        }
        iter, ok2 := m.GetIterFirst()
        for ok2 {
                if want[iterGetString(m, iter, HCOL_IP)] {
                        sel.SelectIter(iter)
                }
                ok2 = m.IterNext(iter)
        }
}

// applyHostMove performs the actual "drag host(s) onto a group" operation.
// It runs on the GTK main loop via glibIdle, after the DnD signal machinery
// has fully finished, and never crashes on the drop row.
func (u *HostsUI) applyHostMove(txt string, dropPath *gtk.TreePath, x, y int) {
        if os.Getenv("RA_DEBUG") != "" {
                fmt.Printf("[dnd] drop at (%d,%d) path=%v\n", x, y, dropPath)
        }
        group, ok := u.groupFromPath(dropPath)
        if !ok {
                group, ok = u.groupAtPos(x, y)
        }
        dbg(fmt.Sprintf("[dnd] target group -> %q ok=%v", group, ok))
        if !ok {
                logf("warn", "↷ Перетащите хост на строку группы слева (например «Общие»)")
                if os.Getenv("RA_DEBUG") != "" {
                        fmt.Println("[dnd] no group row at drop point")
                }
                return
        }
        n := 0
        for _, ip := range strings.Split(txt, "\n") {
                ip = strings.TrimSpace(ip)
                if ip == "" {
                        continue
                }
                if h := store.FindHost(ip); h != nil {
                        h.Group = group
                        n++
                }
        }
        if n == 0 {
                return
        }
        // make sure the target group is registered (host groups show up
        // automatically; this covers groups created while dragging)
        _ = store.AddGroup(group)
        _ = store.Save()
        u.refreshAll()
        logf("ok", "↷ %d хост(ов) → группа «%s»", n, group)
}

func (u *HostsUI) selectedHosts() []string {
        return selectedStrings(u.hostTree, HCOL_IP)
}

func (u *HostsUI) targetHosts() []string {
        sel := u.selectedHosts()
        if len(sel) > 0 {
                return sel
        }
        var out []string
        m, ok := treeModelOf(u.hostTree)
        if !ok {
                return nil
        }
        iter, ok2 := m.GetIterFirst()
        for ok2 {
                out = append(out, iterGetString(m, iter, HCOL_IP))
                ok2 = m.IterNext(iter)
        }
        return out
}

func (u *HostsUI) addFromEntry() {
        raw := entText(u.addEntry)
        group, _ := u.selectedGroup()
        if group == "Все хосты" || group == "" {
                group = "Общие"
        }
        added := 0
        var errs []string
        for _, tok := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == ';' || r == '\n' }) {
                tok = strings.TrimSpace(tok)
                if tok == "" {
                        continue
                }
                if err := store.AddHost(tok, group); err != nil {
                        errs = append(errs, tok)
                } else {
                        added++
                }
        }
        u.addEntry.SetText("")
        _ = store.Save()
        u.refreshAll()
        if added > 0 {
                logf("ok", "➕ Добавлено хостов: %d (группа «%s»)", added, group)
        }
        if len(errs) > 0 {
                logf("warn", "⚠ Пропущены (дубли/пустые): %s", strings.Join(errs, ", "))
        }
}

func (u *HostsUI) deleteSelected() {
        ips := u.selectedHosts()
        if len(ips) == 0 {
                return
        }
        if !confirmDialog(app.window, fmt.Sprintf("Удалить выбранные хосты (%d)?", len(ips))) {
                return
        }
        store.RemoveHosts(ips)
        _ = store.Save()
        u.refreshAll()
        logf("ok", "🗑 Удалено хостов: %d", len(ips))
}

// updateStatusRow updates a host row badge after background work.
func (u *HostsUI) updateStatusRow(ip, status string) {
        store.SetHostStatus(ip, status)
        glibIdle(func() {
                m, ok := treeModelOf(u.hostTree)
                if !ok {
                        return
                }
                iter, ok2 := m.GetIterFirst()
                for ok2 {
                        if iterGetString(m, iter, HCOL_IP) == ip {
                                emoji, color := statusView(status)
                                _ = u.hostStore.SetValue(iter, HCOL_STATUS, emoji)
                                _ = u.hostStore.SetValue(iter, HCOL_COLOR, color)
                                return
                        }
                        ok2 = m.IterNext(iter)
                }
        })
}

func (u *HostsUI) resetAllStatus() {
        for i := range store.State.Hosts {
                store.State.Hosts[i].Status = ""
        }
        glibIdle(func() {
                m, ok := treeModelOf(u.hostTree)
                if !ok {
                        return
                }
                iter, ok2 := m.GetIterFirst()
                for ok2 {
                        _ = u.hostStore.SetValue(iter, HCOL_STATUS, "○")
                        _ = u.hostStore.SetValue(iter, HCOL_COLOR, "#667089")
                        ok2 = m.IterNext(iter)
                }
        })
}

// renameSelectedGroup asks a new name and renames the group everywhere.
func (u *HostsUI) renameSelectedGroup() {
        g, ok := u.selectedGroup()
        if !ok {
                warnDialog(app.window, "Выберите группу в списке слева")
                return
        }
        if g == "Все хосты" {
                warnDialog(app.window, "«Все хосты» — это не группа, а сводный список")
                return
        }
        newName, ok := entryDialog(app.window, "Переименовать группу", "Новое имя группы", g)
        if !ok {
                return
        }
        newName = strings.TrimSpace(newName)
        if newName == "" || newName == g {
                return
        }
        if err := store.RenameGroup(g, newName); err != nil {
                warnDialog(app.window, err.Error())
                return
        }
        _ = store.Save()
        u.refreshAll()
        u.selectGroup(newName)
        logf("ok", "✎ Группа «%s» переименована в «%s»", g, newName)
}

// editHostDialog changes name/address/group of one host (double-click or ✎).
func (u *HostsUI) editHostDialog(ip string) {
        h := store.FindHost(ip)
        if h == nil {
                warnDialog(app.window, "Хост не найден: "+ip)
                return
        }
        d, err := gtk.DialogNewWithButtons("Хост: "+h.DisplayName(), app.window, gtk.DIALOG_MODAL,
                []interface{}{"Сохранить", gtk.RESPONSE_ACCEPT},
                []interface{}{"Отмена", gtk.RESPONSE_CANCEL},
        )
        if err != nil || d == nil {
                return
        }
        defer d.Destroy()
        d.SetDefaultSize(430, 210)
        box, _ := d.GetContentArea()
        box.SetMarginStart(12)
        box.SetMarginEnd(12)
        box.SetMarginTop(8)
        box.SetMarginBottom(4)

        nameE, _ := gtk.EntryNew()
        nameE.SetPlaceholderText("например: web-1, банк-ecad")
        nameE.SetText(h.Name)
        ipE, _ := gtk.EntryNew()
        ipE.SetPlaceholderText("192.168.1.10 или host:2222")
        ipE.SetText(h.IP)
        grpC, _ := gtk.ComboBoxTextNew()
        groups := store.Groups()
        active := 0
        for i, g := range groups {
                grpC.AppendText(g)
                if g == h.Group {
                        active = i
                }
        }
        if len(groups) > 0 {
                grpC.SetActive(active)
        }

        g1 := mkGrid()
        g1.attach(labelNew("Имя:"), 0, 0, 1, 1)
        g1.attach(nameE, 1, 0, 1, 1)
        g1.attach(labelNew("Адрес:"), 0, 1, 1, 1)
        g1.attach(ipE, 1, 1, 1, 1)
        g1.attach(labelNew("Группа:"), 0, 2, 1, 1)
        g1.attach(grpC, 1, 2, 1, 1)
        tagE, _ := gtk.EntryNew()
        tagE.SetPlaceholderText("prod, web — метки через запятую (для поиска и фильтров)")
        tagE.SetText(strings.Join(h.Tags, ", "))
        g1.attach(labelNew("Метки:"), 0, 3, 1, 1)
        g1.attach(tagE, 1, 3, 1, 1)
        // среда: показывается цветным бейджем рядом с хостом и учитывается
        // при подтверждении массовых изменений
        envC, _ := gtk.ComboBoxTextNew()
        envC.AppendText("Не задана")
        for _, e := range hostEnvs[1:] {
                envC.AppendText(e)
        }
        envActive := 0
        for i, e := range hostEnvs {
                if e != "" && e == h.Env {
                        envActive = i
                }
        }
        envC.SetActive(envActive)
        g1.attach(labelNew("Среда:"), 0, 4, 1, 1)
        g1.attach(envC, 1, 4, 1, 1)
        favC, _ := gtk.CheckButtonNewWithLabel("★ Избранный хост (показывается со звёздочкой)")
        favC.SetActive(h.Fav)
        g1.attach(favC, 1, 5, 1, 1)
        box.PackStart(g1.w, false, false, 4)
        hint := labelNew("Имя — как хост показывается во всех списках и отчётах.\nАдрес — IP или хост:порт; метки — для поиска;\nсреда («Тест»/«Продакшен») — цветной бейдж и допподтверждение массовых изменений.")
        addClass(hint, "ramuted")
        hint.SetXAlign(0)
        box.PackStart(hint, false, false, 2)
        box.ShowAll()

        if d.Run() == gtk.RESPONSE_ACCEPT {
                newIP := strings.TrimSpace(entText(ipE))
                newName := strings.TrimSpace(entText(nameE))
                newGrp := ""
                if grpC.GetActive() >= 0 {
                        newGrp = grpC.GetActiveText()
                }
                if err := store.UpdateHost(ip, newIP, newName, newGrp); err != nil {
                        warnDialog(app.window, err.Error())
                        return
                }
                if newGrp != "" {
                        _ = store.AddGroup(newGrp)
                }
                // метки: разбор «prod, web» → []string
                var tags []string
                for _, t := range strings.Split(entText(tagE), ",") {
                        t = strings.TrimSpace(t)
                        t = strings.TrimPrefix(t, "#")
                        if t != "" {
                                tags = append(tags, t)
                        }
                }
                store.SetTags(ip, tags)
                // среда: «Не задана» = пустая строка
                newEnv := ""
                if envC.GetActive() > 0 {
                        newEnv = envC.GetActiveText()
                }
                if env := store.FindHost(ip); env != nil {
                        env.Env = newEnv
                }
                if favC.GetActive() != h.Fav {
                        store.ToggleFav(ip)
                }
                _ = store.Save()
                u.refreshAll()
                if app.sftpUI != nil {
                        app.sftpUI.refreshHosts()
                }
                if app.monUI != nil {
                        app.monUI.refreshHostCombo()
                }
                logf("ok", "✎ Хост обновлён: %s (%s, группа «%s»)", map[bool]string{true: newName, false: newIP}[newName != ""], newIP, newGrp)
        }
}

// ---------- command history ----------

type history struct {
        items []string
        pos   int
        draft string
}

func newHistory() *history { return &history{} }

func (h *history) push(s string) {
        if s == "" {
                return
        }
        if len(h.items) == 0 || h.items[len(h.items)-1] != s {
                h.items = append(h.items, s)
                if len(h.items) > 100 {
                        h.items = h.items[1:]
                }
        }
        h.pos = len(h.items)
}

func (h *history) up() string {
        if len(h.items) == 0 {
                return ""
        }
        if h.pos == len(h.items) {
                h.draft = "" // caller sets from entry before first up()
        }
        if h.pos > 0 {
                h.pos--
        }
        return h.items[h.pos]
}

func (h *history) down() string {
        if h.pos < len(h.items) {
                h.pos++
        }
        if h.pos >= len(h.items) {
                return h.draft
        }
        return h.items[h.pos]
}
