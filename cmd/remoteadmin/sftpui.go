package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/pkg/sftp"
)

// SFTPUI — dual-panel file manager: local FS ↔ remote host (FTP-like UX).
//
// Интерфейс «как в обычных коммандерах»:
//   - папки и файлы различаются значками и цветом;
//   - горячие клавиши F3 (просмотр) F4 (правка) F5 (копия) F6 (имя)
//     F7 (каталог) F8 (удалить) работают в обеих панелях;
//   - под каждой панелью — полоса кнопок с подсказками клавиш.
type SFTPUI struct {
	widget gtk.IWidget

	// connection row
	hostCombo   *gtk.ComboBoxText
	remoteHostL *gtk.Label

	// local panel
	localPath  *gtk.Entry
	localTree  *gtk.TreeView
	localStore *gtk.ListStore

	// remote panel
	remotePath  *gtk.Entry
	remoteTree  *gtk.TreeView
	remoteStore *gtk.ListStore

	mu        sync.Mutex
	client    *SFTPClient
	hostIP    string
	hostCount int // элементов в селекторе (для автотеста)
	connected bool
}

const (
	FCOL_NAME  = 0 // markup: иконка + имя
	FCOL_SIZE  = 1
	FCOL_MTIME = 2
	FCOL_TYPE  = 3 // hidden: "каталог" | "файл"
	FCOL_RAW   = 4 // hidden: чистое имя (без markup)
)

const sftpMaxEditSize = 2 << 20 // 2 МБ — предел для просмотра/правки

