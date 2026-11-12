package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// ---------- шифрование всего хранилища мастер-паролем ----------
//
// По умолчанию state.json шифрует только пароли ключом машины
// (secret.key). Мастер-пароль — опциональный режим: ВЕСЬ файл
// шифруется AES-256-GCM, ключ выводится из пароля через scrypt,
// соль хранится в заголовке файла. Без пароля файл бесполезен,
// и с другой машины его тоже не открыть без знания пароля.
//
// Формат: RAENC1:<base64_salt>:<base64_ciphertext>

const masterEncPrefix = "RAENC1:"

var (
	masterPass     string // активный мастер-пароль (пуст = шифрование выключено)
	stateEncrypted bool   // файл на диске зашифрован
	pendingUnlock  bool   // файл зашифрован, пароль ещё не введён
	encryptedRaw   []byte // сырой зашифрованный контент до разблокировки
)

func masterKey(password, salt []byte) ([]byte, error) {
	return scrypt.Key(password, salt, 1<<15, 8, 1, 32)
}

// masterSeal шифрует данные мастер-паролем (с новой солью).
func masterSeal(data []byte, password string) ([]byte, error) {
	if password == "" {
		return nil, errors.New("пустой мастер-пароль")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	key, err := masterKey([]byte(password), salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nonce, nonce, data, salt) // соль — как AAD
	out := masterEncPrefix + base64.StdEncoding.EncodeToString(salt) +
		":" + base64.StdEncoding.EncodeToString(ct)
	return []byte(out), nil
}

// masterUnseal расшифровывает контент state.json.
func masterUnseal(raw []byte, password string) ([]byte, error) {
	s := strings.TrimPrefix(string(raw), masterEncPrefix)
	if s == string(raw) {
		return nil, errors.New("файл не зашифрован мастер-паролем")
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return nil, errors.New("повреждённый формат хранилища")
	}
	salt, err1 := base64.StdEncoding.DecodeString(parts[0])
	ct, err2 := base64.StdEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return nil, errors.New("повреждённый формат хранилища")
	}
	key, err := masterKey([]byte(password), salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < gcm.NonceSize() {
		return nil, errors.New("повреждённый формат хранилища")
	}
	nonce, body := ct[:gcm.NonceSize()], ct[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, body, salt)
	if err != nil {
		return nil, errors.New("неверный мастер-пароль")
	}
	return pt, nil
}

// stateFileIsEncrypted проверяет заголовок файла.
func stateFileIsEncrypted(raw []byte) bool {
	return strings.HasPrefix(string(raw), masterEncPrefix)
}

// enableMasterEncryption включаает шифрование всего state.json.
// Пароль запрашивается дважды (проверка), после чего файл переписывается.
func enableMasterEncryption(password string) error {
	if len(password) < 4 {
		return errors.New("мастер-пароль короче 4 символов — так небезопасно")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	st := store.State
	data, err := jsonMarshalIndent(&st)
	if err != nil {
		return err
	}
	sealed, err := masterSeal(data, password)
	if err != nil {
		return err
	}
	tmp := store.path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, store.path); err != nil {
		return err
	}
	masterPass = password
	stateEncrypted = true
	pendingUnlock = false
	encryptedRaw = nil
	return nil
}

// disableMasterEncryption выключает шифрование (нужен текущий пароль).
func disableMasterEncryption(password string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, err := os.ReadFile(store.path)
	if err != nil {
		return err
	}
	if !stateFileIsEncrypted(raw) {
		// уже расшифрован — просто сбросить состояние
		masterPass = ""
		stateEncrypted = false
		return nil
	}
	data, err := masterUnseal(raw, password)
	if err != nil {
		return err
	}
	tmp := store.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, store.path); err != nil {
		return err
	}
	masterPass = ""
	stateEncrypted = false
	pendingUnlock = false
	return nil
}

// tryUnlockStore расшифровывает хранилище введённым паролем.
// При успехе пароль запоминается до конца сессии (для Save).
func tryUnlockStore(password string) error {
	if len(encryptedRaw) == 0 {
		return errors.New("нет зашифрованных данных")
	}
	data, err := masterUnseal(encryptedRaw, password)
	if err != nil {
		return err
	}
	store.mu.Lock()
	store.applyLoadedJSON(data)
	store.mu.Unlock()
	masterPass = password
	stateEncrypted = true
	pendingUnlock = false
	encryptedRaw = nil
	return nil
}

// jsonMarshalIndent вынесен для тестов (без отражения в state.go).
func jsonMarshalIndent(v interface{}) ([]byte, error) {
	return jsonMarshal(v)
}

// masterStatusText — строка для настроек.
func masterStatusText() string {
	switch {
	case stateEncrypted && masterPass != "":
		return "Включено: state.json шифруется мастер-паролем (AES-256-GCM + scrypt)"
	case stateEncrypted && masterPass == "":
		return "Файл зашифрован — дождитесь разблокировки"
	default:
		return "Выключено: шифруются только пароли (ключ привязан к машине)"
	}
}

var _ = fmt.Sprintf
var _ = sha256.Sum256
