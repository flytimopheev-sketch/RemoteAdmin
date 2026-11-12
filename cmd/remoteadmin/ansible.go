package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gotk3/gotk3/gtk"
)

// ---------- Ansible inventory / plain text import & export ----------

// parseAnsibleINI parses standard INI inventory.
func parseAnsibleINI(path string) ([]HostEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hosts []HostEntry
	group := "Общие"
	isChildren := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inner := strings.Trim(line, "[]")
			if strings.HasSuffix(inner, ":children") {
				group = strings.TrimSuffix(inner, ":children")
				isChildren = true
			} else if strings.Contains(inner, ":") {
				// :vars section — skip
				group = ""
				isChildren = false
			} else {
				group = inner
				isChildren = false
			}
			continue
		}
		if group == "" {
			continue
		}
		if isChildren {
			// group names inside children section: track as group alias — skip hosts
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		host := fields[0]
		ip := host
		port := 22
		for _, v := range fields[1:] {
			if strings.HasPrefix(v, "ansible_host=") {
				ip = strings.TrimPrefix(v, "ansible_host=")
			}
			if strings.HasPrefix(v, "ansible_port=") {
				fmt.Sscanf(strings.TrimPrefix(v, "ansible_port="), "%d", &port)
			}
		}
		if ip == "" {
			continue
		}
		if port != 22 {
			ip = fmt.Sprintf("%s:%d", ip, port)
		}
		hosts = append(hosts, HostEntry{IP: ip, Group: group})
	}
	return hosts, sc.Err()
}

// parseAnsibleYAML parses the common YAML inventory layout:
//
//	all:
//	  children:
//	    group:
//	      hosts:
//	        host1:
//	          ansible_host: 10.0.0.1
func parseAnsibleYAML(path string) ([]HostEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseAnsibleYAMLText(string(data)), nil
}

func parseAnsibleYAMLText(data string) []HostEntry {
	var hosts []HostEntry
	group := "Общие"
	// Indentation anchors recorded from the "children:" line of the
	// standard layout (all → children → group → hosts → hostname →
	// ansible_host: …); they keep us from confusing "hosts:"/hostnames
	// at ANY indent depth with group names.
	groupInd, hostsInd, hostInd := -1, -1, -1
	inHosts := false
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		name := strings.TrimSuffix(trimmed, ":")
		isKey := strings.HasSuffix(trimmed, ":")
		switch {
		case isKey && name == "children" && groupInd < 0:
			groupInd = indent + 2
			hostsInd = indent + 4
			hostInd = indent + 6
			inHosts = false
		case isKey && name == "all":
			// top-level "all:" — keep waiting for children
		case groupInd >= 0 && indent == groupInd && isKey:
			group = name
			inHosts = false
		case groupInd >= 0 && indent == hostsInd && isKey && name == "hosts":
			inHosts = true
		case groupInd >= 0 && indent >= hostInd && isKey:
			// hostname entry (or a stray deeper key)
			if inHosts && name != "" && !strings.Contains(name, " ") {
				hosts = append(hosts, HostEntry{IP: name, Group: group})
			}
		case strings.HasPrefix(trimmed, "ansible_host:"):
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "ansible_host:"))
			if len(hosts) > 0 && val != "" {
				hosts[len(hosts)-1].IP = val
			}
		case strings.HasPrefix(trimmed, "ansible_port:"):
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "ansible_port:"))
			if len(hosts) > 0 && isAllDigits(val) && val != "22" {
				hosts[len(hosts)-1].IP = fmt.Sprintf("%s:%s", hosts[len(hosts)-1].IP, val)
			}
		}
	}
	return hosts
}