func newSFTPUI() *SFTPUI {
	u := &SFTPUI{}
	root := boxNew(gtk.ORIENTATION_VERTICAL, 6)

	// top bar: host selector + connect
	top := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	top.PackStart(labelNew("Хост:"), false, false, 2)
	u.hostCombo, _ = gtk.ComboBoxTextNew()
	u.hostCombo.SetTooltipText("Выберите хост для файлового обмена (SFTP)")
	u.hostCombo.SetSizeRequest(170, -1)
	top.PackStart(u.hostCombo, false, false, 0)
	btnReHosts, _ := gtk.ButtonNew()
	addClass(btnReHosts, "flat")
	btnReHosts.SetLabel("⟳")
	btnReHosts.SetTooltipText("Обновить список хостов")
	btnReHosts.Connect("clicked", func() { u.refreshHosts() })
	top.PackStart(btnReHosts, false, false, 0)

	btnConn := buttonNew("Подключиться")
	scs, _ := btnConn.GetStyleContext()
	scs.AddClass("suggested")
	btnConn.Connect("clicked", func() { u.connect() })
	u.remoteHostL = labelNew("не подключён")
	addClass(u.remoteHostL, "ramuted")
	top.PackStart(btnConn, false, false, 4)
	top.PackStart(u.remoteHostL, false, false, 2)

	btnDisc := buttonNew("Отключить")
	btnDisc.Connect("clicked", func() { u.disconnect() })
	top.PackStart(btnDisc, false, false, 0)

	// сравнение и синхронизация панелей (в духе Total Commander)
	btnDiff := buttonNew("Сравнить")
	btnDiff.SetTooltipText("Рекурсивно сравнить левый и правый каталоги: что отсутствует, что различается")
	btnDiff.Connect("clicked", func() { showPanelDiffDialog(u) })
	btnMirrorLR := buttonNew("→")
	btnMirrorLR.SetTooltipText("Зеркалировать слева направо: скопировать отсутствующее и изменившееся на хост")
	btnMirrorLR.Connect("clicked", func() { mirrorQuick(u, true) })
	btnMirrorRL := buttonNew("←")
	btnMirrorRL.SetTooltipText("Зеркалировать справа налево: скопировать отсутствующее и изменившееся локально")
	btnMirrorRL.Connect("clicked", func() { mirrorQuick(u, false) })
	btnFanout := buttonNew("На все хосты…")
	btnFanout.SetTooltipText("Разослать выбранные локальные файлы на все хосты выбранной группы")
	btnFanout.Connect("clicked", func() { uploadSelectedToHosts(u) })
	top.PackEnd(btnFanout, false, false, 0)
	top.PackEnd(btnMirrorRL, false, false, 0)
	top.PackEnd(btnMirrorLR, false, false, 0)
	top.PackEnd(btnDiff, false, false, 0)
	root.PackStart(top, false, false, 0)
	u.refreshHosts()

	// two panels
	panels := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)

	// ---- local ----
	lCard, lBox := cardBox("💻  Локальная система")
	lPathRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
	btnLUp := buttonNew("↑")
	btnLHome := buttonNew("🏠")
	btnLRefresh := buttonNew("⟳")
	u.localPath, _ = gtk.EntryNew()
	u.localPath.Connect("activate", func() { u.loadLocal(entText(u.localPath)) })
	btnLUp.SetTooltipText("Вверх")
	btnLUp.Connect("clicked", func() { u.loadLocal(filepath.Dir(u.localDir())) })
	btnLHome.SetTooltipText("Домой")
	btnLHome.Connect("clicked", func() { home, _ := os.UserHomeDir(); u.loadLocal(home) })
	btnLRefresh.SetTooltipText("Обновить")
	btnLRefresh.Connect("clicked", func() { u.loadLocal(u.localDir()) })
	lPathRow.PackStart(btnLUp, false, false, 0)
	lPathRow.PackStart(btnLHome, false, false, 0)
	lPathRow.PackStart(u.localPath, true, true, 0)
	lPathRow.PackStart(btnLRefresh, false, false, 0)
	lBox.PackStart(lPathRow, false, false, 0)

	lTitles := []string{"Имя", "Размер", "Изменён", "", ""}
	lTypes := []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING}
	u.localTree, u.localStore = makeTreeM(lTitles, lTypes, map[int]bool{FCOL_NAME: true})
	hideCol(u.localTree, FCOL_TYPE)
	hideCol(u.localTree, FCOL_RAW)
	lsel, _ := u.localTree.GetSelection()
	lsel.SetMode(gtk.SELECTION_MULTIPLE)
	u.localTree.Connect("row-activated", func(_ *gtk.TreeView, path *gtk.TreePath, _ *gtk.TreeViewColumn) {
		u.localOpen(path)
	})
	u.localTree.Connect("key-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
		return u.hotkeys(fkKey(ev), true)
	})
	lsc := scrolled(u.localTree, 0)
	lsc.SetVExpand(true)
	lBox.PackStart(lsc, true, true, 0)
	lBox.PackStart(u.panelButtons(true), false, false, 2)

	// ---- remote ----
	rCard, rBox := cardBox("🖥  Удалённый хост")
	rPathRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
	btnRUp := buttonNew("↑")
	btnRHome := buttonNew("🏠")
	btnRRefresh := buttonNew("⟳")
	u.remotePath, _ = gtk.EntryNew()
	u.remotePath.SetSensitive(false)
	u.remotePath.Connect("activate", func() { u.loadRemote(entText(u.remotePath)) })
	btnRUp.SetTooltipText("Вверх")
	btnRUp.Connect("clicked", func() { u.loadRemote(remoteParent(u.remoteDir())) })
	btnRHome.SetTooltipText("Домой (/root при входе под root)")
	btnRHome.Connect("clicked", func() { u.loadRemote(".") })
	btnRRefresh.SetTooltipText("Обновить")
	btnRRefresh.Connect("clicked", func() { u.loadRemote(u.remoteDir()) })
	rPathRow.PackStart(btnRUp, false, false, 0)
	rPathRow.PackStart(btnRHome, false, false, 0)
	rPathRow.PackStart(u.remotePath, true, true, 0)
	rPathRow.PackStart(btnRRefresh, false, false, 0)
	rBox.PackStart(rPathRow, false, false, 0)

	u.remoteTree, u.remoteStore = makeTreeM(lTitles, lTypes, map[int]bool{FCOL_NAME: true})
	hideCol(u.remoteTree, FCOL_TYPE)
	hideCol(u.remoteTree, FCOL_RAW)
	rsel, _ := u.remoteTree.GetSelection()
	rsel.SetMode(gtk.SELECTION_MULTIPLE)
	u.remoteTree.Connect("row-activated", func(_ *gtk.TreeView, path *gtk.TreePath, _ *gtk.TreeViewColumn) {
		u.remoteOpen(path)
	})
	u.remoteTree.Connect("key-press-event", func(_ *gtk.TreeView, ev *gdk.Event) bool {
		return u.hotkeys(fkKey(ev), false)
	})
	rsc := scrolled(u.remoteTree, 0)
	rsc.SetVExpand(true)
	rBox.PackStart(rsc, true, true, 0)
	rBox.PackStart(u.panelButtons(false), false, false, 2)

	panels.PackStart(lCard, true, true, 0)
	panels.PackStart(rCard, true, true, 0)
	root.PackStart(panels, true, true, 0)

	// hint bar
	hint := labelNew("F3 — просмотр · F4 — правка (с бэкапом) · F5 — копия на другую сторону · F6 — переименовать · F7 — каталог · F8 — удалить · Enter — открыть")
	addClass(hint, "ramuted")
	hint.SetXAlign(0)
	root.PackStart(hint, false, false, 0)

	home, _ := os.UserHomeDir()
	u.loadLocal(home)
	u.widget = root
	return u
}

