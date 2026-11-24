package main

// safe.go (v1.13.0) — защита от вылетов.
//
// Жалоба v1.12.0: «программа периодически сама по себе вылетает».
// Причина: неперехваченная паника в ЛЮБОЙ горутине убивает весь процесс —
// восстановиться в Go из чужой горутины нельзя. Решение:
//   1. crashRecover вызывается через defer в каждой фоновой горутине
//      (SSH-задачи, планировщик, SFTP, монитор, терминалы): сбой пишется
//      в журнал ~/.config/RemoteAdmin/crash.log со стеком и в журнал
//      событий, приложение продолжает работать.
//   2. goSafe — обёртка для запуска горутин с такой защитой.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

var crashMu sync.Mutex

// crashLogFile — путь к журналу сбоев (рядом с state.json).
func crashLogFile() string {
	if store != nil && store.path != "" {
		return filepath.Join(filepath.Dir(store.path), "crash.log")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "RemoteAdmin", "crash.log")
}

// crashRecover перехватывает панику горутины: стек — в crash.log,
// короткая строка — в журнал событий. Вызывать ТОЛЬКО через defer.
func crashRecover(where string) {
	if r := recover(); r != nil {
		stamp := time.Now().Format("2006-01-02 15:04:05")
		dump := fmt.Sprintf("[%s] panic in %s:\n%v\n\n%s\n---\n", stamp, where, r, debug.Stack())
		crashMu.Lock()
		_ = os.MkdirAll(filepath.Dir(crashLogFile()), 0755)
		f, err := os.OpenFile(crashLogFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = f.WriteString(dump)
			_ = f.Close()
		}
		crashMu.Unlock()
		logf("fail", "⚠ Сбой в «%s» перехвачен — приложение продолжает работу (стек: crash.log)", where)
	}
}

// goSafe запускает горутину с перехватом паник.
func goSafe(where string, fn func()) {
	go func() {
		defer crashRecover(where)
		fn()
	}()
}