// parseHostListText parses a plain host list — one host per line. Also
// accepts optional [group] section headers (like a minimal inventory) and
// inline "group:host" prefixes (legacy RemoteAdmin format), plus CSV with
// the host in the first column.
//
//	10.0.0.1
//	10.0.0.2:2222
//	[web]
//	10.0.1.1
//	web2:10.0.1.2
//	10.0.2.1,сервер БД
func parseHostListText(text string) []HostEntry {
	var hosts []HostEntry
	group := "Общие"
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		// CSV support: "10.0.0.1,web-1" → host is the first column,
		// second column — пользовательское имя (не комментарий!)
		var name string
		cols := strings.Split(line, ",")
		if len(cols) > 1 && strings.TrimSpace(cols[0]) != "" {
			line = strings.TrimSpace(cols[0])
			if strings.TrimSpace(cols[1]) != "" {
				name = strings.TrimSpace(cols[1])
			}
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			g := strings.Trim(line, "[]")
			g = strings.TrimSuffix(g, ":children")
			if !strings.Contains(g, ":") && g != "" {
				group = g
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		tok := fields[0]
		for _, v := range fields[1:] {
			if strings.HasPrefix(v, "ansible_host=") {
				tok = strings.TrimPrefix(v, "ansible_host=")
			}
		}
		if tok == "" {
			continue
		}
		ip, g := splitHostToken(tok)
		if g != "" {
			group = g
		}
		if ip == "" {
			continue
		}
		hosts = append(hosts, HostEntry{IP: ip, Name: name, Group: group})
	}
	return hosts
}

// splitHostToken handles the legacy "group:ip" form while never misparsing
// "host:port" (a pure-numeric tail is a port; a tail with dots/letters is
// an address after a group prefix).
func splitHostToken(tok string) (ip, group string) {
	// more than one colon → ipv6 or garbage, take as is
	if strings.Count(tok, ":") != 1 {
		return tok, ""
	}
	i := strings.Index(tok, ":")
	head, tail := tok[:i], tok[i+1:]
	if tail == "" || head == "" {
		return tok, ""
	}
	// pure digits after colon → host:port (10.0.0.1:2222)
	if isAllDigits(tail) {
		return tok, ""
	}
	// digits with dots would be an ipv4 → also host:port-style; but
	// "10.5.1.2" contains dots so it can't be a bare port, treat
	// "head:10.5.1.2" as group prefix
	return tail, head
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// detectAndParse tries INI → YAML → plain text, returning what it found.
func detectAndParse(path string) ([]HostEntry, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	text := string(data)
	// 1) INI sections present?
	if strings.Contains(text, "[") && strings.Contains(text, "]") {
		if hosts, err := parseAnsibleINI(path); err == nil && len(hosts) > 0 {
			return hosts, "Ansible INI", nil
		}
	}
	// 2) YAML markers?
	if strings.Contains(text, "children:") || strings.Contains(text, "hosts:") {
		hosts := parseAnsibleYAMLText(text)
		if len(hosts) > 0 {
			return hosts, "Ansible YAML", nil
		}
	}
	// 3) plain list
	hosts := parseHostListText(text)
	if len(hosts) > 0 {
		return hosts, "список хостов", nil
	}
	// 4) last resort: full INI parse even without [..] (error surfaced)
	hosts2, err := parseAnsibleINI(path)
	return hosts2, "Ansible INI", err
}

// ---------- Remmina import ----------

// parseRemminaFile читает один .remmina-файл (INI-подобный) и возвращает
// хост с именем, адресом и группой.
func parseRemminaFile(path string) (HostEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return HostEntry{}, false
	}
	var name, server, group, protocol string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		switch key {
		case "name":
			name = val
		case "server":
			server = val
		case "group":
			group = val
		case "protocol":
			protocol = val
		}
	}
	if server == "" {
		return HostEntry{}, false
	}
	// нормализация адреса: Remmina может хранить "host:port", "host::port" (VNC)
	server = strings.ReplaceAll(server, "::", ":")
	if strings.HasPrefix(server, ":") {
		server = "127.0.0.1" + server
	}
	ip := server
	// для RDP/VNC порт несовместим с SSH — оставляем только хост
	if protocol == "RDP" || protocol == "VNC" || protocol == "SPICE" {
		if i := strings.LastIndex(ip, ":"); i > 0 {
			ip = ip[:i]
		}
	}
	if ip == "" || strings.ContainsAny(ip, " ") {
		return HostEntry{}, false
	}
	if group == "" {
		group = "Remmina"
	}
	dispName := name
	if dispName == "" {
		dispName = ip
	}
	return HostEntry{IP: ip, Name: dispName, Group: group}, true
}