// ---------- hotkeys ----------

type sftpKey int

const (
	skNone sftpKey = iota
	skView
	skEdit
	skCopy
	skRename
	skMkdir
	skDelete
)

func fkKey(ev *gdk.Event) sftpKey {
	ke := gdk.EventKeyNewFromEvent(ev)
	switch ke.KeyVal() {
	case gdk.KEY_F3:
		return skView
	case gdk.KEY_F4:
		return skEdit
	case gdk.KEY_F5:
		return skCopy
	case gdk.KEY_F6:
		return skRename
	case gdk.KEY_F7:
		return skMkdir
	case gdk.KEY_F8, gdk.KEY_Delete:
		return skDelete
	}
	return skNone
}

// hotkeys dispatches a commander-style hotkey; isLocal — какая панель в фокусе.
func (u *SFTPUI) hotkeys(k sftpKey, isLocal bool) bool {
	switch k {
	case skView:
		u.viewSelected(isLocal)
		return true
	case skEdit:
		u.editSelected(isLocal)
		return true
	case skCopy:
		go u.transfer(isLocal)
		return true
	case skRename:
		if isLocal {
			u.localRename()
		} else {
			u.remoteRename()
		}
		return true
	case skMkdir:
		if isLocal {
			u.localMkdir()
		} else {
			u.remoteMkdir()
		}
		return true
	case skDelete:
		if isLocal {
			u.localDelete()
		} else {
			u.remoteDelete()
		}
		return true
	}
	return false
}

// panelButtons — полоса кнопок в стиле Norton/Total Commander.
func (u *SFTPUI) panelButtons(isLocal bool) gtk.IWidget {
	bar := boxNew(gtk.ORIENTATION_HORIZONTAL, 3)
	mkBtn := func(label, tip string, fn func()) *gtk.Button {
		b := buttonNew(label)
		b.SetTooltipText(tip)
		addClass(b, "fkeybtn")
		b.Connect("clicked", fn)
		return b
	}
	var viewB, editB, copyB, renB, mkB, delB *gtk.Button
	if isLocal {
		viewB = mkBtn("F3 Просмотр", "Показать содержимое файла", func() { u.viewSelected(true) })
		editB = mkBtn("F4 Правка", "Редактировать файл (оригинал сохраняется в .ra_bak_…)", func() { u.editSelected(true) })
		copyB = mkBtn("F5 Загрузить", "Скопировать выбранное на удалённый хост", func() { go u.transfer(true) })
		renB = mkBtn("F6 Имя", "Переименовать", func() { u.localRename() })
		mkB = mkBtn("F7 Каталог", "Создать каталог", func() { u.localMkdir() })
		delB = mkBtn("F8 Удалить", "Удалить выбранное", func() { u.localDelete() })
	} else {
		viewB = mkBtn("F3 Просмотр", "Показать содержимое файла", func() { u.viewSelected(false) })
		editB = mkBtn("F4 Правка", "Редактировать файл (бэкап .ra_bak_… создаётся автоматически)", func() { u.editSelected(false) })
		copyB = mkBtn("F5 Скачать", "Скопировать выбранное в локальный каталог", func() { go u.transfer(false) })
		renB = mkBtn("F6 Имя", "Переименовать", func() { u.remoteRename() })
		mkB = mkBtn("F7 Каталог", "Создать каталог", func() { u.remoteMkdir() })
		delB = mkBtn("F8 Удалить", "Удалить выбранное", func() { u.remoteDelete() })
	}
	for _, b := range []*gtk.Button{viewB, editB, copyB, renB, mkB, delB} {
		bar.PackStart(b, true, true, 0)
	}
	return bar
}

