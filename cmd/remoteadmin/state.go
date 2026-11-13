package main

import (
        "encoding/json"
        "errors"
        "fmt"
        "os"
        "path/filepath"
        "regexp"
        "strconv"
        "strings"
        "sync"
        "time"
)

// ---------- shared data model ----------

type HostEntry struct {
        IP     string   `json:"ip"`
        Name   string   `json:"name,omitempty"` // пользовательское имя («web-1»); пусто = показываем IP
        Group  string   `json:"group"`
        Status string   `json:"status,omitempty"` // "", "ok", "fail", "warn", "run"
        Tags   []string `json:"tags,omitempty"`   // метки для фильтрации («prod», «web»)
        Fav    bool     `json:"fav,omitempty"`    // избранное (звёздочка)
        Env    string   `json:"env,omitempty"`    // среда: "", "Тест", "Стейдж", "Продакшен" — цветной бейдж рядом с хостом
}

// hostEnvs — список сред окружения: используется в редакторе хоста,
// в списках и в подтверждениях запуска.
var hostEnvs = []string{"", "Тест", "Стейдж", "Продакшен"}

// envColor — цвет бейджа среды (списки, строка цели, сводки запуска).
func envColor(env string) string {
        switch env {
        case "Продакшен":
                return "#ef4444" // красный — боевые серверы, осторожно
        case "Стейдж":
                return "#f59e0b" // янтарь
        case "Тест":
                return "#10b981" // зелёный — можно экспериментировать
        }
        return ""
}

// envBadge — Pango-markup бейджа среды для списков.
func envBadge(env string) string {
        if env == "" {
                return ""
        }
        return " <span background='" + envColor(env) + "' foreground='#0b0e14'> " + env + " </span>"
}

// DisplayName возвращает имя хоста, а если оно не задано — IP.
func (h HostEntry) DisplayName() string {
        if s := strings.TrimSpace(h.Name); s != "" {
                return s
        }
        return h.IP
}

type PlaybookTask struct {
        Type           string   `json:"type"`             // command|script|copy|replace|replace_with_cmds|edit|service|package
        Source         string   `json:"source,omitempty"` // команда / путь / имя службы / список пакетов
        Action         string   `json:"action,omitempty"` // действие для service/package (restart|install|…)
        Dest           string   `json:"dest,omitempty"`
        Search         string   `json:"search,omitempty"`
        Replace        string   `json:"replace,omitempty"`
        RestartService string   `json:"restart_service,omitempty"`
        PostCommands   []string `json:"post_commands,omitempty"`
        Root           bool     `json:"root,omitempty"`
        RunIf          string   `json:"run_if,omitempty"` // always | on_success | on_failure | rc:N

        // --- управление выполнением (v1.9.0) ---
        Name          string `json:"name,omitempty"`            // осмысленное название шага («Собрать SMART-показатели»)
        TimeoutSec    int    `json:"timeout_sec,omitempty"`    // 0 = общий ExecTimeout
        AllowRetry    bool   `json:"allow_retry,omitempty"`    // «Разрешён повторный запуск»
        RetryCount    int    `json:"retry_count,omitempty"`    // авто-повторы (0 = без повторов)
        RetryDelaySec int    `json:"retry_delay_sec,omitempty"`
}

// StepTitle — имя шага; если не задано — обычное описание.
func (t PlaybookTask) StepTitle() string {
        if s := strings.TrimSpace(t.Name); s != "" {
                return s
        }
        return t.Describe()
}

// stepCommand — рабочая строка шага (вторая строка, моноширинно).
func (t PlaybookTask) stepCommand() string {
        switch t.Type {
        case "command":
                return t.Source
        case "script":
                return "sh " + filepath.Base(t.Source)
        case "copy":
                return filepath.Base(t.Source) + " → " + t.Dest
        case "replace":
                return filepath.Base(t.Source) + " → " + t.Dest
        case "replace_with_cmds":
                s := filepath.Base(t.Source) + " → " + t.Dest
                if len(t.PostCommands) > 0 {
                        s += fmt.Sprintf(" + %d команд", len(t.PostCommands))
                }
                return s
        case "edit":
                return "sed " + t.Source + " (" + t.Search + " → " + t.Replace + ")"
        case "service":
                act := t.Action
                if act == "" {
                        act = "restart"
                }
                return "systemctl " + act + " " + t.Source
        case "package":
                act := t.Action
                if act == "" {
                        act = "install"
                }
                return act + " " + t.Source
        }
        return t.Source
}

