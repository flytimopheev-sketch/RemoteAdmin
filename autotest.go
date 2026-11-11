package main

// autotest.go — headless functional test suite (--autotest).
// Runs inside the real GTK app under Xvfb and exercises:
//   state/crypto, host groups & moves, all UI pages, SSH exec (incl. su - root),
//   playbook tasks, and a full SFTP session (list/mkdir/upload/download/rename/
//   recursive transfer/remove) against a built-in SSH server, so the whole
//   stack is tested end-to-end without any external host.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/gtk"
	"golang.org/x/crypto/ssh"
)

type atResult struct {
	name string
	err  error
	dur  time.Duration
}

var (
	atMu       sync.Mutex
	atResults  []atResult
	atFailures int
)

func atCheck(name string, fn func() error) {
	start := time.Now()
	err := fn()
	atMu.Lock()
	defer atMu.Unlock()
	atResults = append(atResults, atResult{name: name, err: err, dur: time.Since(start)})
	if err != nil {
		atFailures++
		fmt.Printf("FAIL  %-42s %v\n", name, err)
	} else {
		fmt.Printf("PASS  %-42s (%s)\n", name, time.Since(start).Round(time.Millisecond))
	}
}

func atWant(cond bool, format string, args ...interface{}) error {
	if !cond {
		return fmt.Errorf(format, args...)
	}
	return nil
}

// runAutotest executes the whole suite in a goroutine and quits GTK when done.
func runAutotest() {
	go func() {
		// wait until the UI is fully realized
		time.Sleep(600 * time.Millisecond)
		fmt.Println("== RemoteAdmin autotest ==")
		runAutotestSuite()
		fmt.Printf("== Autotest: %d проверок, %d ошибок ==\n", len(atResults), atFailures)
		if atFailures > 0 {
			fmt.Println("AUTOTEST FAIL")
		} else {
			fmt.Println("AUTOTEST OK")
		}
		_ = store.Save()
		gtkQuitAsync()
	}()
}

func gtkQuitAsync() {
	glibIdle(func() { gtk.MainQuit() })
}

// rowCount counts rows of a ListStore (via its TreeModel view).
func rowCount(st *gtk.ListStore) int {
	if st == nil {
		return 0
	}
	m := st.ToTreeModel()
	n := 0
	iter, ok := m.GetIterFirst()
	for ok {
		n++
		ok = m.IterNext(iter)
	}
	return n
}

