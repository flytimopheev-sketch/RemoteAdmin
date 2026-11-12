package main

// pbxtras.go — расширенные возможности редактора плейбука (v1.7.0):
//   * подсветка синтаксиса bash в редакторе команд;
//   * лёгкая проверка синтаксиса («Проверить синтаксис»);
//   * диалог переменных {{ var }} (глобальные и по хостам).

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gotk3/gotk3/gtk"
)

// ---------------------------------------------------------------------------
// 1. Подсветка синтаксиса bash
// ---------------------------------------------------------------------------

type bashSeg struct {
	text string
	cls  string // "", "kw", "cmd", "flag", "str", "var", "num", "op", "comm"
}

var bashKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "in": true, "function": true, "select": true,
	"return": true, "exit": true, "break": true, "continue": true,
	"local": true, "export": true, "readonly": true, "declare": true,
	"set": true, "shift": true, "trap": true, "eval": true, "source": true,
	"alias": true, "unset": true, "time": true,
}

var bashCommands = map[string]bool{
	"echo": true, "cd": true, "ls": true, "cat": true, "grep": true,
	"awk": true, "sed": true, "find": true, "systemctl": true,
	"dnf": true, "yum": true, "rpm": true, "apt": true, "apt-get": true,
	"ssh": true, "scp": true, "sftp": true, "curl": true, "wget": true,
	"tar": true, "gzip": true, "gunzip": true, "zip": true, "unzip": true,
	"chmod": true, "chown": true, "chgrp": true, "mount": true, "umount": true,
	"df": true, "du": true, "ps": true, "top": true, "kill": true,
	"pkill": true, "killall": true, "journalctl": true, "ip": true,
	"ifconfig": true, "ping": true, "ss": true, "netstat": true,
	"firewall-cmd": true, "useradd": true, "usermod": true, "userdel": true,
	"passwd": true, "groupadd": true, "mkdir": true, "rmdir": true,
	"rm": true, "cp": true, "mv": true, "ln": true, "touch": true,
	"stat": true, "head": true, "tail": true, "less": true, "more": true,
	"diff": true, "sort": true, "uniq": true, "wc": true, "tee": true,
	"xargs": true, "sh": true, "bash": true, "su": true, "sudo": true,
	"git": true, "vi": true, "vim": true, "nano": true, "service": true,
	"chkconfig": true, "free": true, "uptime": true, "hostname": true,
	"hostnamectl": true, "date": true, "sleep": true, "test": true,
	"printf": true, "read": true, "exec": true, "nohup": true,
	"crontab": true, "sestatus": true, "getenforce": true, "smartctl": true,
}

// bashTokenize — простой, но полезный разбиватель bash-строки на сегменты.
func bashTokenize(src string) []bashSeg {
	var out []bashSeg
	runes := []rune(src)
	i, n := 0, len(runes)
	atCmdPos := true // начало команды (после ; | && || ( { )
	wordStart := -1
	commitWord := func(end int) {
		if wordStart < 0 {
			return
		}
		w := string(runes[wordStart:end])
		cls := ""
		switch {
		case bashKeywords[w]:
			cls = "kw"
		case atCmdPos || bashCommands[w]:
			cls = "cmd"
		case strings.HasPrefix(w, "-"):
			cls = "flag"
		case isNumWord(w):
			cls = "num"
		}
		out = append(out, bashSeg{w, cls})
		wordStart = -1
		if w != "" {
			// после блочных ключевых слов снова позиция команды,
			// после обычных слов — нет
			switch w {
			case "then", "do", "else", "in", "case", "select":
				atCmdPos = true
			default:
				atCmdPos = false
			}
		}
	}
	for i < n {
		c := runes[i]
		switch {
		case c == '#' && wordStart < 0:
			// комментарий до конца строки
			end := i
			for end < n && runes[end] != '\n' {
				end++
			}
			out = append(out, bashSeg{string(runes[i:end]), "comm"})
			i = end
		case c == '\n':
			commitWord(i)
			out = append(out, bashSeg{"\n", ""})
			i++
			atCmdPos = true
		case c == '\'' || c == '"' || c == '`':
			commitWord(i)
			q := c
			end := i + 1
			for end < n {
				if q != '\'' && runes[end] == '\\' && end+1 < n {
					end += 2
					continue
				}
				// внутри двойных кавычек подсвечиваем $-переменные отдельно
				if q == '"' && runes[end] == '$' {
					if end > i {
						out = append(out, bashSeg{string(runes[i:end]), "str"})
					}
					vend := end + 1
					if vend < n && runes[vend] == '{' {
						for vend < n && runes[vend] != '}' {
							vend++
						}
						if vend < n {
							vend++
						}
					} else {
						for vend < n && (isAlnum(runes[vend]) || runes[vend] == '_') {
							vend++
						}
					}
					out = append(out, bashSeg{string(runes[end:vend]), "var"})
					end = vend
					i = vend
					continue
				}
				if runes[end] == q {
					end++
					break
				}
				end++
			}
			out = append(out, bashSeg{string(runes[i:end]), "str"})
			i = end
		case c == '$':
			commitWord(i)
			end := i + 1
			if end < n && runes[end] == '{' {
				for end < n && runes[end] != '}' {
					end++
				}
				if end < n {
					end++
				}
			} else if end < n && (isLetter(runes[end]) || runes[end] == '_') {
				for end < n && (isAlnum(runes[end]) || runes[end] == '_') {
					end++
				}
			} else if end < n && strings.ContainsRune("?@*#$$!", runes[end]) {
				end++
			}
			out = append(out, bashSeg{string(runes[i:end]), "var"})
			i = end
		case c == '-' && wordStart < 0:
			end := i
			for end < n && !isSpaceRune(runes[end]) && runes[end] != '\n' &&
				strings.IndexRune("|;&<>(", runes[end]) < 0 {
				end++
			}
			out = append(out, bashSeg{string(runes[i:end]), "flag"})
			i = end
		case c == '|' || c == '&' || c == ';' || c == '>' || c == '<' || c == '(' || c == ')':
			commitWord(i)
			end := i + 1
			// сдвоенные операторы: || && >> <<
			if end < n && runes[end] == c && strings.ContainsRune("|&<", c) {
				end++
			}
			out = append(out, bashSeg{string(runes[i:end]), "op"})
			i = end
			atCmdPos = true
		case isSpaceRune(c):
			commitWord(i)
			end := i
			for end < n && isSpaceRune(runes[end]) {
				end++
			}
			out = append(out, bashSeg{string(runes[i:end]), ""})
			i = end
		default:
			if wordStart < 0 {
				wordStart = i
			}
			i++
		}
	}
	commitWord(n)
	return out
}