// runIfAllows проверяет, разрешено ли запускать шаг с условием v после
// предыдущего шага (prevErr — был ли сбой, prevRC — код возврата).
func runIfAllows(v string, prevErr bool, prevRC int) bool {
        switch v {
        case "", "always":
                return true
        case "on_success":
                return !prevErr
        case "on_failure":
                return prevErr
        }
        if strings.HasPrefix(v, "rc:") {
                want, err := strconv.Atoi(strings.TrimPrefix(v, "rc:"))
                if err != nil {
                        return true
                }
                rc := prevRC
                if prevErr && rc == 0 {
                        rc = 1
                }
                return rc == want
        }
        return true
}

// errPolicyRu — человеческое имя политики ошибки.
func errPolicyRu(v string) string {
        switch v {
        case "continue":
                return "продолжить"
        case "ask":
                return "спросить"
        default:
                return "остановиться"
        }
}

// effectiveErrPolicy — политика ошибки с учётом старой галочки «Прервать при ошибке».
func (s *State) effectiveErrPolicy() string {
        if s.PlaybookErrPolicy != "" {
                return s.PlaybookErrPolicy
        }
        if s.PlaybookContinueOnError {
                return "continue"
        }
        return "stop"
}

// effectiveParallel — лимит параллелизма плейбука.
func (s *State) effectiveParallel() int {
        if s.PlaybookParallel > 0 {
                return s.PlaybookParallel
        }
        if s.Settings.MaxParallel > 0 {
                return s.Settings.MaxParallel
        }
        return 8
}

// runIfRu — русское имя условия выполнения задачи.
func runIfRu(v string) string {
        switch v {
        case "on_success":
                return "⏯ если предыдущая успешна"
        case "on_failure":
                return "⏯ если предыдущая упала"
        }
        if strings.HasPrefix(v, "rc:") {
                return "⏯ при коде возврата " + strings.TrimPrefix(v, "rc:")
        }
        return ""
}

// Describe — короткая строка для таблицы (без переносов).
func (t PlaybookTask) Describe() string {
        full := t.DescribeFull()
        if len(full) > 72 {
                full = full[:72] + "…"
        }
        return full
}

// DescribeFull — полное описание задачи (для всплывающей подсказки в таблице):
// команда не обрезается, виден весь текст.
func (t PlaybookTask) DescribeFull() string {
        pre := runIfRu(t.RunIf)
        if pre != "" {
                pre += "  "
        }
        switch t.Type {
        case "command":
                return pre + "▶ Команда: " + strings.TrimSpace(t.Source)
        case "script":
                return pre + "📜 Скрипт: " + filepath.Base(t.Source)
        case "copy":
                return pre + "📋 Копировать: " + filepath.Base(t.Source) + " → " + t.Dest
        case "replace":
                return pre + "🔄 Заменить файл: " + filepath.Base(t.Source) + " → " + t.Dest
        case "replace_with_cmds":
                s := pre + "📁 Заменить + команды: " + filepath.Base(t.Source) + " → " + t.Dest
                if len(t.PostCommands) > 0 {
                        s += "  |  " + t.PostCommands[0]
                        if len(t.PostCommands) > 1 {
                                s += fmt.Sprintf(" (+%d)", len(t.PostCommands)-1)
                        }
                }
                return s
        case "edit":
                s := pre + "✏️ Редактировать: " + t.Source
                if t.RestartService != "" {
                        s += " + restart " + t.RestartService
                }
                return s
        case "service":
                return pre + "⚙ Служба: " + serviceActionRu(t.Action) + " " + t.Source
        case "package":
                return pre + "📦 Пакеты: " + pkgActionRu(t.Action) + " " + t.Source
        }
        return "? " + t.Type
}