func runAutotestSuite() {
	// ---------- fake ssh host ----------
	srv, err := startFakeSSHServer("autouser", "autotest-pass")
	if err != nil {
		atCheck("fake-ssh-server", func() error { return err })
		return
	}
	defer srv.Close()
	hostAddr := fmt.Sprintf("127.0.0.1:%d", srv.port)

	// point app state at the fake host
	store.State.Username = "autouser"
	store.SetPassword("autotest-pass")
	store.SetRootPassword("autotest-root")
	store.State.Settings.StrictHostKey = "no"
	store.State.Settings.ConnectTimeout = 5
	store.State.Settings.ExecTimeout = 30
	store.State.Settings.ConfirmDanger = false
	store.State.KeyPath = ""
	_ = store.Save()

	// ---------- state & groups ----------
	atCheck("state: add hosts / groups", func() error {
		store.State.Hosts = nil
		if err := store.AddHost("10.99.0.1", "web"); err != nil {
			return err
		}
		if err := store.AddHost("10.99.0.2", "web"); err != nil {
			return err
		}
		if err := store.AddHost("10.99.0.3", ""); err != nil {
			return err
		}
		if store.FindHost("10.99.0.3").Group != "Общие" {
			return fmt.Errorf("пустая группа должна попадать в «Общие»")
		}
		if err := store.AddHost("10.99.0.1", "web"); err == nil {
			return fmt.Errorf("дубликат хоста должен отклоняться")
		}
		if n := store.GroupCounts()["web"]; n != 2 {
			return fmt.Errorf("counts[web]=%d, хочу 2", n)
		}
		if len(store.Groups()) != 2 {
			return fmt.Errorf("групп должно быть 2, есть %d", len(store.Groups()))
		}
		return nil
	})

	atCheck("state: move host to Общие + refresh", func() error {
		h := store.FindHost("10.99.0.1")
		if h == nil {
			return fmt.Errorf("хост не найден")
		}
		h.Group = "Общие"
		_ = store.Save()
		glibIdle(func() { app.hostsUI.refreshAll() })
		time.Sleep(150 * time.Millisecond)
		return atWant(store.GroupCounts()["Общие"] >= 2, "после перемещения в «Общие» должно быть ≥2 хостов")
	})

	atCheck("state: save/load roundtrip", func() error {
		_ = store.Save()
		path := store.path
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "autotest-pass") {
			return fmt.Errorf("пароль сохранён в открытом виде!")
		}
		s2 := &StateStore{path: path}
		s2.load()
		if s2.Password() != "autotest-pass" {
			return fmt.Errorf("пароль не расшифровывается после перезагрузки")
		}
		if len(s2.State.Hosts) != len(store.State.Hosts) {
			return fmt.Errorf("хосты потеряны при сохранении")
		}
		return nil
	})

	atCheck("state: legacy group:ip migration", func() error {
		legacy := []byte(`{"hosts":["web:10.99.9.9","Общие:10.99.9.10"]}`)
		tmp := filepath.Join(os.TempDir(), "ra_legacy_state.json")
		_ = os.WriteFile(tmp, legacy, 0600)
		s2 := &StateStore{path: tmp}
		s2.load()
		if len(s2.State.Hosts) != 2 || s2.State.Hosts[0].IP != "10.99.9.9" || s2.State.Hosts[0].Group != "web" {
			return fmt.Errorf("миграция старого формата сломана: %+v", s2.State.Hosts)
		}
		_ = os.Remove(tmp)
		return nil
	})

	atCheck("state: персистентные группы (Новая группа)", func() error {
		if err := store.AddGroup("at-пустая-группа"); err != nil {
			return err
		}
		found := false
		for _, g := range store.Groups() {
			if g == "at-пустая-группа" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("созданная группа не видна в Groups()")
		}
		_ = store.Save()
		s2 := &StateStore{path: store.path}
		s2.load()
		f2 := false
		for _, g := range s2.Groups() {
			if g == "at-пустая-группа" {
				f2 = true
			}
		}
		if !f2 {
			return fmt.Errorf("группа потерялась после save/load")
		}
		if err := store.AddGroup("at-пустая-группа"); err == nil {
			return fmt.Errorf("дубликат группы должен отклоняться")
		}
		if err := store.AddGroup(""); err == nil {
			return fmt.Errorf("пустое имя должно отклоняться")
		}
		store.RemoveGroup("at-пустая-группа")
		for _, g := range store.Groups() {
			if g == "at-пустая-группа" {
				return fmt.Errorf("группа не удалена")
			}
		}
		return nil
	})

	atCheck("state: имя хоста (DisplayName) и UpdateHost", func() error {
		if err := store.AddHostNamed("10.9.9.9", "at-web", "Тест"); err != nil {
			return err
		}
		h := store.FindHost("10.9.9.9")
		if h == nil || h.DisplayName() != "at-web" {
			return fmt.Errorf("имя хоста не сохранилось: %+v", h)
		}
		h2 := store.FindHost("10.10.0.1")
		if h2 != nil && h2.Name == "" && h2.DisplayName() != h2.IP {
			return fmt.Errorf("без имени должен показываться IP")
		}
		if err := store.UpdateHost("10.9.9.9", "10.9.9.10", "at-web-2", "Прод"); err != nil {
			return err
		}
		if store.FindHost("10.9.9.9") != nil {
			return fmt.Errorf("старый адрес остался после UpdateHost")
		}
		hn := store.FindHost("10.9.9.10")
		if hn == nil || hn.Name != "at-web-2" || hn.Group != "Прод" {
			return fmt.Errorf("UpdateHost не применился: %+v", hn)
		}
		// дубликат адреса должен отклоняться
		if err := store.AddHostNamed("10.9.9.99", "занят", "Тест"); err != nil {
			return err
		}
		if err := store.UpdateHost("10.9.9.10", "10.9.9.99", "x", ""); err == nil {
			return fmt.Errorf("переход на занятый адрес должен отклоняться")
		}
		store.RemoveHosts([]string{"10.9.9.99"})
		store.RemoveHosts([]string{"10.9.9.10"})
		return nil
	})

	atCheck("state: RenameGroup переносит хосты", func() error {
		if err := store.AddGroup("at-stara"); err != nil {
			return err
		}
		if err := store.AddHostNamed("10.9.8.7", "", "at-stara"); err != nil {
			return err
		}
		if err := store.RenameGroup("at-stara", "at-nova"); err != nil {
			return err
		}
		h := store.FindHost("10.9.8.7")
		if h == nil || h.Group != "at-nova" {
			return fmt.Errorf("хост не переехал в переименованную группу: %+v", h)
		}
		seen := false
		for _, g := range store.Groups() {
			if g == "at-stara" {
				return fmt.Errorf("старое имя группы осталось")
			}
			if g == "at-nova" {
				seen = true
			}
		}
		if !seen {
			return fmt.Errorf("новое имя группы не в списке")
		}
		// «Общие» переименовывать нельзя
		if err := store.RenameGroup("Общие", "x"); err == nil {
			return fmt.Errorf("переименование «Общие» должно отклоняться")
		}
		store.RemoveHosts([]string{"10.9.8.7"})
		store.RemoveGroup("at-nova")
		return nil
	})

	atCheck("remmina: разбор .remmina-файла", func() error {
		dir, err := os.MkdirTemp("", "ra-remina")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		content := "[remmina]\nname=Сервер приложений\nprotocol=SSH\nserver=192.168.7.7:2222\ngroup=Прод\nusername=admin\n"
		if err := os.WriteFile(dir+"/srv.remmina", []byte(content), 0644); err != nil {
			return err
		}
		hosts, skipped, err := parseRemminaDir(dir)
		if err != nil {
			return err
		}
		if len(hosts) != 1 || skipped != 0 {
			return fmt.Errorf("найдено %d хостов, хочу 1", len(hosts))
		}
		if hosts[0].IP != "192.168.7.7:2222" || hosts[0].Name != "Сервер приложений" || hosts[0].Group != "Прод" {
			return fmt.Errorf("неверный разбор: %+v", hosts[0])
		}
		// RDP: порт должен отбрасываться
		content2 := "[remmina]\nname=Терминал\nprotocol=RDP\nserver=10.0.0.5:3389\n"
		_ = os.WriteFile(dir+"/rdp.remmina", []byte(content2), 0644)
		hosts2, _, _ := parseRemminaDir(dir)
		if len(hosts2) != 2 {
			return fmt.Errorf("после добавления RDP найдено %d", len(hosts2))
		}
		for _, h := range hosts2 {
			if h.Name == "Терминал" && h.IP != "10.0.0.5" {
				return fmt.Errorf("порт RDP должен отбрасываться: %s", h.IP)
			}
		}
		return nil
	})

	atCheck("import: CSV со вторым столбцом-именем", func() error {
		hosts := parseHostListText("10.5.3.1,web-a\n10.5.3.2,web-b\n")
		if len(hosts) != 2 {
			return fmt.Errorf("хостов=%d", len(hosts))
		}
		if hosts[0].Name != "web-a" || hosts[1].Name != "web-b" {
			return fmt.Errorf("имена из CSV не разобраны: %+v", hosts)
		}
		return nil
	})

	atCheck("import: разбор простого списка хостов", func() error {
		hosts := parseHostListText("# комментарий\n10.5.0.1\n10.5.0.2:2222\n[webfarm]\n10.5.1.1\nweb3:10.5.1.2\n10.5.2.1,просто комментарий\n")
		if len(hosts) != 5 {
			return fmt.Errorf("хостов=%d, хочу 5: %+v", len(hosts), hosts)
		}
		if hosts[0].Group != "Общие" {
			return fmt.Errorf("до секции группа должна быть «Общие»: %+v", hosts[0])
		}
		if hosts[1].IP != "10.5.0.2:2222" {
			return fmt.Errorf("порт потерян: %+v", hosts[1])
		}
		if hosts[2].Group != "webfarm" || hosts[2].IP != "10.5.1.1" {
			return fmt.Errorf("секция [webfarm] не применилась: %+v", hosts[2])
		}
		if hosts[3].Group != "web3" || hosts[3].IP != "10.5.1.2" {
			return fmt.Errorf("явный префикс группы потерян: %+v", hosts[3])
		}
		if hosts[4].IP != "10.5.2.1" {
			return fmt.Errorf("CSV-колонка: %+v", hosts[4])
		}
		return nil
	})

	atCheck("import: detectAndParse INI/YAML/plain", func() error {
		dir, err := os.MkdirTemp("", "ra_import")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		mk := func(name, content string) string {
			p := filepath.Join(dir, name)
			_ = os.WriteFile(p, []byte(content), 0644)
			return p
		}
		hosts, kind, err := detectAndParse(mk("inv.ini", "[web]\n10.6.0.1 ansible_host=10.6.0.1\n"))
		if err != nil || len(hosts) != 1 || hosts[0].Group != "web" || kind != "Ansible INI" {
			return fmt.Errorf("INI: %d %q %v %+v", len(hosts), kind, err, hosts)
		}
		hosts, kind, err = detectAndParse(mk("inv.yaml", "all:\n  children:\n    db:\n      hosts:\n        10.7.0.1:\n          ansible_host: 10.7.0.1\n"))
		if err != nil || len(hosts) != 1 || hosts[0].Group != "db" || hosts[0].IP != "10.7.0.1" {
			return fmt.Errorf("YAML: %d %q %v %+v", len(hosts), kind, err, hosts)
		}
		hosts, kind, err = detectAndParse(mk("hosts.txt", "10.8.0.1\n10.8.0.2\n"))
		if err != nil || len(hosts) != 2 || hosts[0].Group != "Общие" || kind != "список хостов" {
			return fmt.Errorf("plain: %d %q %v %+v", len(hosts), kind, err, hosts)
		}
		return nil
	})

	atCheck("шаблоны: пресеты при первом запуске", func() error {
		store.State.Templates = nil
		store.State.PresetsSeeded = false
		seedPresetTemplates()
		if len(store.State.Templates) < 5 {
			return fmt.Errorf("пресетов=%d, хочу ≥5", len(store.State.Templates))
		}
		if store.TemplateByName("Обновление системы") == nil {
			return fmt.Errorf("пресет «Обновление системы» не найден")
		}
		return nil
	})

	atCheck("журнал: фильтр ошибок и привязка строки", func() error {
		before := len(store.State.Reports)
		addReport("at-фильтр", "10.9.9.9", "OK", "всё хорошо")
		addReport("at-фильтр", "10.9.9.10", "FAIL", "что-то сломалось")
		if len(store.State.Reports) != before+2 {
			return fmt.Errorf("отчёты не добавлены")
		}
		done := make(chan struct{})
		glibIdle(func() {
			app.repUI.curFlt = 1 // только ошибки/предупреждения
			app.repUI.refresh()
			close(done)
		})
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			return fmt.Errorf("refresh завис")
		}
		n := rowCount(app.repUI.st)
		glibIdle(func() {
			app.repUI.curFlt = 0
			app.repUI.refresh()
		})
		if n < 1 {
			return fmt.Errorf("фильтр «только ошибки» показал %d строк", n)
		}
		return nil
	})

	// ---------- UI pages ----------
	atCheck("ui: все 8 страниц собраны", func() error {
		if len(app.navBtns) != len(navItems) {
			return fmt.Errorf("navBtns=%d, хочу %d", len(app.navBtns), len(navItems))
		}
		done := make(chan struct{})
		glibIdle(func() {
			for _, it := range navItems {
				app.activateNav(it.id)
			}
			app.activateNav("hosts")
			close(done)
		})
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			return fmt.Errorf("переключение страниц зависло")
		}
		return nil
	})

	atCheck("ui: host filter (Ctrl+F) поиск", func() error {
		glibIdle(func() { app.hostsUI.search.SetText("10.99.0.2") })
		time.Sleep(120 * time.Millisecond)
		n := rowCount(app.hostsUI.hostStore)
		glibIdle(func() { app.hostsUI.search.SetText("") })
		time.Sleep(80 * time.Millisecond)
		return atWant(n == 1, "фильтр должен оставить 1 строку, показано %d", n)
	})

	atCheck("ui: sftp-список хостов", func() error {
		glibIdle(func() { app.sftpUI.refreshHosts() })
		time.Sleep(120 * time.Millisecond)
		n := app.sftpUI.hostCount
		return atWant(n >= 3, "в селекторе SFTP должно быть ≥3 хостов, есть %d", n)
	})

	// ---------- SSH ----------
	creds := buildCreds(hostAddr)
	var client *ssh.Client
	atCheck("ssh: подключение (password)", func() error {
		cl, err := Connect(creds)
		if err != nil {
			return err
		}
		client = cl
		return nil
	})
	if client == nil {
		return // everything below needs the connection
	}
	defer client.Close()

	atCheck("ssh: exec echo", func() error {
		out, err := Exec(client, "echo hello-ra", 10)
		if err != nil {
			return err
		}
		return atWant(strings.TrimSpace(out) == "hello-ra", "неожиданный вывод: %q", out)
	})

	atCheck("ssh: код выхода команды", func() error {
		_, err := Exec(client, "exit 3", 10)
		return atWant(err != nil, "ожидалась ошибка ненулевого кода выхода")
	})

	atCheck("ssh: exec root через su - root -c", func() error {
		out, err := ExecRoot(client, "echo root-ok", "autotest-root", 10)
		if err != nil {
			return err
		}
		return atWant(strings.TrimSpace(out) == "root-ok", "неожиданный вывод: %q", out)
	})

	atCheck("ssh: несколько команд на одном соединении", func() error {
		for i := 0; i < 5; i++ {
			out, err := Exec(client, fmt.Sprintf("echo n%d", i), 10)
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) != fmt.Sprintf("n%d", i) {
				return fmt.Errorf("команда %d вернула %q", i, out)
			}
		}
		return nil
	})

	// ---------- playbook tasks ----------
	atCheck("playbook: задача «команда»", func() error {
		t := PlaybookTask{Type: "command", Source: "echo task-ok"}
		out, err := execTask(client, hostAddr, t, creds)
		if err != nil {
			return err
		}
		return atWant(strings.Contains(out, "task-ok"), "нет вывода задачи: %q", out)
	})

	atCheck("playbook: задача «скрипт» (загрузка+запуск)", func() error {
		dir, _ := os.MkdirTemp("", "ra_script")
		defer os.RemoveAll(dir)
		script := filepath.Join(dir, "install.sh")
		_ = os.WriteFile(script, []byte("#!/bin/sh\necho script-ran-ok\n"), 0755)
		t := PlaybookTask{Type: "script", Source: script}
		out, err := execTask(client, hostAddr, t, creds)
		if err != nil {
			return err
		}
		return atWant(strings.Contains(out, "script-ran-ok"), "скрипт не выполнен: %q", out)
	})

	atCheck("playbook: задача «копировать»", func() error {
		dir, _ := os.MkdirTemp("", "ra_copy")
		defer os.RemoveAll(dir)
		src := filepath.Join(dir, "app.conf")
		_ = os.WriteFile(src, []byte("key=value\n"), 0644)
		t := PlaybookTask{Type: "copy", Source: src, Dest: "/tmp/ra_autotest_copy.conf"}
		if _, err := execTask(client, hostAddr, t, creds); err != nil {
			return err
		}
		data, err := os.ReadFile("/tmp/ra_autotest_copy.conf")
		if err != nil {
			return fmt.Errorf("файл не появился на «удалённой» стороне: %v", err)
		}
		return atWant(string(data) == "key=value\n", "содержимое не совпало")
	})

	atCheck("playbook: задача «заменить + команды»", func() error {
		dir, _ := os.MkdirTemp("", "ra_repl")
		defer os.RemoveAll(dir)
		src := filepath.Join(dir, "new.conf")
		_ = os.WriteFile(src, []byte("replaced-content\n"), 0644)
		t := PlaybookTask{Type: "replace_with_cmds", Source: src, Dest: "/tmp/ra_autotest_repl.conf",
			PostCommands: []string{"echo post1", "echo post2"}}
		out, err := execTask(client, hostAddr, t, creds)
		if err != nil {
			return err
		}
		return atWant(strings.Contains(out, "post1") && strings.Contains(out, "post2"), "пост-команды не выполнены: %q", out)
	})

	atCheck("playbook: задача «правка» (sed)", func() error {
		Exec(client, "printf 'a=1\\n' > /tmp/ra_autotest_edit.conf", 10)
		t := PlaybookTask{Type: "edit", Source: "/tmp/ra_autotest_edit.conf", Search: "a=1", Replace: "a=42"}
		out, err := execTask(client, hostAddr, t, creds)
		if err != nil {
			return err
		}
		got, _ := Exec(client, "cat /tmp/ra_autotest_edit.conf", 10)
		return atWant(strings.Contains(got, "a=42"), "sed-правка не применилась: %q / %q", out, got)
	})

	// ---------- SFTP ----------
	var sftpCl *SFTPClient
	atCheck("sftp: подключение", func() error {
		cl, err := OpenSFTP(buildCreds(hostAddr))
		if err != nil {
			return err
		}
		sftpCl = cl
		return nil
	})
	if sftpCl != nil {
		defer sftpCl.Close()

		base := "/tmp/ra_sftp_test"
		Exec(client, "rm -rf "+shellQuote(base), 10)
		Exec(client, "mkdir -p "+shellQuote(base), 10)

		atCheck("sftp: чтение каталога", func() error {
			Exec(client, "touch "+shellQuote(base+"/marker.txt"), 10)
			infos, err := sftpCl.ListDir(base)
			if err != nil {
				return err
			}
			for _, fi := range infos {
				if fi.Name() == "marker.txt" {
					return nil
				}
			}
			return fmt.Errorf("marker.txt не виден в каталоге (%d записей)", len(infos))
		})

		atCheck("sftp: MkdirAllP глубокий каталог", func() error {
			return sftpCl.MkdirAllP(base + "/a/b/c")
		})

		atCheck("sftp: загрузка файла", func() error {
			dir, _ := os.MkdirTemp("", "ra_up")
			defer os.RemoveAll(dir)
			lf := filepath.Join(dir, "hello.txt")
			payload := strings.Repeat("RA-TEST-", 3000) // 24 КБ
			_ = os.WriteFile(lf, []byte(payload), 0644)
			if _, err := sftpCl.Upload(lf, base+"/hello.txt"); err != nil {
				return err
			}
			out, _ := Exec(client, "wc -c < "+shellQuote(base+"/hello.txt"), 10)
			return atWant(strings.TrimSpace(out) == fmt.Sprint(len(payload)), "размер на сервере %s, хочу %d", out, len(payload))
		})

		atCheck("sftp: скачивание файла (сравнение байт)", func() error {
			dir, _ := os.MkdirTemp("", "ra_dn")
			defer os.RemoveAll(dir)
			dst := filepath.Join(dir, "hello2.txt")
			if _, err := sftpCl.Download(base+"/hello.txt", dst); err != nil {
				return err
			}
			a, _ := os.ReadFile(base + "/hello.txt")
			b, err := os.ReadFile(dst)
			if err != nil {
				return err
			}
			return atWant(string(a) == string(b), "содержимое различается")
		})

		atCheck("sftp: рекурсивная загрузка каталога", func() error {
			dir, _ := os.MkdirTemp("", "ra_tree")
			defer os.RemoveAll(dir)
			_ = os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0755)
			_ = os.WriteFile(filepath.Join(dir, "f1.txt"), []byte("one"), 0644)
			_ = os.WriteFile(filepath.Join(dir, "sub", "f2.txt"), []byte("two"), 0644)
			_ = os.WriteFile(filepath.Join(dir, "sub", "deep", "f3.txt"), []byte("three"), 0644)
			n, err := sftpCl.UploadPath(dir, base+"/tree")
			if err != nil {
				return err
			}
			if n != 11 {
				return fmt.Errorf("передано %d байт, хочу 11 (one+two+three)", n)
			}
			out, _ := Exec(client, "cat "+shellQuote(base+"/tree/sub/deep/f3.txt"), 10)
			return atWant(strings.TrimSpace(out) == "three", "глубокий файл не совпал: %q", out)
		})

		atCheck("sftp: рекурсивное скачивание каталога", func() error {
			dir, _ := os.MkdirTemp("", "ra_rdn")
			defer os.RemoveAll(dir)
			if _, err := sftpCl.DownloadPath(base+"/tree", filepath.Join(dir, "tree")); err != nil {
				return err
			}
			b, err := os.ReadFile(filepath.Join(dir, "tree", "sub", "deep", "f3.txt"))
			if err != nil {
				return fmt.Errorf("глубокий файл не скачался: %v", err)
			}
			return atWant(string(b) == "three", "содержимое %q", string(b))
		})

		atCheck("sftp: переименование", func() error {
			return sftpCl.SFTP.Rename(base+"/hello.txt", base+"/hello-renamed.txt")
		})

		atCheck("sftp: удаление дерева (RemoveAllP)", func() error {
			if err := sftpCl.RemoveAllP(base + "/tree"); err != nil {
				return err
			}
			_, err := sftpCl.SFTP.Stat(base + "/tree")
			return atWant(err != nil, "каталог должен исчезнуть")
		})

		atCheck("sftp: ошибка на несуществующем файле", func() error {
			_, err := sftpCl.Download(base+"/no-such-file", "/tmp/ra_must_fail")
			return atWant(err != nil, "ожидалась ошибка")
		})
	}

	// ---------- reports / templates / chains ----------
	atCheck("журнал: запись отчётов", func() error {
		before := len(store.State.Reports)
		addReport("autotest", hostAddr, "OK", "проверка журнала")
		return atWant(len(store.State.Reports) == before+1, "отчёт не добавлен")
	})

	atCheck("шаблоны: сохранение и поиск", func() error {
		store.State.Templates = append(store.State.Templates, Template{
			Name: "at-tpl", Description: "тест",
			Tasks: []PlaybookTask{{Type: "command", Source: "echo tpl"}},
		})
		if store.TemplateByName("at-tpl") == nil {
			return fmt.Errorf("шаблон не найден")
		}
		return nil
	})

	atCheck("цепочки: структура шагов", func() error {
		c := Chain{Name: "at-chain", Steps: []ChainStep{
			{Template: "at-tpl", OnSuccess: "continue", OnFailure: "stop"},
			{Template: "at-tpl", OnSuccess: "continue", OnFailure: "rollback", Rollback: []string{"echo rollback"}},
		}}
		store.State.Chains = append(store.State.Chains, c)
		got := store.ChainByName("at-chain")
		return atWant(got != nil && len(got.Steps) == 2 && got.Steps[1].OnFailure == "rollback", "цепочка не сохранилась")
	})

	atCheck("describe: описания задач", func() error {
		t := PlaybookTask{Type: "script", Source: "/opt/very/long/path/deploy.sh"}
		d := t.Describe()
		return atWant(strings.Contains(d, "deploy.sh"), "описание без имени файла: %s", d)
	})

	atCheck("cron: разбор выражений", func() error {
		f, err := parseCron("*/15 * * * *")
		if err != nil {
			return err
		}
		if len(f[0]) != 4 || (f[0][0] != 0 || f[0][1] != 15 || f[0][2] != 30 || f[0][3] != 45) {
			return atWant(false, "*/15 минут неверны: %v", f[0])
		}
		if _, err := parseCron("30 3 * * 1-5"); err != nil {
			return err
		}
		if _, err := parseCron("0 9 * * пн,ср"); err != nil {
			return err
		}
		if _, err := parseCron("плохое выражение"); err == nil {
			return atWant(false, "неверное выражение принято")
		}
		return nil
	})

	atCheck("cron: следующий запуск", func() error {
		base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local) // понедельник
		t1, err := cronNext("*/15 * * * *", base)
		if err != nil {
			return err
		}
		if w := t1.Sub(base); w != 15*time.Minute {
			return atWant(false, "*/15: интервал %v", w)
		}
		t2, err := cronNext("30 3 * * *", base)
		if err != nil {
			return err
		}
		if t2.Format("02.01 15:04") != "03.11 03:30" {
			return atWant(false, "30 3 * * *: %s", t2.Format("02.01 15:04"))
		}
		t3, err := cronNext("0 9 * * 1-5", base)
		if err != nil {
			return err
		}
		if t3.Format("02.01 15:04") != "03.11 09:00" {
			return atWant(false, "будни 09:00: %s", t3.Format("02.01 15:04"))
		}
		return nil
	})

	atCheck("cron: человекочитаемая расшифровка", func() error {
		h := cronHuman("*/15 * * * *")
		return atWant(strings.Contains(h, "15"), "расшифровка */15: %s", h)
	})

	atCheck("расписания: хелперы state", func() error {
		store.State.Schedules = append(store.State.Schedules, Schedule{
			Name: "at-sched", Template: "at-tpl", Group: "web", Cron: "*/5 * * * *", Enabled: true})
		got := store.ScheduleByName("at-sched")
		if got == nil || got.Cron != "*/5 * * * *" {
			return atWant(false, "расписание не найдено")
		}
		store.RemoveSchedule("at-sched")
		if store.ScheduleByName("at-sched") != nil {
			return atWant(false, "расписание не удалилось")
		}
		return nil
	})

	atCheck("теги и избранное: state", func() error {
		store.AddHostNamed("10.9.9.9", "at-tagged", "web")
		store.SetTags("10.9.9.9", []string{"prod", " web "})
		h := store.FindHost("10.9.9.9")
		if h == nil || len(h.Tags) != 2 || h.Tags[0] != "prod" {
			return atWant(false, "метки не сохранились: %v", h)
		}
		if !hostMatchesFilter(*h, "prod") || !hostMatchesFilter(*h, "#web") {
			return atWant(false, "фильтр по меткам не сработал")
		}
		if store.ToggleFav("10.9.9.9") != true || !h.Fav {
			return atWant(false, "избранное не переключилось")
		}
		store.RemoveHosts([]string{"10.9.9.9"})
		return nil
	})

	atCheck("мастер-шифрование: seal/unseal", func() error {
		plain := []byte(`{"hosts":[{"ip":"10.1.1.1"}],"version":2}`)
		sealed, err := masterSeal(plain, "тесто123")
		if err != nil {
			return err
		}
		if !stateFileIsEncrypted(sealed) {
			return atWant(false, "заголовок шифровки не распознан")
		}
		open, err := masterUnseal(sealed, "тесто123")
		if err != nil {
			return err
		}
		if string(open) != string(plain) {
			return atWant(false, "содержимое изменилось после seal/unseal")
		}
		if _, err := masterUnseal(sealed, "другой пароль"); err == nil {
			return atWant(false, "чужой пароль подошёл!")
		}
		return nil
	})

	atCheck("webhook: сборка события", func() error {
		ev := HookEvent{Event: "task.finished", Task: "at", OK: 3, Failed: 1, Total: 4, Status: "fail", Hosts: []string{"a", "b"}}
		txt := hookEventText(ev)
		if !strings.Contains(txt, "at") || !strings.Contains(txt, "a, b") {
			return atWant(false, "текст события неверный: %s", txt)
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return atWant(strings.Contains(string(data), "\"status\""), "JSON события без status: %s", string(data))
	})

	atCheck("сравнение панелей: расчёт diff", func() error {
		// локальный каталог с файлом и подкаталогом
		tmp, err := os.MkdirTemp("", "at-diff-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("AAA"), 0644); err != nil {
			return err
		}
		_ = os.Mkdir(filepath.Join(tmp, "sub"), 0755)
		_ = os.WriteFile(filepath.Join(tmp, "sub", "b.txt"), []byte("BBBB"), 0644)
		local := map[string]*panelEntry{}
		if err := walkLocalList(tmp, 0, local); err != nil {
			return err
		}
		return atWant(local["a.txt"] != nil && local["sub/b.txt"] != nil,
			"рекурсивный обход локального каталога: %v", local)
	})

	// restore the hosts the user sees in GUI tests
	store.State.Hosts = []HostEntry{
		{IP: "10.0.0.1", Group: "web"},
		{IP: "10.0.0.2", Group: "web"},
		{IP: "10.0.0.3", Group: "Общие"},
	}
	_ = store.Save()
	glibIdle(func() { app.hostsUI.refreshAll() })
}
