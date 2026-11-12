package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// curModal — открытое модальное окно; используется автотестом
// (--shot-dialogs) для снятия скриншотов и программного закрытия.
var curModal *gtk.Dialog

func registerModal(d *gtk.Dialog) { curModal = d }
func unregisterModal()            { curModal = nil }

// ---------- generic dialog helpers ----------

func entText(e *gtk.Entry) string {
	if e == nil {
		return ""
	}
	s, err := e.GetText()
	if err != nil {
		return ""
	}
	return s
}

func tvText(t *gtk.TextView) string {
	if t == nil {
		return ""
	}
	buf, err := t.GetBuffer()
	if err != nil {
		return ""
	}
	s, err := buf.GetText(buf.GetStartIter(), buf.GetEndIter(), false)
	if err != nil {
		return ""
	}
	return s
}

func tvSetText(t *gtk.TextView, s string) {
	if t == nil {
		return
	}
	buf, err := t.GetBuffer()
	if err != nil {
		return
	}
	buf.SetText(s)
}

// tvSet — размер и перенос строк TextView (высота в px).
func tvSet(t *gtk.TextView, height int) {
	t.SetWrapMode(gtk.WRAP_WORD_CHAR)
	t.SetSizeRequest(-1, height)
}

// msgDialog shows a modal message dialog, returns response code.
func msgDialog(parent *gtk.Window, typ gtk.DialogFlags, mtype gtk.MessageType, buttons gtk.ButtonsType, text string) (gtk.ResponseType, *gtk.MessageDialog) {
	var parentWin gtk.IWindow
	if parent != nil {
		parentWin = parent
	}
	d := gtk.MessageDialogNew(parentWin, typ, mtype, buttons, "%s", text)
	if d == nil {
		return gtk.RESPONSE_CLOSE, nil
	}
	if parent != nil {
		d.SetTransientFor(parent)
	}
	resp := d.Run()
	return resp, d
}

func infoDialog(parent *gtk.Window, text string) {
	if parent == nil && app != nil {
		parent = app.window
	}
	r, d := msgDialog(parent, gtk.DIALOG_MODAL, gtk.MESSAGE_INFO, gtk.BUTTONS_OK, text)
	if d != nil {
		d.Destroy()
	}
	_ = r
}

func warnDialog(parent *gtk.Window, text string) {
	if parent == nil && app != nil {
		parent = app.window
	}
	r, d := msgDialog(parent, gtk.DIALOG_MODAL, gtk.MESSAGE_WARNING, gtk.BUTTONS_OK, text)
	if d != nil {
		d.Destroy()
	}
	_ = r
}

func errDialog(parent *gtk.Window, text string) {
	if parent == nil && app != nil {
		parent = app.window
	}
	r, d := msgDialog(parent, gtk.DIALOG_MODAL, gtk.MESSAGE_ERROR, gtk.BUTTONS_OK, text)
	if d != nil {
		d.Destroy()
	}
	_ = r
}

// confirmDialog shows a yes/no question; true if accepted.
func confirmDialog(parent *gtk.Window, text string) bool {
	if parent == nil && app != nil {
		parent = app.window
	}
	r, d := msgDialog(parent, gtk.DIALOG_MODAL, gtk.MESSAGE_QUESTION, gtk.BUTTONS_YES_NO, text)
	yes := r == gtk.RESPONSE_YES
	if d != nil {
		d.Destroy()
	}
	return yes
}

// confirmDanger shows a destructive-action confirmation with host count.
func confirmDanger(parent *gtk.Window, action string, hosts int) bool {
	if !store.State.Settings.ConfirmDanger {
		return true
	}
	text := fmt.Sprintf("⚠ Опасная операция: %s\nЗатронет хостов: %d\n\nПродолжить?", action, hosts)
	return confirmDialog(parent, text)
}

// confirmHostKey is the UI callback for host key changes.
func confirmHostKeyChanged(hc *hostKeyChange) bool {
	ch := make(chan bool, 1)
	glib.IdleAdd(func() bool {
		text := fmt.Sprintf(
			"⚠ КЛЮЧ ХОСТА ИЗМЕНИЛСЯ!\n\nХост: %s\nСтарый отпечаток:\n%s\nНовый отпечаток:\n%s\n\n"+
				"Это может указывать на MITM-атаку, либо на переустановку сервера.\n\n"+
				"Заменить ключ и продолжить?", hc.Host, hc.OldFP, hc.NewFP)
		ch <- confirmDialog(app.window, text)
		return false
	})
	select {
	case v := <-ch:
		return v
	case <-time.After(120 * time.Second):
		return false
	}
}

// fileChooser helper. Returns chosen path or "".
func fileChooser(parent *gtk.Window, title string, action gtk.FileChooserAction, saveName string) string {
	return fileChooserFiltered(parent, title, action, saveName, nil)
}