// isDangerous reports whether the task may break the system
// and requires a confirmation dialog.
func (t PlaybookTask) isDangerous() bool {
        switch t.Type {
        case "replace", "replace_with_cmds":
                return true
        case "edit":
                return t.RestartService != ""
        case "service":
                return t.Action == "stop" || t.Action == "disable"
        case "package":
                return t.Action == "remove"
        }
        return false
}

// serviceActionRu / pkgActionRu — русские подписи действий служб и пакетов.
func serviceActionRu(a string) string {
        switch a {
        case "start":
                return "запустить"
        case "stop":
                return "остановить"
        case "restart":
                return "перезапустить"
        case "reload":
                return "reload"
        case "status":
                return "статус"
        case "enable":
                return "в автозагрузку"
        case "disable":
                return "из автозагрузки"
        }
        if a == "" {
                return "перезапустить"
        }
        return a
}

func pkgActionRu(a string) string {
        switch a {
        case "install":
                return "установить"
        case "remove":
                return "удалить"
        case "upgrade":
                return "обновить все"
        case "info":
                return "инфо"
        }
        if a == "" {
                return "установить"
        }
        return a
}

type Template struct {
        Name        string         `json:"name"`
        Description string         `json:"description,omitempty"`
        Tasks       []PlaybookTask `json:"tasks"`
}

type ChainStep struct {
        Template  string   `json:"template"`
        OnSuccess string   `json:"on_success"` // continue | stop
        OnFailure string   `json:"on_failure"` // stop | continue | rollback
        Rollback  []string `json:"rollback,omitempty"`
}

func (c ChainStep) Describe(tplName string) string {
        r := "▶ " + tplName
        if c.OnFailure == "rollback" && len(c.Rollback) > 0 {
                r += " (rollback: " + c.Rollback[0] + ")"
        }
        return r
}

type Chain struct {
        Name  string      `json:"name"`
        Steps []ChainStep `json:"steps"`
}

type ReportEntry struct {
        Time   string `json:"time"`
        Task   string `json:"task"`
        Host   string `json:"host"`
        Status string `json:"status"` // OK | FAIL | WARN
        Output string `json:"output"`
}

type Settings struct {
        Theme           string  `json:"theme"`           // dark | light
        Opacity         float64 `json:"opacity"`         // 0.50..1.00
        ConnectTimeout  int     `json:"connect_timeout"` // sec
        ExecTimeout     int     `json:"exec_timeout"`    // sec
        MaxParallel     int     `json:"max_parallel"`    // workers
        Retries         int     `json:"retries"`
        StrictHostKey   string  `json:"strict_hostkey"` // ask | accept-new | no
        ConfirmDanger   bool    `json:"confirm_danger"`
        MassConfirmMin  int     `json:"mass_confirm_min"` // массовое изменение: подтверждение от N хостов (0 = выключено)
        MonitorInterval int     `json:"monitor_interval"` // sec
        Notifications   bool    `json:"notifications"`
        WebhookURL      string  `json:"webhook_url,omitempty"`
        NotifyFailOnly  bool    `json:"notify_fail_only,omitempty"`
        EmailSMTP       string  `json:"email_smtp,omitempty"` // host:port
        EmailSSL        bool    `json:"email_ssl,omitempty"`  // TLS сразу (порт 465)
        EmailUser       string  `json:"email_user,omitempty"`
        EmailPass       string  `json:"email_pass,omitempty"`
        EmailFrom       string  `json:"email_from,omitempty"`
        EmailTo         string  `json:"email_to,omitempty"`
}

func defaultSettings() Settings {
        return Settings{
                Theme:           "dark",
                Opacity:         0.85,
                ConnectTimeout:  10,
                ExecTimeout:     120,
                MaxParallel:     8,
                Retries:         2,
                StrictHostKey:   "ask",
                ConfirmDanger:   true,
                MassConfirmMin:  10,
                MonitorInterval: 5,
                Notifications:   true,
        }
}

