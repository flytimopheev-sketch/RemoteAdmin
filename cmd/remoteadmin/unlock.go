package main

import (
	"github.com/gotk3/gotk3/gtk"
)

// unlockDialog — ввод мастер-пароля при старте, если state.json
// зашифрован целиком. Пользователь может повторять попытки; «Отмена»
// завершает приложение (работать с зашифрованным хранилищем без пароля
// нельзя — там и доступы SSH, и журналы).
func unlockDialog() bool {
	for {
		d, err := gtk.DialogNewWithButtons(
			"Хранилище защищено", nil, gtk.DIALOG_MODAL,
			[]interface{}{"Разблокировать", gtk.RESPONSE_ACCEPT},
			[]interface{}{"Выход", gtk.RESPONSE_CLOSE},
		)
		if err != nil {
			return false
		}
		registerModal(d)
		d.SetDefaultSize(460, 170)
		box, _ := d.GetContentArea()
		box.SetMarginStart(14)
		box.SetMarginEnd(14)
		box.SetMarginTop(10)
		box.SetMarginBottom(6)

		lbl := labelNew("Файл конфигурации (state.json) зашифрован мастер-паролем.\nВведите его, чтобы открыть данные.")
		lbl.SetLineWrap(true)
		box.PackStart(lbl, false, false, 6)

		e, _ := gtk.EntryNew()
		e.SetVisibility(false)
		e.SetPlaceholderText("Мастер-пароль")
		e.SetActivatesDefault(true)
		box.PackStart(e, false, false, 4)
		box.ShowAll()
		e.GrabFocus()

		resp := d.Run()
		val := entText(e)
		d.Destroy()
		unregisterModal()
		if resp != gtk.RESPONSE_ACCEPT {
			return false
		}
		if err := tryUnlockStore(val); err == nil {
			logf("ok", "🔓 Хранилище разблокировано")
			return true
		}
		errDialog(nil, "Неверный мастер-пароль. Попробуйте ещё раз.")
	}
}

// promptMasterEnable — диалог включения мастер-шифрования из настроек.
func promptMasterEnable() {
	d1, ok := passwordDialog("Мастер-пароль", "Придумайте мастер-пароль (минимум 4 символа).\nОн потребуется при каждом запуске RemoteAdmin.")
	if !ok {
		return
	}
	d2, ok2 := passwordDialog("Повторите пароль", "Введите тот же пароль ещё раз для проверки.")
	if !ok2 {
		return
	}
	if d1 != d2 {
		errDialog(app.window, "Пароли не совпадают")
		return
	}
	if err := enableMasterEncryption(d1); err != nil {
		errDialog(app.window, "Не удалось включить шифрование: "+err.Error())
		return
	}
	logf("ok", "🔒 Мастер-шифрование state.json включено")
}

// promptMasterDisable — диалог выключения мастер-шифрования.
func promptMasterDisable() {
	pw, ok := passwordDialog("Выключить мастер-шифрование",
		"Введите текущий мастер-пароль. Хранилище будет расшифровано\n(пароли останутся зашифрованы ключом машины).")
	if !ok {
		return
	}
	if err := disableMasterEncryption(pw); err != nil {
		errDialog(app.window, "Ошибка: "+err.Error())
		return
	}
	logf("warn", "🔓 Мастер-шифрование state.json выключено")
}

// passwordDialog — модальный ввод пароля; возвращает (значение, ok).
func passwordDialog(title, text string) (string, bool) {
	d, err := gtk.DialogNewWithButtons(
		title, app.window, gtk.DIALOG_MODAL,
		[]interface{}{"ОК", gtk.RESPONSE_ACCEPT},
		[]interface{}{"Отмена", gtk.RESPONSE_CANCEL},
	)
	if err != nil {
		return "", false
	}
	defer d.Destroy()
	defer unregisterModal()
	registerModal(d)
	d.SetDefaultSize(460, 170)
	box, _ := d.GetContentArea()
	box.SetMarginStart(14)
	box.SetMarginEnd(14)
	box.SetMarginTop(10)
	box.SetMarginBottom(6)
	lbl := labelNew(text)
	lbl.SetLineWrap(true)
	box.PackStart(lbl, false, false, 6)
	e, _ := gtk.EntryNew()
	e.SetVisibility(false)
	e.SetActivatesDefault(true)
	box.PackStart(e, false, false, 4)
	box.ShowAll()
	e.GrabFocus()
	resp := d.Run()
	return entText(e), resp == gtk.RESPONSE_ACCEPT
}