// fileChooserFiltered is fileChooser with optional pattern filters
// (e.g. "*.ini"); an «Все файлы» filter is always appended.
func fileChooserFiltered(parent *gtk.Window, title string, action gtk.FileChooserAction, saveName string, patterns []string) string {
	var parentWin gtk.IWindow
	if parent != nil {
		parentWin = parent
	}
	var fc *gtk.FileChooserDialog
	var err error
	if action == gtk.FILE_CHOOSER_ACTION_SAVE {
		fc, err = gtk.FileChooserDialogNewWith2Buttons(title, parentWin, action,
			"Сохранить", gtk.RESPONSE_ACCEPT, "Отмена", gtk.RESPONSE_CANCEL)
	} else {
		fc, err = gtk.FileChooserDialogNewWith2Buttons(title, parentWin, action,
			"Открыть", gtk.RESPONSE_ACCEPT, "Отмена", gtk.RESPONSE_CANCEL)
	}
	if err != nil || fc == nil {
		return ""
	}
	if saveName != "" {
		fc.SetCurrentName(saveName)
	}
	if len(patterns) > 0 {
		flt, ferr := gtk.FileFilterNew()
		if ferr == nil && flt != nil {
			flt.SetName("Хосты и inventory (ini, txt, csv, yaml…)")
			for _, p := range patterns {
				flt.AddPattern(p)
			}
			fc.AddFilter(flt)
		}
		if all, aerr := gtk.FileFilterNew(); aerr == nil && all != nil {
			all.SetName("Все файлы")
			all.AddPattern("*")
			fc.AddFilter(all)
		}
	}
	defer fc.Destroy()
	if fc.Run() == gtk.RESPONSE_ACCEPT {
		return fc.GetFilename()
	}
	return ""
}

// entryDialog prompts for a single-line value.
func entryDialog(parent *gtk.Window, title, placeholder, def string) (string, bool) {
	d, err := gtk.DialogNewWithButtons(title, parent, gtk.DIALOG_MODAL,
		[]interface{}{"OK", gtk.RESPONSE_ACCEPT},
		[]interface{}{"Отмена", gtk.RESPONSE_CANCEL},
	)
	if err != nil || d == nil {
		return "", false
	}
	defer d.Destroy()
	d.SetDefaultSize(380, 90)
	box, _ := d.GetContentArea()
	e, _ := gtk.EntryNew()
	e.SetPlaceholderText(placeholder)
	e.SetText(def)
	box.Add(e)
	box.ShowAll()
	e.Connect("activate", func() { d.Response(gtk.RESPONSE_ACCEPT) })
	if d.Run() == gtk.RESPONSE_ACCEPT {
		return entText(e), true
	}
	return "", false
}

// textViewDialog — просмотр/правка текстового файла (F3/F4 в SFTP).
// editable=false — только просмотр; возвращает (новый текст, «Сохранить»?).
func textViewDialog(parent *gtk.Window, title, content string, editable bool) (string, bool) {
	var d *gtk.Dialog
	var err error
	if editable {
		d, err = gtk.DialogNewWithButtons(title, parent, gtk.DIALOG_MODAL,
			[]interface{}{"Сохранить", gtk.RESPONSE_ACCEPT},
			[]interface{}{"Закрыть без сохранения", gtk.RESPONSE_CLOSE},
		)
	} else {
		d, err = gtk.DialogNewWithButtons(title, parent, gtk.DIALOG_MODAL,
			[]interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
		)
	}
	if err != nil || d == nil {
		return "", false
	}
	defer d.Destroy()
	d.SetDefaultSize(760, 560)
	box, _ := d.GetContentArea()
	box.SetMarginStart(8)
	box.SetMarginEnd(8)
	box.SetMarginTop(6)

	tv, _ := gtk.TextViewNew()
	tv.SetMonospace(true)
	tv.SetEditable(editable)
	tv.SetCursorVisible(editable)
	tv.SetWrapMode(gtk.WRAP_WORD_CHAR)
	tv.SetLeftMargin(8)
	tv.SetRightMargin(8)
	tv.SetTopMargin(6)
	buf, _ := tv.GetBuffer()
	buf.SetText(content)
	sc, _ := gtk.ScrolledWindowNew(nil, nil)
	sc.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	sc.Add(tv)
	sc.SetVExpand(true)
	box.PackStart(sc, true, true, 0)
	hint := labelNew("Ctrl+S — сохранить · оригинал сохраняется в .ra_bak_…")
	if !editable {
		hint = labelNew("Режим просмотра (только чтение)")
	}
	addClass(hint, "ramuted")
	hint.SetXAlign(0)
	box.PackStart(hint, false, false, 2)
	box.ShowAll()

	// Ctrl+S сохраняет
	tv.Connect("key-press-event", func(_ *gtk.TextView, ev *gdk.Event) bool {
		ke := gdk.EventKeyNewFromEvent(ev)
		if editable && ke.State()&gdk.CONTROL_MASK != 0 && ke.KeyVal() == gdk.KEY_s {
			d.Response(gtk.RESPONSE_ACCEPT)
			return true
		}
		return false
	})

	resp := d.Run()
	if editable && resp == gtk.RESPONSE_ACCEPT {
		s, _ := buf.GetText(buf.GetStartIter(), buf.GetEndIter(), false)
		return s, true
	}
	return "", false
}

