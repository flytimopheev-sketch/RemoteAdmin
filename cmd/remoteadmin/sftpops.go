package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// ---------- сравнение и синхронизация панелей SFTP ----------
//
// В стиле Total Commander: показываем, какие файлы различаются между
// левой (локальной) и правой (удалённой) панелью, и позволяем
// «зеркалировать» недостающее/изменившееся в любую сторону.
// Сравнение рекурсивное (каталоги обходятся), по относительным путям.

const (
	syncMaxDepth   = 12   // защита от бесконечных обходов
	syncMaxEntries = 8000 // предохранитель для огромных деревьев
)

type panelEntry struct {
	rel           string // относительный путь (разделитель «/»)
	lsize, rsize  int64
	lmod, rmod    time.Time
	lex, rex      bool // существует слева / справа
	ldir, rdir    bool
	same, isDir   bool
	status        string // "совпадает" | "различается" | "только слева" | "только справа"
}

// walkLocalList собирает {rel: entry} для локального каталога.
func walkLocalList(root string, depth int, out map[string]*panelEntry) error {
	if len(out) > syncMaxEntries {
		return nil
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range ents {
		full := filepath.Join(root, e.Name())
		rel := e.Name()
		if root != "/" {
			rel, _ = filepath.Rel(root, full)
		}
		fi, err2 := e.Info()
		if err2 != nil {
			continue
		}
		p := &panelEntry{rel: rel, lex: true, lsize: fi.Size(), lmod: fi.ModTime(), ldir: fi.IsDir()}
		if fi.IsDir() {
			p.isDir = true
		}
		out[p.rel] = p
		if fi.IsDir() && depth < syncMaxDepth {
			// вложенные записи — с общим префиксом rel/
			sub := map[string]*panelEntry{}
			_ = walkLocalList(full, depth+1, sub)
			for k, v := range sub {
				out[p.rel+"/"+k] = v
			}
		}
	}
	return nil
}

// walkRemoteList — то же для удалённой стороны (SFTP).
func walkRemoteList(sf *SFTPClient, root string, depth int, out map[string]*panelEntry) error {
	if len(out) > syncMaxEntries {
		return nil
	}
	ents, err := sf.ListDir(root)
	if err != nil {
		return err
	}
	for _, fi := range ents {
		name := fi.Name()
		if name == "." || name == ".." {
			continue
		}
		p := &panelEntry{rel: name, rex: true, rsize: fi.Size(), rmod: fi.ModTime(), rdir: fi.IsDir()}
		if fi.IsDir() {
			p.isDir = true
		}
		out[p.rel] = p
		if fi.IsDir() && depth < syncMaxDepth {
			sub := map[string]*panelEntry{}
			_ = walkRemoteList(sf, remoteJoin(root, name), depth+1, sub)
			for k, v := range sub {
				out[p.rel+"/"+k] = v
			}
		}
	}
	return nil
}

// computePanelDiff объединяет два списка и вычисляет статусы.
func computePanelDiff(lDir, rDir string, sf *SFTPClient) ([]panelEntry, error) {
	local := map[string]*panelEntry{}
	remote := map[string]*panelEntry{}
	if err := walkLocalList(lDir, 0, local); err != nil {
		return nil, fmt.Errorf("локальный каталог: %v", err)
	}
	if err := walkRemoteList(sf, rDir, 0, remote); err != nil {
		return nil, fmt.Errorf("удалённый каталог: %v", err)
	}
	rels := map[string]bool{}
	for k := range local {
		rels[k] = true
	}
	for k := range remote {
		rels[k] = true
	}
	var out []panelEntry
	for rel := range rels {
		p := panelEntry{rel: rel}
		if l, ok := local[rel]; ok {
			p.lex, p.lsize, p.lmod, p.ldir = true, l.lsize, l.lmod, l.ldir
		}
		if r, ok := remote[rel]; ok {
			p.rex, p.rsize, p.rmod, p.rdir = true, r.rsize, r.rmod, r.rdir
		}
		switch {
		case p.lex && !p.rex:
			p.status = "только слева"
		case !p.lex && p.rex:
			p.status = "только справа"
		case p.ldir && p.rdir:
			p.status = "совпадает"
		case p.ldir != p.rdir:
			p.status = "различается"
		case p.lsize == p.rsize && p.lmod.Unix() == p.rmod.Unix():
			p.status = "совпадает"
		default:
			p.status = "различается"
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// ---------- диалог сравнения ----------

func showPanelDiffDialog(u *SFTPUI) {
	sf := u.sftpClient()
	if sf == nil {
		warnDialog(app.window, "Сначала подключитесь к хосту (правая панель)")
		return
	}
	lDir, rDir := u.localDir(), u.remoteDir()
	app.setStatus("Сравнение каталогов…")
	diff, err := computePanelDiff(lDir, rDir, sf)
	if err != nil {
		errDialog(app.window, "Сравнение: "+err.Error())
		return
	}
	d, err := gtk.DialogNewWithButtons(
		"Сравнение панелей", app.window, gtk.DIALOG_MODAL,
		[]interface{}{"Зеркалировать слева → вправо", gtk.RESPONSE_OK},
		[]interface{}{"Зеркалировать справа → влево", gtk.RESPONSE_APPLY},
		[]interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
	)
	if err != nil {
		return
	}
	defer d.Destroy()
	defer unregisterModal()
	registerModal(d)
	d.SetDefaultSize(880, 520)
	box, _ := d.GetContentArea()

	sameN, diffN, onlyL, onlyR := 0, 0, 0, 0
	for _, p := range diff {
		switch p.status {
		case "совпадает":
			sameN++
		case "различается":
			diffN++
		case "только слева":
			onlyL++
		case "только справа":
			onlyR++
		}
	}
	head := labelNew(fmt.Sprintf("%s  ⇄  %s\nСовпадают: %d  ·  различаются: %d  ·  только слева: %d  ·  только справа: %d",
		lDir, rDir, sameN, diffN, onlyL, onlyR))
	addClass(head, "ramuted")
	head.SetXAlign(0)
	box.PackStart(head, false, false, 6)

	tree, st := makeTree([]string{"Путь", "Слева (локально)", "Справа (на хосте)", "Статус"},
		[]glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING})
	showSame := false
	fill := func() {
		st.Clear()
		for _, p := range diff {
			if !showSame && p.status == "совпадает" {
				continue
			}
			cell := func(size int64, mod time.Time, isDir, exists bool) string {
				if !exists {
					return "—"
				}
				k := "файл"
				if isDir {
					k = "каталог"
				}
				return fmt.Sprintf("%s, %s, %s", k, humanSize(size), mod.Format("02.01 15:04"))
			}
			iter := st.Append()
			_ = st.Set(iter, []int{0, 1, 2, 3}, []interface{}{
				p.rel, cell(p.lsize, p.lmod, p.ldir, p.lex), cell(p.rsize, p.rmod, p.rdir, p.rex), p.status})
		}
	}
	fill()
	ch, _ := gtk.CheckButtonNewWithLabel("Показывать также совпадающие файлы")
	ch.Connect("toggled", func() {
		showSame = ch.GetActive()
		fill()
	})
	sc := scrolled(tree, 0)
	sc.SetVExpand(true)
	box.PackStart(sc, true, true, 6)
	botRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
	botRow.PackStart(ch, false, false, 0)
	box.PackStart(botRow, false, false, 2)
	box.ShowAll()

	resp := d.Run()
	switch resp {
	case gtk.RESPONSE_OK:
		mirrorPanel(u, diff, true)
	case gtk.RESPONSE_APPLY:
		mirrorPanel(u, diff, false)
	}
}

// mirrorPanel копирует недостающее/различающееся в указанную сторону.
func mirrorPanel(u *SFTPUI, diff []panelEntry, ltr bool) {
	var jobs []panelEntry
	var total int64
	for _, p := range diff {
		if ltr && (p.status == "совпадает") {
			continue
		}
		if !ltr && (p.status == "совпадает") {
			continue
		}
		// в сторону ltr: нужен источник слева (есть файл или каталог)
		if ltr {
			if !p.lex || p.ldir {
				continue // каталоги создаются при переносе содержимого
			}
			total += p.lsize
		} else {
			if !p.rex || p.rdir {
				continue
			}
			total += p.rsize
		}
		jobs = append(jobs, p)
	}
	if len(jobs) == 0 {
		infoDialog(app.window, "Различий для копирования нет")
		return
	}
	dir := "локальный → на хост"
	if !ltr {
		dir = "с хоста → локально"
	}
	if !confirmDialog(app.window, fmt.Sprintf("Зеркалирование (%s): файлов %d, объём %s.\nФайлы на приёмнике будут перезаписаны. Продолжить?", dir, len(jobs), humanSize(total))) {
		return
	}
	app.setStatus("Зеркалирование панелей…")
	logf("info", "🔁 Зеркалирование: %d файл(ов), %s", len(jobs), humanSize(total))
	go func() {
		var okN, failN int
		sf := u.sftpClient()
		if sf == nil {
			glibIdle(func() { errDialog(app.window, "Подключение потеряно") })
			return
		}
		lDir, rDir := u.localDir(), u.remoteDir()
		for _, p := range jobs {
			var err error
			if ltr {
				_, err = sf.UploadPath(filepath.Join(lDir, filepath.FromSlash(p.rel)), remoteJoin(rDir, p.rel))
			} else {
				_, err = sf.DownloadPath(remoteJoin(rDir, p.rel), filepath.Join(lDir, filepath.FromSlash(p.rel)))
			}
			if err != nil {
				failN++
				logf("fail", "🔁 %s: %v", p.rel, err)
			} else {
				okN++
			}
		}
		glibIdle(func() {
			if failN == 0 {
				logf("ok", "🔁 Зеркалирование завершено: файлов %d", okN)
				Notify("SFTP", fmt.Sprintf("Зеркалирование готово: %d файлов", okN))
			} else {
				logf("warn", "🔁 Зеркалирование: успешно %d, с ошибками %d", okN, failN)
			}
			app.setStatus("Готово")
			u.loadLocal(u.localDir())
			u.loadRemote(u.remoteDir())
		})
	}()
}

// ---------- групповая загрузка из SFTP ----------

// uploadSelectedToHosts отправляет выбранные локальные файлы/каталоги
// на все хосты выбранной группы (в один и тот же удалённый каталог).
func uploadSelectedToHosts(u *SFTPUI) {
	names := u.localSelection()
	if len(names) == 0 {
		warnDialog(app.window, "Выберите файлы/каталоги в ЛОКАЛЬНОЙ панели слева")
		return
	}
	lDir := u.localDir()
	var items []string
	for _, n := range names {
		items = append(items, filepath.Join(lDir, n))
	}

	d, err := gtk.DialogNewWithButtons(
		"Разослать файлы на хосты", app.window, gtk.DIALOG_MODAL,
		[]interface{}{"Разослать", gtk.RESPONSE_ACCEPT},
		[]interface{}{"Отмена", gtk.RESPONSE_CANCEL},
	)
	if err != nil {
		return
	}
	defer d.Destroy()
	defer unregisterModal()
	registerModal(d)
	d.SetDefaultSize(520, 240)
	box, _ := d.GetContentArea()
	box.SetMarginStart(14)
	box.SetMarginEnd(14)
	box.SetMarginTop(10)

	listLbl := labelNew("Файлы: " + strings.Join(names, ", "))
	addClass(listLbl, "ramuted")
	listLbl.SetLineWrap(true)
	listLbl.SetXAlign(0)
	box.PackStart(listLbl, false, false, 6)

	grid := mkGrid()
	cGrp, _ := gtk.ComboBoxTextNew()
	cGrp.AppendText("Все хосты")
	for _, g := range store.Groups() {
		cGrp.AppendText(g)
	}
	cGrp.SetActive(0)
	grid.attach(labelNew("Хосты:"), 0, 0, 1, 1)
	grid.attach(cGrp, 1, 0, 1, 1)

	eDir := entryNew("/tmp/upload")
	defDir := u.remoteDir()
	if defDir != "" && defDir != "/" {
		eDir.SetText(defDir)
	}
	grid.attach(labelNew("Каталог на хосте:"), 0, 1, 1, 1)
	grid.attach(eDir, 1, 1, 1, 1)
	box.PackStart(grid.w, false, false, 4)
	box.ShowAll()

	if d.Run() != gtk.RESPONSE_ACCEPT {
		return
	}
	group := cGrp.GetActiveText()
	remoteDir := strings.TrimSpace(entText(eDir))
	if remoteDir == "" {
		warnDialog(app.window, "Укажите каталог на хосте")
		return
	}
	var ips []string
	for _, h := range store.State.Hosts {
		if group == "Все хосты" || h.Group == group {
			ips = append(ips, h.IP)
		}
	}
	if len(ips) == 0 {
		warnDialog(app.window, "В выбранной группе нет хостов")
		return
	}
	if !needCreds() {
		return
	}
	logf("info", "📤 Разослать: %d файл(ов) на %d хост(ов) → %s", len(items), len(ips), remoteDir)
	go runParallel(ips, "Разослать файлы", func(ip string) (string, error) {
		creds := buildCreds(ip)
		sf, err := OpenSFTP(creds)
		if err != nil {
			return "", err
		}
		defer sf.Close()
		if err := sf.MkdirAllP(remoteDir); err != nil {
			return "", fmt.Errorf("создание каталога %s: %v", remoteDir, err)
		}
		var total int64
		for _, it := range items {
			n, err := sf.UploadPath(it, remoteJoin(remoteDir, filepath.Base(it)))
			total += n
			if err != nil {
				return fmt.Sprintf("успешно разослано %d байт", total), err
			}
		}
		return fmt.Sprintf("разослано %d файл(ов), %s в %s", len(items), humanSize(total), remoteDir), nil
	})
}

// mirrorQuick — зеркалирование одним нажатием: считаем различия,
// спрашиваем подтверждение и копируем в указанную сторону.
func mirrorQuick(u *SFTPUI, ltr bool) {
	sf := u.sftpClient()
	if sf == nil {
		warnDialog(app.window, "Сначала подключитесь к хосту (правая панель)")
		return
	}
	app.setStatus("Сравнение каталогов…")
	diff, err := computePanelDiff(u.localDir(), u.remoteDir(), sf)
	if err != nil {
		errDialog(app.window, "Сравнение: "+err.Error())
		return
	}
	mirrorPanel(u, diff, ltr)
}
