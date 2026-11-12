package main

// pbxtras_test.go — тесты подсветки/валидации bash и подстановки переменных.

import (
	"strings"
	"testing"
)

func classes(src string) []string {
	var out []string
	for _, seg := range bashTokenize(src) {
		if seg.cls != "" {
			out = append(out, seg.cls)
		}
	}
	return out
}

func TestBashTokenizeKeywords(t *testing.T) {
	cls := classes("if [ -f /etc/hosts ]; then systemctl restart nginx; fi")
	joined := strings.Join(cls, ",")
	for _, want := range []string{"kw", "flag", "cmd", "op"} {
		if !strings.Contains(joined, want) {
			t.Errorf("нет класса %q в %v", want, cls)
		}
	}
}

func TestBashTokenizeStringVarComment(t *testing.T) {
	cls := classes("echo \"hello $USER\" # комментарий")
	joined := strings.Join(cls, ",")
	if !strings.Contains(joined, "str") {
		t.Errorf("строка не подсвечена: %v", cls)
	}
	if !strings.Contains(joined, "var") {
		t.Errorf("переменная не подсвечена: %v", cls)
	}
	if !strings.Contains(joined, "comm") {
		t.Errorf("комментарий не подсвечен: %v", cls)
	}
}

func TestCheckBashSyntaxOk(t *testing.T) {
	if p := checkBashSyntax("df -h && free -m || echo 'ok'"); len(p) != 0 {
		t.Errorf("ложные замечания: %v", p)
	}
	if p := checkBashSyntax("for i in 1 2 3; do echo $i; done"); len(p) != 0 {
		t.Errorf("цикл with done не должен давать замечаний: %v", p)
	}
}

func TestCheckBashSyntaxProblems(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"echo 'незакрытая", "кавычка"},
		{"if true; then echo 1", "if без fi"},
		{"for i in 1 2; do echo $i", "цикл без done"},
		{"( echo 1", "скобка"},
		{"echo 1 )", "лишняя закрывающая"},
		{"case $x in a) ;;", "case без esac"},
	}
	for _, c := range cases {
		p := checkBashSyntax(c.src)
		found := false
		for _, s := range p {
			if strings.Contains(s, c.want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("checkBashSyntax(%q): ожидали замечание %q, получили %v", c.src, c.want, p)
		}
	}
}

func TestCheckBashSyntaxDanger(t *testing.T) {
	p := checkBashSyntax("rm -rf /")
	if len(p) == 0 || !strings.Contains(strings.Join(p, " "), "опасн") {
		t.Errorf("rm -rf / должен помечаться опасным: %v", p)
	}
}

func TestExpandVars(t *testing.T) {
	st := &StateStore{State: State{
		Username: "admin",
		Hosts:    []HostEntry{{IP: "10.0.0.5", Name: "web-1", Group: "Прод"}},
	}}
	st.State.PlaybookVars = map[string]string{"port": "8080"}
	st.State.PlaybookHostVars = map[string]map[string]string{"10.0.0.5": {"port": "9090"}}

	if got := st.expandVars("10.0.0.5", "systemctl restart app && curl http://{{ hostname }}:{{ port }}"); got != "systemctl restart app && curl http://web-1:9090" {
		t.Errorf("host vars: %q", got)
	}
	if got := st.expandVars("10.0.0.9", "echo {{ user }} {{ port }} {{ ip }}"); got != "echo admin 8080 10.0.0.9" {
		t.Errorf("global vars: %q", got)
	}
	if got := st.expandVars("10.0.0.9", "unknown {{ nosuch }} stays"); got != "unknown {{ nosuch }} stays" {
		t.Errorf("unknown var must stay: %q", got)
	}
}

func TestExpandTaskVars(t *testing.T) {
	st := &StateStore{State: State{Username: "root"}}
	st.State.PlaybookVars = map[string]string{"svc": "nginx"}
	tk := PlaybookTask{Type: "command", Source: "systemctl restart {{ svc }}", PostCommands: []string{"echo {{ svc }}"}}
	out := st.expandTaskVars("1.2.3.4", tk)
	if out.Source != "systemctl restart nginx" || out.PostCommands[0] != "echo nginx" {
		t.Errorf("expandTaskVars: %+v", out)
	}
}

func TestDescribeFullNotTruncated(t *testing.T) {
	long := "df -h; echo " + strings.Repeat("x", 200)
	tk := PlaybookTask{Type: "command", Source: long}
	d := tk.DescribeFull()
	if !strings.Contains(d, long) {
		t.Errorf("DescribeFull обрезает команду: len=%d", len(d))
	}
	if strings.HasSuffix(tk.Describe(), long) {
		// Describe может быть равен, если строка короче лимита — не ошибка
	}
}

func TestRunIfRuAndActions(t *testing.T) {
	if runIfRu("on_failure") == "" {
		t.Error("runIfRu on_failure пуст")
	}
	if serviceActionRu("") != "перезапустить" {
		t.Error("serviceActionRu default")
	}
	if pkgActionRu("") != "установить" {
		t.Error("pkgActionRu default")
	}
}

func TestVarsTextRoundtrip(t *testing.T) {
	m := map[string]string{"b": "2", "a": "1"}
	txt := varsToText(m)
	back := textToVars(txt)
	if len(back) != 2 || back["a"] != "1" || back["b"] != "2" {
		t.Errorf("roundtrip: %v -> %q -> %v", m, txt, back)
	}
}