type State struct {
        Version         int               `json:"version"`
        Hosts           []HostEntry       `json:"hosts"`
        Groups          []string          `json:"groups,omitempty"` // созданные пользователем группы (в т.ч. пустые)
        Playbook        []PlaybookTask    `json:"playbook"`
        Templates       []Template        `json:"templates"`
        PresetsSeeded   bool              `json:"presets_seeded,omitempty"`    // пресетные шаблоны уже добавлены
        PresetsSeededV2 bool              `json:"presets_seeded_v2,omitempty"` // вторая волна пресетов (v1.7.0)
        Chains          []Chain           `json:"chains"`
        Schedules       []Schedule        `json:"schedules,omitempty"` // планировщик задач
        Reports         []ReportEntry     `json:"reports"`
        Settings        Settings          `json:"settings"`
        Username        string            `json:"username"`
        Password        string            `json:"password,omitempty"`
        RootPass        string            `json:"root_password,omitempty"`
        KeyPath         string            `json:"key_path,omitempty"`
        KeyPass         string            `json:"key_passphrase,omitempty"`
        UseRoot         bool              `json:"use_root"`
        LastOutput      map[string]string `json:"last_output,omitempty"`
        Session         SessionState      `json:"session,omitempty"` // геометрия окна и выбор при последнем выходе

        // --- плейбук: цели, переменные, политика ошибок (v1.7.0) ---
        PlaybookHosts           []string                     `json:"playbook_hosts,omitempty"`             // выбранные хосты прямо в плейбуке
        PlaybookVars            map[string]string            `json:"playbook_vars,omitempty"`              // глобальные переменные {{ var }}
        PlaybookHostVars        map[string]map[string]string `json:"playbook_host_vars,omitempty"`         // переменные конкретных хостов
        PlaybookContinueOnError bool                         `json:"playbook_continue_on_error,omitempty"` // false = прервать при ошибке (по умолчанию)

        // --- менеджер секретов (v1.8.0): {{ secret:имя }}; значения AES-256-GCM ---
        Secrets map[string]string `json:"secrets,omitempty"`

        // --- плейбук v1.9.0: метаданные и управление выполнения ---
        PlaybookName        string `json:"playbook_name,omitempty"`           // название плейбука
        PlaybookDescription string `json:"playbook_description,omitempty"`     // описание
        PlaybookParallel      int    `json:"playbook_parallel,omitempty"`       // одновременные хосты (0 = из настроек)
        PlaybookRunTimeoutSec int    `json:"playbook_run_timeout_sec,omitempty"` // общий таймаут (0 = без лимита)
        PlaybookErrPolicy     string `json:"playbook_err_policy,omitempty"`     // stop | continue | ask
}

// SessionState — «последняя сессия»: запоминается при закрытии программы,
// восстанавливается при следующем запуске (размер/позиция окна, вкладка,
// выделенные хосты и выбранная группа).
type SessionState struct {
        W             int      `json:"w"`
        H             int      `json:"h"`
        X             int      `json:"x"`
        Y             int      `json:"y"`
        Maximized     bool     `json:"maximized,omitempty"`
        Nav           string   `json:"nav,omitempty"`
        SelectedHosts []string `json:"selected_hosts,omitempty"`
        Group         string   `json:"group,omitempty"`
        Paned         map[string]int `json:"paned,omitempty"` // положения разделителей панелей (делители можно таскать мышью)
}

// panedPos — сохранённое положение разделителя (0 = не сохранено).
func (s SessionState) panedPos(name string) int {
        if s.Paned == nil {
                return 0
        }
        return s.Paned[name]
}

// ---------- state manager ----------

type StateStore struct {
        mu    sync.Mutex
        path  string
        State State
}

var store *StateStore

func configDir() (string, error) {
        if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
                return filepath.Join(d, AppName), nil
        }
        home, err := os.UserHomeDir()
        if err != nil {
                return "", err
        }
        return filepath.Join(home, ".config", AppName), nil
}

// auditPath — путь к файлу журнала аудита (кто/когда/что/на каких хостах).
func auditPath() string {
        dir, err := configDir()
        if err != nil {
                return ""
        }
        return filepath.Join(dir, "audit.jsonl")
}

func initStateStore() error {
        dir, err := configDir()
        if err != nil {
                return err
        }
        if err := os.MkdirAll(dir, 0700); err != nil {
                return err
        }
        store = &StateStore{path: filepath.Join(dir, "state.json")}
        store.State.Version = 2
        store.State.Settings = defaultSettings()
        store.State.LastOutput = map[string]string{}
        store.load()
        return nil
}

