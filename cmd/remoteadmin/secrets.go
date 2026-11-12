package main

// secrets.go — встроенный менеджер секретов (v1.8.0).
//
// Зачем: пароли (su/root, базы данных, API-токены) не должны лежать
// открытым текстом ни в плейбуке, ни в журнале. Секреты хранятся в
// state.json в виде "enc:v1:..." (AES-256-GCM, ключ машины — файл
// secret.key с правами 0600); при включённом мастер-пароле шифруется
// весь файл целиком. В задачах плейбука секрет подставляется по ссылке
// {{ secret:имя }} только в момент выполнения; в журнале и отчётах
// любое значение секрета (а также SSH/root-пароли) маскируется "•••".

import (
        "fmt"
        "regexp"
        "sort"
        "strings"
        "sync"

        "github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// ---------- хранилище секретов ----------

var (
        secretMu    sync.RWMutex
        secretCache map[string]string // расшифрованные значения для маскировки вывода
)

func secretNameValid(name string) bool {
        if name == "" || len(name) > 64 {
                return false
        }
        for _, r := range name {
                if !(r == '_' || r == '-' || r == '.' ||
                        (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
                        return false
                }
        }
        return true
}

// SecretSet сохраняет секрет в зашифрованном виде и обновляет кэш маскировки.
func (s *StateStore) SecretSet(name, value string) error {
        if !secretNameValid(name) {
                return fmt.Errorf("имя секрета: латиница, цифры, _ - . (до 64 символов)")
        }
        enc, err := encryptString(value)
        if err != nil {
                return err
        }
        s.mu.Lock()
        if s.State.Secrets == nil {
                s.State.Secrets = map[string]string{}
        }
        s.State.Secrets[name] = enc
        s.mu.Unlock()
        secretMu.Lock()
        if secretCache == nil {
                secretCache = map[string]string{}
        }
        secretCache[name] = value
        secretMu.Unlock()
        return nil
}

// SecretDelete удаляет секрет.
func (s *StateStore) SecretDelete(name string) {
        s.mu.Lock()
        delete(s.State.Secrets, name)
        s.mu.Unlock()
        secretMu.Lock()
        delete(secretCache, name)
        secretMu.Unlock()
}

// SecretValue расшифровывает секрет ("" — не найден).
func (s *StateStore) SecretValue(name string) string {
        s.mu.Lock()
        enc, ok := s.State.Secrets[name]
        s.mu.Unlock()
        if !ok {
                return ""
        }
        v, err := decryptString(enc)
        if err != nil {
                return ""
        }
        return v
}

// SecretNames возвращает отсортированный список имён секретов.
func (s *StateStore) SecretNames() []string {
        s.mu.Lock()
        defer s.mu.Unlock()
        out := make([]string, 0, len(s.State.Secrets))
        for n := range s.State.Secrets {
                out = append(out, n)
        }
        sort.Strings(out)
        return out
}

// secretValuesForMask возвращает все значения, которые нельзя показывать:
// секреты + SSH-пароль + root-пароль + пароль ключа.
func secretValuesForMask() []string {
        secretMu.RLock()
        vals := make([]string, 0, len(secretCache))
        for _, v := range secretCache {
                if v != "" {
                        vals = append(vals, v)
                }
        }
        secretMu.RUnlock()
        // «секретное» значение может быть добавлено в этой сессии из другого
        // экземпляра приложения — на всякий случай достаём из хранилища
        for _, n := range store.SecretNames() {
                if v := store.SecretValue(n); v != "" {
                        vals = append(vals, v)
                }
        }
        for _, v := range []string{store.Password(), store.RootPassword(), store.KeyPassphrase()} {
                if v != "" {
                        vals = append(vals, v)
                }
        }
        return vals
}

const maskStr = "••••••"

// redactSecrets маскирует значения секретов и паролей в тексте вывода.
// Вызывается на границе вывода: журнал, отчёты, LastOutput.
func redactSecrets(s string) string {
        if s == "" {
                return s
        }
        for _, v := range secretValuesForMask() {
                if len(v) < 3 { // слишком короткие значения не маскируем (ложные срабатывания)
                        continue
                }
                s = strings.ReplaceAll(s, v, maskStr)
        }
        return s
}

// ---------- подстановка {{ secret:имя }} ----------

// expandSecretRefs заменяет {{ secret:имя }} на значение секрета.
// Вызывается из expandVars (state.go) ПОСЛЕ обычной подстановки переменных.
func (s *StateStore) expandSecretRefs(ip, text string) string {
        if !strings.Contains(text, "{{ secret:") {
                return text
        }
        return secretRefRe.ReplaceAllStringFunc(text, func(m string) string {
                name := strings.TrimPrefix(strings.Trim(m, "{} \t"), "secret:")
                if v := s.SecretValue(name); v != "" {
                        return v
                }
                logf("warn", "🔐 Секрет «%s» не найден — подставлена маска (меню «Секреты…»)", name)
                return "***"
        })
}

var secretRefRe = regexp.MustCompile(`\{\{\s*secret:[^{}\s]+\s*\}\}`)

// ---------- диалог менеджера секретов ----------

// showSecretsManager — окно управления секретами (список, добавление,
// изменение, удаление). Значения показываются маской; при вводе можно
// включить отображение.
func showSecretsManager() {
        d, err := gtk.DialogNewWithButtons(
                "Менеджер секретов",
                app.window, gtk.DIALOG_MODAL,
                []interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
        )
        if err != nil {
                return
        }
        defer d.Destroy()
        defer unregisterModal()
        registerModal(d)
        d.SetDefaultSize(560, 380)

        box, _ := d.GetContentArea()
        box.SetMarginStart(14)
        box.SetMarginEnd(14)
        box.SetMarginTop(10)
        box.SetMarginBottom(6)

        info := labelNew("Секреты подставляются в команды и скрипты по ссылке {{ secret:имя }}.\n" +
                "Значения шифруются AES-256-GCM и никогда не попадают в журнал открытым текстом.")
        info.SetXAlign(0)
        info.SetLineWrap(true)
        addClass(info, "ramuted")
        box.PackStart(info, false, false, 4)

        tree, st := makeTree([]string{"Имя", "Значение"}, []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING})
        fill := func() {
                st.Clear()
                for _, n := range store.SecretNames() {
                        it := st.Append()
                        _ = st.Set(it, []int{0, 1}, []interface{}{n, maskStr})
                }
        }
        fill()
        sc := scrolled(tree, 0)
        sc.SetVExpand(true)
        box.PackStart(sc, true, true, 0)

        btns := boxNew(gtk.ORIENTATION_HORIZONTAL, 6)
        addBtn := func(lbl string, fn func()) *gtk.Button {
                b := buttonNew(lbl)
                b.Connect("clicked", fn)
                btns.PackStart(b, false, false, 0)
                return b
        }
        selName := func() string {
                m, iter, ok := selIter(tree)
                if !ok {
                        return ""
                }
                return iterGetString(m, iter, 0)
        }
        addBtn("➕ Добавить", func() {
                name, ok := entryDialog(app.window, "Новый секрет", "Имя (латиница, цифры, _ - .)", "")
                if !ok {
                        return
                }
                name = strings.TrimSpace(name)
                if !secretNameValid(name) {
                        errDialog(app.window, "Некорректное имя секрета")
                        return
                }
                if store.SecretValue(name) != "" {
                        if !confirmDialog(app.window, "Секрет «"+name+"» уже существует. Заменить значение?") {
                                return
                        }
                }
                val, ok := secretValueDialog(d, name)
                if !ok {
                        return
                }
                if err := store.SecretSet(name, val); err != nil {
                        errDialog(app.window, err.Error())
                        return
                }
                _ = store.Save()
                fill()
                logf("ok", "🔐 Секрет «%s» сохранён (AES-256-GCM)", name)
        })
        addBtn("✏️ Изменить", func() {
                name := selName()
                if name == "" {
                        warnDialog(app.window, "Выберите секрет в списке")
                        return
                }
                val, ok := secretValueDialog(d, name)
                if !ok {
                        return
                }
                if err := store.SecretSet(name, val); err != nil {
                        errDialog(app.window, err.Error())
                        return
                }
                _ = store.Save()
                fill()
                logf("ok", "🔐 Секрет «%s» обновлён", name)
        })
        addBtn("🗑 Удалить", func() {
                name := selName()
                if name == "" {
                        warnDialog(app.window, "Выберите секрет в списке")
                        return
                }
                if !confirmDialog(app.window, "Удалить секрет «"+name+"»?\nСсылки {{ secret:"+name+" }} в задачах перестанут раскрываться.") {
                        return
                }
                store.SecretDelete(name)
                _ = store.Save()
                fill()
                logf("warn", "🗑 Секрет «%s» удалён", name)
        })
        box.PackStart(btns, false, false, 4)

        box.ShowAll()
        d.Run()
}

// secretValueDialog — ввод значения секрета с переключателем видимости.
func secretValueDialog(parent *gtk.Dialog, name string) (string, bool) {
        dd, err := gtk.DialogNewWithButtons(
                "Значение секрета «"+name+"»",
                parent, gtk.DIALOG_MODAL,
                []interface{}{"Сохранить", gtk.RESPONSE_ACCEPT},
                []interface{}{"Отмена", gtk.RESPONSE_CANCEL},
        )
        if err != nil {
                return "", false
        }
        defer dd.Destroy()
        defer unregisterModal()
        registerModal(dd)
        dd.SetDefaultSize(460, 170)

        box, _ := dd.GetContentArea()
        box.SetMarginStart(14)
        box.SetMarginEnd(14)
        box.SetMarginTop(10)

        e, _ := gtk.EntryNew()
        e.SetVisibility(false) // по умолчанию скрыто
        e.SetInputPurpose(gtk.INPUT_PURPOSE_PASSWORD)
        box.PackStart(labelNew("Значение (хранится только в зашифрованном виде):"), false, false, 4)
        box.PackStart(e, false, false, 4)
        ch, _ := gtk.CheckButtonNewWithLabel("Показать вводимое значение")
        ch.Connect("toggled", func() { e.SetVisibility(ch.GetActive()) })
        box.PackStart(ch, false, false, 4)
        box.ShowAll()

        if dd.Run() == gtk.RESPONSE_ACCEPT {
                return entText(e), true
        }
        return "", false
}
