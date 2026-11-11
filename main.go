package main

import (
        "fmt"
        "os"
        "strings"

        "github.com/gotk3/gotk3/gtk"
)

func main() {
        // CLI args
        for _, a := range os.Args[1:] {
                switch a {
                case "--version", "-v":
                        fmt.Printf("%s %s\n", AppName, AppVersion)
                        return
                case "--help", "-h":
                        fmt.Printf("%s %s — SSH-менеджер группы хостов\n", AppName, AppVersion)
                        fmt.Println("  --selftest   запустить самопроверку интерфейса (headless)")
                        fmt.Println("  --version    показать версию")
                        return
                }
        }

        selftest := false
        autotest := false
        shotDir := ""
        shotDialogs := ""
        for _, a := range os.Args[1:] {
                if a == "--selftest" {
                        selftest = true
                }
                if a == "--autotest" {
                        autotest = true
                }
                if strings.HasPrefix(a, "--shot=") {
                        shotDir = strings.TrimPrefix(a, "--shot=")
                }
                if strings.HasPrefix(a, "--shot-dialogs=") {
                        shotDialogs = strings.TrimPrefix(a, "--shot-dialogs=")
                }
        }

        if err := initStateStore(); err != nil {
                fmt.Fprintln(os.Stderr, "state:", err)
                os.Exit(1)
        }
        initKnownHosts()

        // RA_DEMO_SSH=1 — встроенный демо-хост (принимает любые логин/пароль):
        // позволяет попробовать SSH/SFTP без реального сервера.
        if os.Getenv("RA_DEMO_SSH") == "1" {
                var srv *fakeSSHServer
                var err error
                for _, p := range []int{2222, 2223, 2224, 2225} {
                        srv, err = startFakeSSHServerOn(fmt.Sprintf("127.0.0.1:%d", p), "*", "")
                        if err == nil {
                                break
                        }
                }
                if srv != nil {
                        demoHost := fmt.Sprintf("127.0.0.1:%d", srv.port)
                        if store.FindHost(demoHost) == nil {
                                _ = store.AddHost(demoHost, "Демо")
                        }
                        fmt.Println("demo ssh host:", demoHost)
                }
        }

        gtk.Init(nil)
        if err := buildApp(selftest); err != nil {
                fmt.Fprintln(os.Stderr, "ui:", err)
                os.Exit(1)
        }

        // хранилище зашифровано мастер-паролем — спросить его сразу после
        // появления окна (до первого обращения к данным в UI)
        if pendingUnlock {
                glibIdle(func() {
                        if !unlockDialog() {
                                appQuit()
                                return
                        }
                        // данные загрузились — обновить все вкладки
                        app.refreshAfterUnlock()
                        startScheduler()
                })
        } else {
                startScheduler()
        }

        if selftest {
                logf("ok", "Selftest: интерфейс собран, хостов в state: %d", len(store.State.Hosts))
                fmt.Println("SELFTEST OK")
        }
        if shotDir != "" {
                _ = os.MkdirAll(shotDir, 0755)
                glibIdle(func() { app.captureSeq(shotDir, 0) })
        }
        if shotDialogs != "" {
                _ = os.MkdirAll(shotDialogs, 0755)
                glibIdle(func() { captureDialogs(shotDialogs) })
        }
        if autotest {
                runAutotest()
        }
        gtk.Main()

        if err := store.Save(); err != nil {
                fmt.Fprintln(os.Stderr, "save:", err)
        }
        if autotest && atFailures > 0 {
                os.Exit(1)
        }
}