func (s *StateStore) load() {
        data, err := os.ReadFile(s.path)
        if err != nil {
                return
        }
        // весь файл шифруется мастер-паролем: запоминаем сырые байты,
        // разблокировка — позже (после gtk.Init, диалогом ввода пароля)
        if stateFileIsEncrypted(data) {
                stateEncrypted = true
                pendingUnlock = true
                encryptedRaw = data
                return
        }
        s.applyLoadedJSON(data)
}

// applyLoadedJSON разбирает и применяет JSON состояния (уже расшифрованный).
func (s *StateStore) applyLoadedJSON(data []byte) {
        var st State
        if err := json.Unmarshal(data, &st); err != nil {
                // Legacy python remote_admin.py format: hosts stored as
                // []string "group:ip" — unmarshalling into []HostEntry fails,
                // so retry with the old shape and convert.
                var legacy struct {
                        Hosts    []string `json:"hosts"`
                        Username string   `json:"username,omitempty"`
                        Password string   `json:"password,omitempty"`
                        UseRoot  bool     `json:"use_root,omitempty"`
                }
                if err2 := json.Unmarshal(data, &legacy); err2 != nil || len(legacy.Hosts) == 0 {
                        return
                }
                st = State{Version: 2, Settings: defaultSettings(), LastOutput: map[string]string{}}
                for _, raw := range legacy.Hosts {
                        g, ip := splitLegacyHost(raw)
                        if strings.TrimSpace(ip) == "" {
                                continue
                        }
                        st.Hosts = append(st.Hosts, HostEntry{IP: ip, Group: g})
                }
                st.Username = legacy.Username
                st.Password = legacy.Password
                st.UseRoot = legacy.UseRoot
                s.State = st
                return
        }
        if st.Settings == (Settings{}) {
                st.Settings = defaultSettings()
        }
        if st.Version == 0 {
                st.Version = 2
        }
        if st.LastOutput == nil {
                st.LastOutput = map[string]string{}
        }
        if st.Groups == nil {
                st.Groups = []string{}
        }
        // migrate legacy format: hosts stored as []string "group:ip"
        var legacy struct {
                Hosts []json.RawMessage `json:"hosts"`
        }
        if err := json.Unmarshal(data, &legacy); err == nil {
                for i, raw := range legacy.Hosts {
                        var strv string
                        if json.Unmarshal(raw, &strv) == nil && i < len(st.Hosts) && st.Hosts[i].IP == "" {
                                g, ip := splitLegacyHost(strv)
                                st.Hosts[i] = HostEntry{IP: ip, Group: g}
                        }
                }
        }
        s.State = st
}

func splitLegacyHost(s string) (group, ip string) {
        for i := 0; i < len(s); i++ {
                if s[i] == ':' {
                        // group part must not look like ipv6
                        return s[:i], s[i+1:]
                }
        }
        return "Общие", s
}

// Save persists state atomically. Passwords are encrypted on disk;
// при включённом мастер-пароле шифруется весь файл.
func (s *StateStore) Save() error {
        s.mu.Lock()
        defer s.mu.Unlock()
        st := s.State // shallow copy
        // ensure secrets are encrypted (machine-key mode; при мастер-шифровании
        // это не нужно — но не мешает)
        if v, err := encryptString(decMust(st.Password)); err == nil {
                st.Password = v
        }
        if v, err := encryptString(decMust(st.RootPass)); err == nil {
                st.RootPass = v
        }
        if v, err := encryptString(decMust(st.KeyPass)); err == nil {
                st.KeyPass = v
        }
        if v, err := encryptString(decMust(st.Settings.EmailPass)); err == nil {
                st.Settings.EmailPass = v
        }
        data, err := json.MarshalIndent(&st, "", "  ")
        if err != nil {
                return err
        }
        if masterPass != "" {
                data, err = masterSeal(data, masterPass)
                if err != nil {
                        return fmt.Errorf("master seal: %w", err)
                }
        }
        tmp := s.path + ".tmp"
        if err := os.WriteFile(tmp, data, 0600); err != nil {
                return err
        }
        return os.Rename(tmp, s.path)
}