func isSpaceRune(r rune) bool { return r == ' ' || r == '\t' || r == '\r' }
func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
}
func isAlnum(r rune) bool { return isLetter(r) || (r >= '0' && r <= '9') }
func isNumWord(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// bashEditor — TextView с живой подсветкой bash.
type bashEditor struct {
	tv    *gtk.TextView
	buf   *gtk.TextBuffer
	tags  map[string]*gtk.TextTag
	guard bool
}

// bashPalette — цвета подсветки для активной темы.
func bashPalette() map[string]string {
	if isLight() {
		return map[string]string{
			"kw": "#4353d8", "cmd": "#1f6feb", "flag": "#0e9f6e",
			"str": "#b26a00", "var": "#b3179a", "num": "#a020c0",
			"op": "#8a3fc0", "comm": "#8b93a5", "err": "#dc4b3f",
		}
	}
	return map[string]string{
		"kw": "#8f9bff", "cmd": "#7dd3fc", "flag": "#34d399",
		"str": "#fbbf24", "var": "#f472b6", "num": "#e879f9",
		"op": "#c084fc", "comm": "#667089", "err": "#f87171",
	}
}

// newBashEditor создаёт редактор с подсветкой; height — минимальная высота.
func newBashEditor(height int) *bashEditor {
	e := &bashEditor{}
	tv, _ := gtk.TextViewNew()
	tv.SetWrapMode(gtk.WRAP_WORD_CHAR)
	tv.SetSizeRequest(-1, height)
	tv.SetMonospace(true)
	buf, _ := tv.GetBuffer()
	e.tv, e.buf = tv, buf
	pal := bashPalette()
	e.tags = map[string]*gtk.TextTag{}
	mk := func(name string, props map[string]interface{}) {
		buf.CreateTag(name, props)
	}
	for cls, color := range pal {
		props := map[string]interface{}{"foreground": color}
		switch cls {
		case "kw", "op":
			props["weight"] = 700
		case "comm":
			props["style"] = 2 // pango italic
		}
		mk("bh-"+cls, props)
	}
	buf.Connect("changed", func() {
		if e.guard {
			return
		}
		glibTimeout(90, func() bool {
			e.highlight()
			return false
		})
	})
	return e
}

// highlight перекрашивает буфер по токенам (деструктивно, с защитой от рекурсии).
func (e *bashEditor) highlight() {
	if e.guard || e.buf == nil {
		return
	}
	text, _ := e.buf.GetText(e.buf.GetStartIter(), e.buf.GetEndIter(), false)
	pos := e.getCursorPos()
	e.guard = true
	e.buf.SetText("")
	for _, seg := range bashTokenize(text) {
		iter := e.buf.GetEndIter()
		if seg.cls != "" {
			e.buf.InsertWithTagByName(iter, seg.text, "bh-"+seg.cls)
		} else {
			e.buf.Insert(iter, seg.text)
		}
	}
	e.setClursorPos(pos)
	e.guard = false
}

func (e *bashEditor) getCursorPos() int {
	mark := e.buf.GetInsert()
	iter := e.buf.GetIterAtMark(mark)
	return iter.GetOffset()
}

func (e *bashEditor) setClursorPos(off int) {
	if off <= 0 {
		return
	}
	iter := e.buf.GetIterAtOffset(off)
	if iter == nil {
		return
	}
	e.buf.PlaceCursor(iter)
	mark := e.buf.GetInsert()
	e.tv.ScrollToMark(mark, 0.0, true, 0.0, 1.0)
}

func (e *bashEditor) text() string {
	s, _ := e.buf.GetText(e.buf.GetStartIter(), e.buf.GetEndIter(), false)
	return s
}

func (e *bashEditor) setText(s string) {
	e.guard = true
	e.buf.SetText(s)
	e.guard = false
	e.highlight()
}

// ---------------------------------------------------------------------------
// 2. Проверка синтаксиса
// ---------------------------------------------------------------------------

// checkBashSyntax возвращает список замечаний (пустой список = проблем нет).
// Это ЛЁГКАЯ проверка: кавычки, скобки, парные блоки if/fi, for/done и т.п.
func checkBashSyntax(src string) []string {
	var problems []string
	add := func(s string) { problems = append(problems, s) }

	runes := []rune(src)
	var quote rune
	var stack []rune
	line := 1
	i, n := 0, len(runes)
	for i < n {
		c := runes[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if quote != 0 {
			if quote != '\'' && c == '\\' && i+1 < n {
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '\\':
			i++ // пропустить экранированный символ
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			var open rune
			switch c {
			case ')':
				open = '('
			case ']':
				open = '['
			default:
				open = '{'
			}
			if len(stack) == 0 || stack[len(stack)-1] != open {
				add(fmt.Sprintf("строка %d: лишняя закрывающая %c", line, c))
			} else {
				stack = stack[:len(stack)-1]
			}
		}
		i++
	}
	if quote != 0 {
		add(fmt.Sprintf("не закрыта кавычка %c", quote))
	}
	if len(stack) > 0 {
		add(fmt.Sprintf("не закрыта скобка %c (нужна парная)", stack[len(stack)-1]))
	}

	// парные блоки: if/fi, for|while|until/done, case/esac
	counts := map[string]int{}
	words := bashWords(src)
	for _, w := range words {
		counts[w]++
	}
	if counts["if"] > counts["fi"] {
		add(fmt.Sprintf("блок if без fi (if: %d, fi: %d)", counts["if"], counts["fi"]))
	}
	if counts["fi"] > counts["if"] {
		add(fmt.Sprintf("лишний fi (if: %d, fi: %d)", counts["if"], counts["fi"]))
	}
	loops := counts["for"] + counts["while"] + counts["until"]
	if loops > counts["done"] {
		add(fmt.Sprintf("цикл без done (for/while: %d, done: %d)", loops, counts["done"]))
	}
	if counts["done"] > loops {
		add(fmt.Sprintf("лишний done (for/while: %d, done: %d)", loops, counts["done"]))
	}
	if counts["case"] > counts["esac"] {
		add(fmt.Sprintf("case без esac (case: %d, esac: %d)", counts["case"], counts["esac"]))
	}
	if counts["esac"] > counts["case"] {
		add(fmt.Sprintf("лишний esac (case: %d, esac: %d)", counts["case"], counts["esac"]))
	}

	// незавершённое продолжение строки
	if len(runes) > 0 && runes[len(runes)-1] == '\\' {
		add("строка заканчивается на \\ — продолжение не задано")
	}
	// опасные паттерны
	low := " " + strings.ToLower(src) + " "
	if strings.Contains(low, "rm -rf / ") || strings.HasSuffix(low, "rm -rf / ") ||
		strings.Contains(low, "rm -fr / ") || strings.Contains(low, "mkfs.") ||
		strings.Contains(low, "> /dev/sd") || strings.Contains(low, "dd if=") && strings.Contains(low, "of=/dev/sd") {
		add("опасная команда: полное затирание диска/корня — перепроверьте")
	}
	if strings.TrimSpace(src) == "" {
		add("команда пустая")
	}
	return problems
}

// bashWords — слова вне кавычек и комментариев (для подсчёта блоков).
func bashWords(src string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	inComment := false
	for _, c := range src {
		if inComment {
			if c == '\n' {
				inComment = false
			}
			continue
		}
		if quote != 0 {
			if quote != '\'' && c == '\\' {
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '#':
			inComment = true
		case c == '\'' || c == '"':
			quote = c
		case isAlnum(c) || c == '_':
			cur.WriteRune(c)
		default:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// syntaxReportText собирает текст отчёта для диалога.
func syntaxReportText(kind string, problems []string) string {
	if len(problems) == 0 {
		return "Проблем не найдено — " + kind + " выглядит корректно."
	}
	var b strings.Builder
	b.WriteString("Найдено замечаний: " + fmt.Sprint(len(problems)) + "\n")
	for _, p := range problems {
		b.WriteString("• " + p + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 3. Переменные {{ var }}
// ---------------------------------------------------------------------------

// showPlaybookVarsDialog — редактор переменных: глобальные (все хосты)
// и индивидуальные для выбранного хоста.
func showPlaybookVarsDialog() {
	dlg, err2 := gtk.DialogNew()
	if err2 != nil {
		return
	}
	dlg.SetTitle("Переменные плейбука")
	dlg.SetTransientFor(app.window)
	dlg.SetModal(true)
	dlg.SetDefaultSize(560, 520)
	dlg.AddButton("Отмена", gtk.RESPONSE_CANCEL)
	dlg.AddButton("Сохранить", gtk.RESPONSE_ACCEPT)

	box, _ := dlg.GetContentArea()
	box.SetSpacing(6)

	// подсказка по встроенным переменным
	hint := labelNew("Подстановка в командах: {{ hostname }} {{ ip }} {{ user }} {{ group }} {{ date }} — встроенные;\n" +
		"остальные задайте ниже (перекрываются переменными хоста). Формат: имя = значение")
	addClass(hint, "ramuted")
	hint.SetXAlign(0)
	hint.SetLineWrap(true)
	box.PackStart(hint, false, false, 2)

	// --- глобальные ---
	lab1 := labelNew("Глобальные переменные (действуют на всех хостах):")
	addClass(lab1, "ratitle")
	lab1.SetXAlign(0)
	box.PackStart(lab1, false, false, 2)
	gv := newBashEditor(90)
	gv.setText(varsToText(store.State.PlaybookVars))
	gsc := scrolled(gv.tv, 90)
	gsc.SetVExpand(false)
	box.PackStart(gsc, false, false, 0)

	// --- по хостам ---
	lab2 := labelNew("Переменные конкретного хоста (перекрывают глобальные):")
	addClass(lab2, "ratitle")
	lab2.SetXAlign(0)
	box.PackStart(lab2, false, false, 2)

	hostCombo, _ := gtk.ComboBoxTextNew()
	var hosts []string
	seen := map[string]bool{}
	for _, h := range store.State.Hosts {
		if !seen[h.IP] {
			seen[h.IP] = true
			hosts = append(hosts, h.IP)
		}
	}
	sort.Strings(hosts)
	for _, ip := range hosts {
		name := ip
		if h := store.FindHost(ip); h != nil && h.Name != "" {
			name = h.Name + " (" + ip + ")"
		}
		hostCombo.AppendText(name)
	}
	hostRow := boxNew(gtk.ORIENTATION_HORIZONTAL, 4)
	hostRow.PackStart(labelNew("Хост:"), false, false, 0)
	hostRow.PackStart(hostCombo, true, true, 0)
	box.PackStart(hostRow, false, false, 0)

	hv := newBashEditor(90)
	hsc := scrolled(hv.tv, 90)
	hsc.SetVExpand(false)
	box.PackStart(hsc, false, false, 0)

	// при смене хоста — показать его переменные
	hvFor := func() string {
		idx := hostCombo.GetActive()
		if idx < 0 || idx >= len(hosts) {
			return ""
		}
		return hosts[idx]
	}
	loadHostVars := func() {
		ip := hvFor()
		if ip == "" {
			hv.setText("")
			return
		}
		m := store.State.PlaybookHostVars[ip]
		hv.setText(varsToText(m))
	}
	hostCombo.Connect("changed", func() { loadHostVars() })
	if len(hosts) > 0 {
		hostCombo.SetActive(0)
	}

	box.ShowAll()
	if dlg.Run() == gtk.RESPONSE_ACCEPT {
		if store.State.PlaybookVars == nil {
			store.State.PlaybookVars = map[string]string{}
		}
		store.State.PlaybookVars = textToVars(gv.text())
		if store.State.PlaybookHostVars == nil {
			store.State.PlaybookHostVars = map[string]map[string]string{}
		}
		if ip := hvFor(); ip != "" {
			m := textToVars(hv.text())
			if len(m) == 0 {
				delete(store.State.PlaybookHostVars, ip)
			} else {
				store.State.PlaybookHostVars[ip] = m
			}
		}
		_ = store.Save()
		logf("ok", "🔤 Переменные плейбука сохранены (%d глобальных)", len(store.State.PlaybookVars))
	}
	dlg.Destroy()
}

func varsToText(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, m[k])
	}
	return b.String()
}

func textToVars(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// readScriptPreview читает локальный скрипт (для проверки синтаксиса).
func readScriptPreview(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