// ---------- helpers ----------

func (u *SFTPUI) localDir() string {
	d := entText(u.localPath)
	if d == "" {
		d = "/"
	}
	return d
}

func (u *SFTPUI) remoteDir() string {
	d := entText(u.remotePath)
	if d == "" {
		d = "/"
	}
	return d
}

func remoteParent(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	return filepath.Dir(p)
}

func humanSize(n int64) string {
	const k = 1024
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1f ГБ", float64(n)/(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.1f МБ", float64(n)/(k*k))
	case n >= k:
		return fmt.Sprintf("%.1f КБ", float64(n)/k)
	}
	return fmt.Sprintf("%d Б", n)
}

// fileIcon возвращает markup-префикс, различающий папки и файлы:
// папки — синяя жирная иконка папки, файлы — обычный текст.
func fileIcon(name string, isDir bool) string {
	if isDir {
		return "📁 "
	}
	switch {
	case strings.HasSuffix(name, ".sh"), strings.HasSuffix(name, ".run"):
		return "⚙ "
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"),
		strings.HasSuffix(name, ".zip"), strings.HasSuffix(name, ".xz"),
		strings.HasSuffix(name, ".gz"), strings.HasSuffix(name, ".rpm"),
		strings.HasSuffix(name, ".deb"):
		return "📦 "
	case strings.HasSuffix(name, ".log"), strings.HasSuffix(name, ".txt"),
		strings.HasSuffix(name, ".conf"), strings.HasSuffix(name, ".cfg"),
		strings.HasSuffix(name, ".ini"), strings.HasSuffix(name, ".yaml"),
		strings.HasSuffix(name, ".yml"), strings.HasSuffix(name, ".json"):
		return "📄 "
	}
	return "📄 "
}

// fillFileStore заполняет список с сортировкой «папки сверху».
func fillFileStore(st *gtk.ListStore, names []string, sizes []int64, mtimes []time.Time, isDirs []bool) {
	st.Clear()
	idx := make([]int, len(names))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		if isDirs[idx[a]] != isDirs[idx[b]] {
			return isDirs[idx[a]] // dirs first
		}
		return strings.ToLower(names[idx[a]]) < strings.ToLower(names[idx[b]])
	})
	for _, i := range idx {
		typ := "файл"
		size := humanSize(sizes[i])
		mt := mtimes[i].Format("02.01.2006 15:04")
		if isDirs[i] {
			typ = "каталог"
			size = "—"
			mt = ""
		}
		nm := xmlEscapeStr(names[i])
		if isDirs[i] {
			nm = "<span foreground=\"#60a5fa\" weight=\"bold\">📁 " + nm + "</span>"
		} else {
			nm = fileIcon(names[i], false) + nm
		}
		if names[i] == ".." {
			nm = "<span foreground=\"#8b93a5\">📁 ..</span>"
		}
		iter := st.Append()
		_ = st.Set(iter, []int{FCOL_NAME, FCOL_SIZE, FCOL_MTIME, FCOL_TYPE, FCOL_RAW},
			[]interface{}{nm, size, mt, typ, names[i]})
	}
}

// refreshHosts rebuilds the host selector, preserving the current choice.
func (u *SFTPUI) refreshHosts() {
	if u.hostCombo == nil {
		return
	}
	cur := u.hostCombo.GetActiveText()
	u.hostCombo.RemoveAll()
	hosts := make([]HostEntry, len(store.State.Hosts))
	copy(hosts, store.State.Hosts)
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Group != hosts[j].Group {
			return hosts[i].Group < hosts[j].Group
		}
		return hosts[i].DisplayName() < hosts[j].DisplayName()
	})
	def := 0
	for i, h := range hosts {
		u.hostCombo.AppendText(h.IP)
		if h.IP == cur {
			def = i
		}
	}
	u.hostCount = len(hosts)
	if len(hosts) > 0 {
		// default: the host selected on the Хосты tab, otherwise first
		sel := []string(nil)
		if app != nil && app.hostsUI != nil {
			sel = app.hostsUI.selectedHosts()
		}
		if len(sel) == 1 {
			for i, h := range hosts {
				if h.IP == sel[0] {
					def = i
				}
			}
		}
		u.hostCombo.SetActive(def)
	}
}