func decMust(v string) string {
        if v == "" {
                return ""
        }
        p, err := decryptString(v)
        if err != nil {
                return v
        }
        return p
}

// decrypted accessors
func (s *StateStore) Password() string      { return decMust(s.State.Password) }
func (s *StateStore) RootPassword() string  { return decMust(s.State.RootPass) }
func (s *StateStore) KeyPassphrase() string { return decMust(s.State.KeyPass) }
func (s *StateStore) EmailPass() string     { return decMust(s.State.Settings.EmailPass) }

func (s *StateStore) SetPassword(v string) {
        if v == "" {
                s.State.Password = ""
                return
        }
        if enc, err := encryptString(v); err == nil {
                s.State.Password = enc
        } else {
                s.State.Password = v
        }
}
func (s *StateStore) SetRootPassword(v string) {
        if v == "" {
                s.State.RootPass = ""
                return
        }
        if enc, err := encryptString(v); err == nil {
                s.State.RootPass = enc
        } else {
                s.State.RootPass = v
        }
}
func (s *StateStore) SetKeyPassphrase(v string) {
        if v == "" {
                s.State.KeyPass = ""
                return
        }
        if enc, err := encryptString(v); err == nil {
                s.State.KeyPass = enc
        } else {
                s.State.KeyPass = v
        }
}

func (s *StateStore) SetEmailPass(v string) {
        if v == "" {
                s.State.Settings.EmailPass = ""
                return
        }
        if enc, err := encryptString(v); err == nil {
                s.State.Settings.EmailPass = enc
        } else {
                s.State.Settings.EmailPass = v
        }
}

// ---------- hosts helpers ----------

func (s *StateStore) FindHost(ip string) *HostEntry {
        for i := range s.State.Hosts {
                if s.State.Hosts[i].IP == ip {
                        return &s.State.Hosts[i]
                }
        }
        return nil
}

func (s *StateStore) SetHostStatus(ip, status string) {
        s.mu.Lock()
        defer s.mu.Unlock()
        if h := s.FindHost(ip); h != nil {
                h.Status = status
        }
}

func (s *StateStore) RemoveHosts(ips []string) {
        out := s.State.Hosts[:0]
        for _, h := range s.State.Hosts {
                drop := false
                for _, ip := range ips {
                        if h.IP == ip {
                                drop = true
                                break
                        }
                }
                if !drop {
                        out = append(out, h)
                }
        }
        s.State.Hosts = out
}

func (s *StateStore) AddHost(ip, group string) error {
        return s.AddHostNamed(ip, "", group)
}

// AddHostNamed добавляет хост с пользовательским именем.
func (s *StateStore) AddHostNamed(ip, name, group string) error {
        if ip == "" {
                return errors.New("пустой адрес")
        }
        if s.FindHost(ip) != nil {
                return fmt.Errorf("хост %s уже есть", ip)
        }
        if group == "" {
                group = "Общие"
        }
        s.State.Hosts = append(s.State.Hosts, HostEntry{IP: ip, Name: strings.TrimSpace(name), Group: group})
        return nil
}

// UpdateHost меняет адрес/имя/группу существующего хоста.
// Возвращает ошибку, если новый адрес занят другим хостом.
func (s *StateStore) UpdateHost(oldIP, newIP, newName, newGroup string) error {
        newIP = strings.TrimSpace(newIP)
        if newIP == "" {
                return errors.New("пустой адрес")
        }
        h := s.FindHost(oldIP)
        if h == nil {
                return fmt.Errorf("хост %s не найден", oldIP)
        }
        if newIP != oldIP {
                if other := s.FindHost(newIP); other != nil && other != h {
                        return fmt.Errorf("хост %s уже есть в списке", newIP)
                }
        }
        h.IP = newIP
        h.Name = strings.TrimSpace(newName)
        if newGroup != "" {
                h.Group = newGroup
        }
        return nil
}