// parseRemminaDir обходит каталог с подключениями Remmina
// (обычно ~/.config/remmina).
func parseRemminaDir(dir string) ([]HostEntry, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	var hosts []HostEntry
	skipped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".remmina") {
			continue
		}
		h, ok := parseRemminaFile(filepath.Join(dir, e.Name()))
		if !ok {
			skipped++
			continue
		}
		hosts = append(hosts, h)
	}
	return hosts, skipped, nil
}

// importRemminaDialog импортирует подключения Remmina: выбирается любой
// .remmina-файл, импортируется весь каталог подключений.
func importRemminaDialog() {
	fn := fileChooserFiltered(app.window, "Выберите любой .remmina-файл — импортируются все подключения",
		gtk.FILE_CHOOSER_ACTION_OPEN, "", []string{"*.remmina"})
	if fn == "" {
		return
	}
	var hosts []HostEntry
	skipped := 0
	hs, sk, err := parseRemminaDir(filepath.Dir(fn))
	if err == nil && len(hs) > 0 {
		hosts, skipped = hs, sk
	} else if h, ok := parseRemminaFile(fn); ok {
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		warnDialog(app.window, "Не найдено ни одного подключения Remmina.\n\nПроверьте, что Remmina установлена и подключения сохранены\n(обычно ~/.config/remmina/*.remmina).")
		return
	}
	importHostEntries(hosts, fmt.Sprintf("Remmina (%d подключений, пропущено %d)", len(hosts), skipped))
}

func importAnsibleDialog() {
	fn := fileChooserFiltered(app.window, "Импорт хостов", gtk.FILE_CHOOSER_ACTION_OPEN, "",
		[]string{"*.ini", "*.cfg", "*.conf", "*.txt", "*.list", "*.csv", "*.yaml", "*.yml", "*.inventory", "*.hosts"})
	if fn == "" {
		return
	}
	hosts, kind, err := detectAndParse(fn)
	if err != nil && len(hosts) == 0 {
		errDialog(app.window, "Импорт: "+err.Error())
		return
	}
	if len(hosts) == 0 {
		warnDialog(app.window, "В файле не найдено ни одного хоста.\n\nПоддерживаются: список по одному в строке (10.0.0.1 или 10.0.0.1:2222),\nсекции [группа], CSV (хост в 1-й колонке), Ansible INI и YAML.")
		return
	}
	importHostEntries(hosts, kind)
}

// importPasteDialog adds hosts from a pasted multi-line list.
func importPasteDialog() {
	d, err := gtk.DialogNewWithButtons("Вставить список хостов", app.window, gtk.DIALOG_MODAL,
		[]interface{}{"Добавить", gtk.RESPONSE_ACCEPT},
		[]interface{}{"Отмена", gtk.RESPONSE_CANCEL},
	)
	if err != nil || d == nil {
		return
	}
	defer d.Destroy()
	d.SetDefaultSize(520, 380)
	box, _ := d.GetContentArea()
	hint := labelNew("По одному хосту в строке. Поддерживается:\n• 10.0.0.1  или  10.0.0.1:2222 (порт)\n• [имя-группы] — заголовок секции (хосты ниже попадут в неё)\n• группа:10.0.0.1 — префикс группы\n• CSV: 10.0.0.1,комментарий")
	hint.SetXAlign(0)
	hint.SetLineWrap(true)
	hsc, _ := hint.GetStyleContext()
	hsc.AddClass("ramuted")
	box.PackStart(hint, false, false, 4)
	tv, _ := gtk.TextViewNew()
	tv.SetWrapMode(gtk.WRAP_WORD_CHAR)
	buf, _ := tv.GetBuffer()
	sc := scrolled(tv, 200)
	sc.SetVExpand(true)
	box.PackStart(sc, true, true, 0)
	box.ShowAll()
	if d.Run() != gtk.RESPONSE_ACCEPT {
		return
	}
	text, _ := buf.GetText(buf.GetStartIter(), buf.GetEndIter(), false)
	if strings.TrimSpace(text) == "" {
		return
	}
	hosts := parseHostListText(text)
	if len(hosts) == 0 {
		warnDialog(app.window, "Не удалось разобрать ни одного адреса")
		return
	}
	importHostEntries(hosts, "вставленный список")
}