// ---------- view / edit ----------

// looksBinary определяет двоичный файл (доля непечатаемых символов).
func looksBinary(data []byte) bool {
	n := len(data)
	if n == 0 {
		return false
	}
	if n > 4096 {
		n = 4096
	}
	if !utf8.Valid(data[:n]) {
		return true
	}
	bad := 0
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
		if b < 9 || (b > 13 && b < 32) {
			bad++
		}
	}
	return bad*10 > n
}

// selectedFileInfo — имя и полный путь выбранного файла (одного).
// Вызывать ТОЛЬКО из главного потока GTK.
func (u *SFTPUI) selectedFileInfo(isLocal bool) (name, path string, ok bool) {
	var sel []string
	if isLocal {
		sel = selectedStrings(u.localTree, FCOL_RAW)
	} else {
		sel = selectedStrings(u.remoteTree, FCOL_RAW)
	}
	if len(sel) == 0 {
		warnDialog(app.window, "Выберите файл в списке")
		return "", "", false
	}
	if sel[0] == ".." {
		return "", "", false
	}
	name = sel[0]
	if isLocal {
		path = filepath.Join(u.localDir(), name)
	} else {
		path = remoteJoin(u.remoteDir(), name)
	}
	return name, path, true
}

// viewSelected показывает файл (F3): локальный (isLocal) или удалённый.
func (u *SFTPUI) viewSelected(isLocal bool) {
	name, path, ok := u.selectedFileInfo(isLocal)
	if !ok {
		return
	}
	go func() {
		var data []byte
		var err error
		if isLocal {
			data, err = os.ReadFile(path)
		} else {
			cl := u.sftpClient()
			if cl == nil {
				return
			}
			data, err = cl.ReadFile(path)
		}
		glibIdle(func() {
			if err != nil {
				errDialog(app.window, "Чтение: "+err.Error())
				return
			}
			if looksBinary(data) {
				warnDialog(app.window, "Файл двоичный — просмотр в виде текста невозможен.\nИспользуйте загрузку/скачивание (F5).")
				return
			}
			textViewDialog(app.window, "Просмотр: "+name, string(data), false)
		})
	}()
}

// editSelected правит файл (F4): читает → редактор → сохранение с
// автоматическим бэкапом «имя.ra_bak_дата».
func (u *SFTPUI) editSelected(isLocal bool) {
	name, path, ok := u.selectedFileInfo(isLocal)
	if !ok {
		return
	}
	go func() {
		var data []byte
		var mode os.FileMode = 0644
		var err error
		if isLocal {
			if fi, statErr := os.Stat(path); statErr == nil {
				mode = fi.Mode().Perm()
			}
			data, err = os.ReadFile(path)
		} else {
			cl := u.sftpClient()
			if cl == nil {
				return
			}
			if fi, statErr := cl.SFTP.Stat(path); statErr == nil {
				mode = fi.Mode().Perm()
			}
			data, err = cl.ReadFile(path)
		}
		glibIdle(func() {
			if err != nil {
				errDialog(app.window, "Чтение: "+err.Error())
				return
			}
			if len(data) > sftpMaxEditSize {
				warnDialog(app.window, fmt.Sprintf("Файл больше %d МБ — редактирование недоступно.", sftpMaxEditSize>>20))
				return
			}
			if looksBinary(data) {
				warnDialog(app.window, "Файл двоичный — правка в виде текста небезопасна.")
				return
			}
			newText, saved := textViewDialog(app.window, "Правка: "+name, string(data), true)
			if !saved {
				return
			}
			go func() {
				var serr error
				var bak string
				stamp := time.Now().Format("20060102_150405")
				if isLocal {
					bak = path + ".ra_bak_" + stamp
					_ = os.Rename(path, bak) // бэкап; при неудаче просто перезаписываем
					serr = os.WriteFile(path, []byte(newText), mode)
				} else {
					cl := u.sftpClient()
					if cl == nil {
						return
					}
					bak, serr = cl.WriteFileWithBackup(path, []byte(newText), mode)
				}
				glibIdle(func() {
					if serr != nil {
						errDialog(app.window, "Сохранение: "+serr.Error())
						return
					}
					msg := "✏ Сохранено: " + path
					if bak != "" {
						msg += " (бэкап: " + filepath.Base(bak) + ")"
					}
					logf("ok", "%s", msg)
					if isLocal {
						u.loadLocal(u.localDir())
					} else {
						u.loadRemote(u.remoteDir())
					}
				})
			}()
		})
	}()
}