// RenameGroup переименовывает группу у всех хостов и в списке групп.
func (s *StateStore) RenameGroup(oldName, newName string) error {
        newName = strings.TrimSpace(newName)
        if oldName == "" || oldName == "Общие" || oldName == "Все хосты" {
                return errors.New("эту группу переименовать нельзя")
        }
        if newName == "" {
                return errors.New("пустое имя группы")
        }
        if newName == oldName {
                return nil
        }
        for _, g := range s.Groups() {
                if g == newName {
                        return fmt.Errorf("группа «%s» уже есть", newName)
                }
        }
        for i := range s.State.Hosts {
                if s.State.Hosts[i].Group == oldName {
                        s.State.Hosts[i].Group = newName
                }
        }
        for i, g := range s.State.Groups {
                if g == oldName {
                        s.State.Groups[i] = newName
                }
        }
        return nil
}

// Groups returns the union of groups currently used by hosts and the
// groups explicitly created by the user ("Новая группа"), so an empty
// group stays visible in the sidebar until deleted.
func (s *StateStore) Groups() []string {
        seen := map[string]bool{}
        var out []string
        add := func(g string) {
                g = strings.TrimSpace(g)
                if g == "" || g == "Все хосты" || seen[g] {
                        return
                }
                seen[g] = true
                out = append(out, g)
        }
        // persistent (user-created) groups first, in saved order
        for _, g := range s.State.Groups {
                add(g)
        }
        for _, h := range s.State.Hosts {
                add(h.Group)
        }
        return out
}

// AddGroup registers a user-created group; the group immediately appears
// in the sidebar even before any host is placed into it.
func (s *StateStore) AddGroup(name string) error {
        name = strings.TrimSpace(name)
        if name == "" {
                return errors.New("пустое имя группы")
        }
        if name == "Все хосты" {
                return errors.New("имя зарезервировано")
        }
        for _, g := range s.Groups() {
                if g == name {
                        return fmt.Errorf("группа «%s» уже есть", name)
                }
        }
        s.State.Groups = append(s.State.Groups, name)
        return nil
}

// RemoveGroup drops the persistent marker of a group (hosts are moved out
// separately). Returns false when the group was not user-created.
func (s *StateStore) RemoveGroup(name string) bool {
        out := s.State.Groups[:0]
        found := false
        for _, g := range s.State.Groups {
                if g == name {
                        found = true
                        continue
                }
                out = append(out, g)
        }
        s.State.Groups = out
        return found
}

func (s *StateStore) GroupCounts() map[string]int {
        m := map[string]int{}
        for _, h := range s.State.Hosts {
                m[h.Group]++
        }
        return m
}

func (s *StateStore) AddReport(e ReportEntry) {
        s.State.Reports = append(s.State.Reports, e)
        if len(s.State.Reports) > 5000 {
                s.State.Reports = s.State.Reports[len(s.State.Reports)-5000:]
        }
}

func (s *StateStore) TemplateByName(name string) *Template {
        for i := range s.State.Templates {
                if s.State.Templates[i].Name == name {
                        return &s.State.Templates[i]
                }
        }
        return nil
}

func (s *StateStore) ChainByName(name string) *Chain {
        for i := range s.State.Chains {
                if s.State.Chains[i].Name == name {
                        return &s.State.Chains[i]
                }
        }
        return nil
}

// ---------- schedules helpers ----------

func (s *StateStore) ScheduleByName(name string) *Schedule {
        for i := range s.State.Schedules {
                if s.State.Schedules[i].Name == name {
                        return &s.State.Schedules[i]
                }
        }
        return nil
}

func (s *StateStore) RemoveSchedule(name string) {
        out := s.State.Schedules[:0]
        for _, sc := range s.State.Schedules {
                if sc.Name != name {
                        out = append(out, sc)
                }
        }
        s.State.Schedules = out
}

// ---------- tags & favorites helpers ----------

// ---------- playbook variables & targets (v1.7.0) ----------