// showAboutDialog — окно «О программе».
func showAboutDialog() {
	d, err := gtk.DialogNewWithButtons("О программе", app.window, gtk.DIALOG_MODAL,
		[]interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
	)
	if err != nil || d == nil {
		return
	}
	defer d.Destroy()
	defer unregisterModal()
	registerModal(d)
	d.SetDefaultSize(460, 430)
	box, _ := d.GetContentArea()
	box.SetMarginStart(18)
	box.SetMarginEnd(18)
	box.SetMarginTop(14)

	logo := labelNew("RA")
	addClass(logo, "logochip")
	logo.SetHAlign(gtk.ALIGN_CENTER)
	logo.SetSizeRequest(64, -1)
	t1 := labelNew("")
	t1.SetMarkup("<span size=\"x-large\" weight=\"bold\">RemoteAdmin</span>\n<span foreground=\"#8b93a5\">версия " + AppVersion + " · РЕД ОС 8</span>")
	t1.SetJustify(gtk.JUSTIFY_CENTER)
	desc := labelNew("Массовое управление хостами по SSH:\nплейбуки, шаблоны, цепочки, мониторинг,\nдвухпанельный SFTP и мульти-терминал.")
	addClass(desc, "ramuted")
	desc.SetJustify(gtk.JUSTIFY_CENTER)

	head := boxNew(gtk.ORIENTATION_VERTICAL, 6)
	head.PackStart(logo, false, false, 0)
	head.PackStart(t1, false, false, 0)
	head.PackStart(desc, false, false, 0)
	box.PackStart(head, false, false, 8)

	sep, _ := gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL)
	box.PackStart(sep, false, false, 4)

	keys := labelNew(
		"Ctrl+F — поиск хостов          Ctrl+1…8 — разделы\n" +
			"F3/F4 — просмотр/правка файла   F5 — копия · F6 — имя\n" +
			"F7 — каталог · F8 — удаление    Delete — удалить хост\n" +
			"↑↓ — история команд             Двойной клик — изменить хост")
	addClass(keys, "ramuted")
	keys.SetXAlign(0)
	box.PackStart(keys, false, false, 4)

	tech := labelNew("")
	tech.SetMarkup("<span size=\"small\" foreground=\"#8b93a5\">Go " + goVersion() + " · GTK3 · SSH/SFTP · пароли AES-256-GCM\n© 2026 RemoteAdmin · MIT</span>")
	tech.SetJustify(gtk.JUSTIFY_CENTER)
	box.PackStart(tech, false, false, 6)
	box.ShowAll()
	d.Run()
}

// goVersion — версия Go, на котором собрано приложение.
func goVersion() string {
	return strings.TrimSpace(strings.TrimPrefix(runtime.Version(), "go"))
}

// captureDialogs — автотестовый обход модальных окон: настройки → о программе.
// Флаг --shot-dialogs=DIR: снимки снимаются, пока диалог открыт во вложенном
// цикле d.Run(), закрывается программно через Response.
func captureDialogs(dir string) {
	glibIdle(showSettingsDialog)
	glibTimeout(900, func() bool {
		captureModalShot(dir, "settings")
		glibTimeout(200, func() bool {
			d := dir
			glibIdle(func() { captureDialogsStep2(d) })
			return false
		})
		return false
	})
}

func captureDialogsStep2(dir string) {
	glibIdle(showAboutDialog)
	glibTimeout(900, func() bool {
		captureModalShot(dir, "about")
		glibTimeout(200, func() bool {
			gtk.MainQuit()
			return false
		})
		return false
	})
}

// captureModalShot снимает окно вместе с открытым ДИАЛОГОМ (диалог —
// отдельное toplevel-окно, поэтому снимаем корневое окно экрана по
// координатам окна приложения) и закрывает диалог.
func captureModalShot(dir, name string) {
	w, h := app.window.GetAllocatedWidth(), app.window.GetAllocatedHeight()
	wx, wy := app.window.GetPosition()
	scr := app.window.GetScreen()
	if scr != nil {
		if root, rerr := scr.GetRootWindow(); rerr == nil && root != nil {
			if pb, perr := root.PixbufGetFromWindow(wx, wy, w, h); perr == nil && pb != nil {
				fn := dir + "/dialog_" + name + ".png"
				if serr := pb.SavePNG(fn, 4); serr == nil {
					fmt.Println("shot:", fn)
				} else {
					fmt.Fprintln(os.Stderr, "shot:", serr)
				}
			}
		}
	}
	if curModal != nil {
		curModal.Response(gtk.RESPONSE_CANCEL)
	}
}