// ---------- local panel ----------

func (u *SFTPUI) loadLocal(dir string) {
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		warnDialog(app.window, "Не каталог: "+dir)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		warnDialog(app.window, "Ошибка чтения: "+err.Error())
		return
	}
	var names []string
	var sizes []int64
	var mtimes []time.Time
	var dirs []bool
	if dir != "/" {
		names = append(names, "..")
		sizes = append(sizes, 0)
		mtimes = append(mtimes, time.Time{})
		dirs = append(dirs, true)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		names = append(names, e.Name())
		sizes = append(sizes, info.Size())
		mtimes = append(mtimes, info.ModTime())
		dirs = append(dirs, info.IsDir())
	}
	fillFileStore(u.localStore, names, sizes, mtimes, dirs)
	u.localPath.SetText(dir)
}

func (u *SFTPUI) localOpen(path *gtk.TreePath) {
	m, ok := treeModelOf(u.localTree)
	if !ok {
		return
	}
	iter, err := m.GetIter(path)
	if err != nil {
		return
	}
	name := iterGetString(m, iter, FCOL_RAW)
	typ := iterGetString(m, iter, FCOL_TYPE)
	if typ == "каталог" {
		u.loadLocal(filepath.Join(u.localDir(), name))
	}
}

func (u *SFTPUI) localSelection() []string {
	return treeSelection(u.localTree, u.localStore, FCOL_RAW)
}

func (u *SFTPUI) localMkdir() {
	name, ok := entryDialog(app.window, "Новый каталог", "Имя каталога", "")
	if !ok || name == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(u.localDir(), name), 0755); err != nil {
		errDialog(app.window, err.Error())
	}
	u.loadLocal(u.localDir())
}

func (u *SFTPUI) localRename() {
	sel := u.localSelection()
	if len(sel) != 1 || sel[0] == ".." {
		return
	}
	newName, ok := entryDialog(app.window, "Переименовать", "Новое имя", sel[0])
	if !ok || newName == "" || newName == sel[0] {
		return
	}
	if err := os.Rename(filepath.Join(u.localDir(), sel[0]), filepath.Join(u.localDir(), newName)); err != nil {
		errDialog(app.window, err.Error())
	}
	u.loadLocal(u.localDir())
}

func (u *SFTPUI) localDelete() {
	sel := u.localSelection()
	var files []string
	for _, s := range sel {
		if s != ".." {
			files = append(files, s)
		}
	}
	if len(files) == 0 {
		return
	}
	if !confirmDialog(app.window, fmt.Sprintf("Удалить выбранные объекты (%d) безвозвратно?", len(files))) {
		return
	}
	for _, f := range files {
		full := filepath.Join(u.localDir(), f)
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			_ = os.RemoveAll(full)
		} else {
			_ = os.Remove(full)
		}
	}
	u.loadLocal(u.localDir())
}

// ---------- remote panel ----------