// expandVars подставляет переменные вида {{ имя }} в строку с учётом
// переменных конкретного хоста (они перекрывают глобальные) и встроенных
// значений: {{ hostname }}, {{ ip }}, {{ user }}, {{ date }}, {{ time }}.
func (s *StateStore) expandVars(ip, text string) string {
        if !strings.Contains(text, "{{") {
                return text
        }
        hv := map[string]string{}
        if s.State.PlaybookHostVars != nil {
                if m, ok := s.State.PlaybookHostVars[ip]; ok {
                        hv = m
                }
        }
        builtins := map[string]string{
                "ip":       ip,
                "hostname": ip,
                "user":     s.State.Username,
                "date":     time.Now().Format("2006-01-02"),
                "time":     time.Now().Format("15:04:05"),
                "datetime": time.Now().Format("2006-01-02 15:04:05"),
        }
        if h := s.FindHost(ip); h != nil {
                if n := h.DisplayName(); n != "" {
                        builtins["hostname"] = n
                }
                if h.Group != "" {
                        builtins["group"] = h.Group
                }
        }
        out := varRe.ReplaceAllStringFunc(text, func(m string) string {
                name := strings.Trim(m, "{} \t")
                if strings.HasPrefix(name, "secret:") {
                        return m // секреты раскрываются отдельно — ниже
                }
                if v, ok := hv[name]; ok {
                        return v
                }
                if v, ok := builtins[name]; ok {
                        return v
                }
                if s.State.PlaybookVars != nil {
                        if v, ok := s.State.PlaybookVars[name]; ok {
                                return v
                        }
                }
                return m // неизвестная переменная остаётся как есть
        })
        // {{ secret:имя }} — подстановка значений из менеджера секретов (v1.8.0)
        return s.expandSecretRefs(ip, out)
}

var varRe = regexp.MustCompile(`\{\{\s*[^{}\s]+\s*\}\}`)

// varExists — объявлена ли переменная (глобальная, хостовая или встроенная);
// используется проверкой «Проверить перед запуском» (v1.9.0).
func (s *StateStore) varExists(name string) bool {
        if strings.HasPrefix(name, "secret:") {
                return s.SecretValue(strings.TrimPrefix(name, "secret:")) != ""
        }
        switch name {
        case "ip", "hostname", "user", "date", "time", "datetime", "group":
                return true
        }
        if s.State.PlaybookVars != nil {
                if _, ok := s.State.PlaybookVars[name]; ok {
                        return true
                }
        }
        for _, m := range s.State.PlaybookHostVars {
                if _, ok := m[name]; ok {
                        return true
                }
        }
        return false
}

// expandTaskVars раскрывает переменные во всех текстовых полях задачи.
func (s *StateStore) expandTaskVars(ip string, t PlaybookTask) PlaybookTask {
        t.Source = s.expandVars(ip, t.Source)
        t.Action = s.expandVars(ip, t.Action)
        t.Dest = s.expandVars(ip, t.Dest)
        t.Search = s.expandVars(ip, t.Search)
        t.Replace = s.expandVars(ip, t.Replace)
        t.RestartService = s.expandVars(ip, t.RestartService)
        if len(t.PostCommands) > 0 {
                pc := make([]string, len(t.PostCommands))
                for i, c := range t.PostCommands {
                        pc[i] = s.expandVars(ip, c)
                }
                t.PostCommands = pc
        }
        return t
}

// ToggleFav переключает звёздочку у хоста.
func (s *StateStore) ToggleFav(ip string) bool {
        h := s.FindHost(ip)
        if h == nil {
                return false
        }
        h.Fav = !h.Fav
        return h.Fav
}

// SetTags заменяет список меток хоста.
func (s *StateStore) SetTags(ip string, tags []string) {
        h := s.FindHost(ip)
        if h == nil {
                return
        }
        h.Tags = tags
}

// AllTags — все используемые метки (для подсказок).
func (s *StateStore) AllTags() []string {
        seen := map[string]bool{}
        var out []string
        for _, h := range s.State.Hosts {
                for _, t := range h.Tags {
                        t = strings.TrimSpace(t)
                        if t != "" && !seen[t] {
                                seen[t] = true
                                out = append(out, t)
                        }
                }
        }
        return out
}

// jsonMarshal обёртка для masterpass.go.
func jsonMarshal(v interface{}) ([]byte, error) {
        return json.MarshalIndent(v, "", "  ")
}