// importHostEntries adds parsed hosts to the store and refreshes the UI.
func importHostEntries(hosts []HostEntry, kind string) {
	added, skipped := 0, 0
	groups := map[string]bool{}
	for _, h := range hosts {
		if err := store.AddHost(h.IP, h.Group); err != nil {
			skipped++
		} else {
			added++
			groups[h.Group] = true
		}
	}
	for g := range groups {
		_ = store.AddGroup(g)
	}
	_ = store.Save()
	app.hostsUI.refreshAll()
	if added == 0 {
		warnDialog(app.window, "Ни один хост не добавлен: все адреса уже есть в списке")
		return
	}
	logf("ok", "⬇ Импорт (%s): добавлено %d, пропущено %d (дубли)", kind, added, skipped)
	Notify("Импорт хостов", fmt.Sprintf("Добавлено %d хостов", added))
}

func exportAnsibleDialog() {
	if len(store.State.Hosts) == 0 {
		warnDialog(app.window, "Список хостов пуст")
		return
	}
	fn := fileChooser(app.window, "Экспорт inventory", gtk.FILE_CHOOSER_ACTION_SAVE, "inventory.ini")
	if fn == "" {
		return
	}
	var content string
	if strings.HasSuffix(strings.ToLower(fn), ".yaml") || strings.HasSuffix(strings.ToLower(fn), ".yml") {
		content = buildAnsibleYAML()
	} else {
		content = buildAnsibleINI()
	}
	if err := os.WriteFile(fn, []byte(content), 0644); err != nil {
		errDialog(app.window, "Экспорт: "+err.Error())
		return
	}
	logf("ok", "⬆ Inventory сохранён: %s (%d хостов)", fn, len(store.State.Hosts))
}

func buildAnsibleINI() string {
	groups := map[string][]string{}
	for _, h := range store.State.Hosts {
		groups[h.Group] = append(groups[h.Group], h.IP)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Сгенерировано RemoteAdmin " + AppVersion + "\n")
	for _, g := range names {
		fmt.Fprintf(&b, "\n[%s]\n", g)
		for _, ip := range groups[g] {
			host, port := ip, 22
			if i := strings.LastIndex(ip, ":"); i > 0 {
				host, port = ip[:i], atoiSafe(ip[i+1:])
			}
			fmt.Fprintf(&b, "%s ansible_host=%s ansible_port=%d\n", host, host, port)
		}
	}
	return b.String()
}

func atoiSafe(s string) int {
	n := 22
	fmt.Sscanf(s, "%d", &n)
	return n
}

func buildAnsibleYAML() string {
	groups := map[string][]string{}
	for _, h := range store.State.Hosts {
		groups[h.Group] = append(groups[h.Group], h.IP)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Сгенерировано RemoteAdmin " + AppVersion + "\n")
	b.WriteString("all:\n  children:\n")
	for _, g := range names {
		fmt.Fprintf(&b, "    %s:\n      hosts:\n", g)
		for _, ip := range groups[g] {
			host, port := ip, 22
			if i := strings.LastIndex(ip, ":"); i > 0 {
				host, port = ip[:i], atoiSafe(ip[i+1:])
			}
			fmt.Fprintf(&b, "        %s:\n          ansible_host: %s\n          ansible_port: %d\n", host, host, port)
		}
	}
	return b.String()
}