func (u *SFTPUI) connect() {
	ip := ""
	if u.hostCombo != nil {
		ip = strings.TrimSpace(u.hostCombo.GetActiveText())
	}
	if ip == "" {
		ips := app.hostsUI.selectedHosts()
		if len(ips) == 1 {
			ip = ips[0]
		}
	}
	if ip == "" {
		warnDialog(app.window, "Сначала добавьте хосты на вкладке «Хосты» и выберите один в списке «Хост:»")
		return
	}
	if !needCreds() {
		return
	}
	app.setStatus("Подключение SFTP: " + ip + "…")
	u.remoteHostL.SetText("подключение к " + ip + "…")
	go func() {
		cl, err := OpenSFTP(buildCreds(ip))
		if err != nil {
			glibIdle(func() {
				u.remoteHostL.SetText("ошибка подключения")
				errDialog(app.window, "SFTP: "+err.Error())
				app.setStatus("SFTP: ошибка подключения")
			})
			logf("fail", "❌ SFTP %s: %v", ip, err)
			return
		}
		u.mu.Lock()
		if u.client != nil {
			u.client.Close()
		}
		u.client = cl
		u.hostIP = ip
		u.connected = true
		u.mu.Unlock()
		glibIdle(func() {
			who := ip
			if cl.AsRoot {
				who = ip + " (root — все каталоги доступны)"
			}
			u.remoteHostL.SetText("✓ " + who)
			u.remotePath.SetSensitive(true)
			u.loadRemote(".")
			app.setStatus("SFTP подключён: " + ip)
		})
		logf("ok", "🔌 SFTP подключён: %s", ip)
	}()
}

func (u *SFTPUI) disconnect() {
	u.mu.Lock()
	if u.client != nil {
		u.client.Close()
		u.client = nil
	}
	u.connected = false
	u.mu.Unlock()
	glibIdle(func() {
		u.remoteStore.Clear()
		u.remotePath.SetText("")
		u.remotePath.SetSensitive(false)
		u.remoteHostL.SetText("не подключён")
	})
	logf("info", "⏏ SFTP отключён")
}

func (u *SFTPUI) sftpClient() *SFTPClient {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.client
}

func (u *SFTPUI) loadRemote(dir string) {
	cl := u.sftpClient()
	if cl == nil {
		return
	}
	if dir == "" {
		dir = "."
	}
	go func() {
		infos, err := cl.ListDir(dir)
		if err != nil {
			glibIdle(func() { warnDialog(app.window, "SFTP: "+err.Error()) })
			return
		}
		var names []string
		var sizes []int64
		var mtimes []time.Time
		var dirs []bool
		if dir != "/" && dir != "." {
			names = append(names, "..")
			sizes = append(sizes, 0)
			mtimes = append(mtimes, time.Time{})
			dirs = append(dirs, true)
		}
		for _, fi := range infos {
			names = append(names, fi.Name())
			sizes = append(sizes, fi.Size())
			mtimes = append(mtimes, fi.ModTime())
			dirs = append(dirs, fi.IsDir())
		}
		// resolve relative to absolute
		abs := dir
		if !strings.HasPrefix(abs, "/") {
			if wd, err := cl.SFTP.Getwd(); err == nil {
				abs = filepath.Join(wd, dir)
			}
		}
		glibIdle(func() {
			fillFileStore(u.remoteStore, names, sizes, mtimes, dirs)
			u.remotePath.SetText(abs)
		})
	}()
}

func (u *SFTPUI) remoteOpen(path *gtk.TreePath) {
	m, ok := treeModelOf(u.remoteTree)
	if !ok {
		return
	}
	iter, err := m.GetIter(path)
	if err != nil {
		return
	}
	name := iterGetString(m, iter, FCOL_RAW)
	typ := iterGetString(m, iter, FCOL_TYPE)
	if typ == "каталог" {
		var target string
		if name == ".." {
			target = remoteParent(u.remoteDir())
		} else {
			target = remoteJoin(u.remoteDir(), name)
		}
		u.loadRemote(target)
	}
}

func (u *SFTPUI) remoteSelection() []string {
	return treeSelection(u.remoteTree, u.remoteStore, FCOL_RAW)
}

func (u *SFTPUI) remoteMkdir() {
	cl := u.sftpClient()
	if cl == nil {
		return
	}
	name, ok := entryDialog(app.window, "Новый каталог", "Имя каталога", "")
	if !ok || name == "" {
		return
	}
	if err := cl.MkdirAllP(remoteJoin(u.remoteDir(), name)); err != nil {
		errDialog(app.window, err.Error())
	}
	u.loadRemote(u.remoteDir())
}

func (u *SFTPUI) remoteRename() {
	cl := u.sftpClient()
	if cl == nil {
		return
	}
	sel := u.remoteSelection()
	if len(sel) != 1 || sel[0] == ".." {
		return
	}
	newName, ok := entryDialog(app.window, "Переименовать", "Новое имя", sel[0])
	if !ok || newName == "" || newName == sel[0] {
		return
	}
	if err := cl.SFTP.Rename(remoteJoin(u.remoteDir(), sel[0]), remoteJoin(u.remoteDir(), newName)); err != nil {
		errDialog(app.window, err.Error())
	}
	u.loadRemote(u.remoteDir())
}

func (u *SFTPUI) remoteDelete() {
	cl := u.sftpClient()
	if cl == nil {
		return
	}
	sel := u.remoteSelection()
	var files []string
	for _, s := range sel {
		if s != ".." {
			files = append(files, s)
		}
	}
	if len(files) == 0 {
		return
	}
	if !confirmDialog(app.window, fmt.Sprintf("Удалить на хосте %s выбранные объекты (%d)?", u.hostIP, len(files))) {
		return
	}
	for _, f := range files {
		full := remoteJoin(u.remoteDir(), f)
		if err := cl.RemoveAllP(full); err != nil {
			// fallback: try with root escalation
			if rp := store.RootPassword(); rp != "" {
				if _, e := ExecRoot(cl.SSH, "rm -rf "+shellQuote(full), rp, 60); e != nil {
					errDialog(app.window, "Не удалено "+full+": "+err.Error())
				}
			} else {
				errDialog(app.window, "Не удалено "+full+": "+err.Error())
			}
		}
	}
	u.loadRemote(u.remoteDir())
}

// ---------- transfers ----------

func (u *SFTPUI) transfer(upload bool) {
	cl := u.sftpClient()
	if cl == nil {
		warnDialog(app.window, "Сначала подключитесь к хосту")
		return
	}
	var pairs [][2]string // [0]=local, [1]=remote
	if upload {
		sel := u.localSelection()
		if len(sel) == 0 {
			return
		}
		for _, s := range sel {
			if s == ".." {
				continue
			}
			pairs = append(pairs, [2]string{filepath.Join(u.localDir(), s), remoteJoin(u.remoteDir(), s)})
		}
	} else {
		sel := u.remoteSelection()
		if len(sel) == 0 {
			return
		}
		for _, s := range sel {
			if s == ".." {
				continue
			}
			pairs = append(pairs, [2]string{filepath.Join(u.localDir(), s), remoteJoin(u.remoteDir(), s)})
		}
	}
	if len(pairs) == 0 {
		return
	}
	go func() {
		total := len(pairs)
		files, failed := 0, 0
		var bytes int64
		for i, p := range pairs {
			glibIdle(func() {
				app.setProgress(i, total)
				app.setStatus(fmt.Sprintf("Передача %d/%d: %s", i+1, total, filepath.Base(p[0])))
			})
			var err error
			var n int64
			if upload {
				n, err = cl.UploadPath(p[0], p[1])
			} else {
				n, err = cl.DownloadPath(p[1], p[0])
			}
			if err != nil {
				failed++
				logf("fail", "❌ %s: %v", filepath.Base(p[0]), err)
			} else {
				files++
				bytes += n
				logf("ok", "%s %s (%s)", map[bool]string{true: "📤", false: "📥"}[upload], filepath.Base(p[0]), humanSize(n))
			}
		}
		glibIdle(func() {
			app.setProgress(0, 0)
			if failed > 0 {
				app.setStatus(fmt.Sprintf("Передача: %d ок, %d с ошибками", files, failed))
			} else {
				app.setStatus(fmt.Sprintf("Передача завершена: %d объект(ов), %s", files, humanSize(bytes)))
			}
			if upload {
				u.loadRemote(u.remoteDir())
			} else {
				u.loadLocal(u.localDir())
			}
		})
		status := "OK"
		if failed > 0 {
			status = "WARN"
		}
		addReport("SFTP", u.hostIP, status, fmt.Sprintf("%s: %d объект(ов), %s (%s)",
			map[bool]string{true: "upload", false: "download"}[upload], files, humanSize(bytes), time.Now().Format("15:04:05")))
	}()
}

// ---------- shared tree helpers ----------

func treeSelection(tv *gtk.TreeView, st *gtk.ListStore, col int) []string {
	return selectedStrings(tv, col)
}

var _ = sftp.ErrSSHFxNoSuchFile
