package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ================= known_hosts handling =================

// knownHostsPath returns the app-local known_hosts file path.
func knownHostsPath() string {
	dir, err := configDir()
	if err == nil {
		return filepath.Join(dir, "known_hosts")
	}
	return "/tmp/remoteadmin_known_hosts"
}

type knownHostsFile struct {
	mu    sync.Mutex
	hosts map[string]string // "host:port" -> fingerprint (SHA256:...)
}

var kh *knownHostsFile

func initKnownHosts() {
	kh = &knownHostsFile{hosts: map[string]string{}}
	// read app-local known_hosts (writable)
	kh.readInto(kh.hosts, knownHostsPath())
	// also read global ~/.ssh/known_hosts into a second map (read-only use)
}

func (k *knownHostsFile) readInto(m map[string]string, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// format: [marker] hostpatterns keytype base64key
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		idx := 0
		if strings.HasPrefix(fields[0], "@") {
			idx = 1 // @cert-authority / @revoked — skip marker
			if len(fields) < 4 {
				continue
			}
		}
		patterns := fields[idx]
		keytype := fields[idx+1]
		keydata := fields[idx+2]
		keyBytes, err := base64.StdEncoding.DecodeString(keydata)
		if err != nil {
			continue
		}
		pub, err := ssh.ParsePublicKey(keyBytes)
		if err != nil {
			continue
		}
		_ = keytype
		fp := fpSHA256(pub)
		for _, hp := range strings.Split(patterns, ",") {
			h := normalizeHostPattern(hp)
			if h != "" {
				m[h] = fp
			}
		}
	}
}

func normalizeHostPattern(hp string) string {
	if strings.HasPrefix(hp, "[") && strings.HasSuffix(hp, "]:22") {
		return strings.Trim(hp, "[]") // [host]:22 -> host
	}
	return hp
}

func fpSHA256(key ssh.PublicKey) string {
	return ssh.FingerprintSHA256(key)
}

func crandRead(b []byte) (int, error) { return rand.Read(b) }

func sha1Hash(b []byte) []byte {
	s := sha1.Sum(b)
	return s[:]
}

// hashHost computes OpenSSH hashed known_hosts entry (|1|salt|hash) for hostname.
func hashHost(hostname string) (string, error) {
	salt := make([]byte, 20)
	if _, err := crandRead(salt); err != nil {
		return "", err
	}
	h := sha1Hash(append(append([]byte{}, salt...), []byte(hostname)...))
	return "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(h), nil
}

// lookupKnownHost checks both app-local and global known_hosts for hostname.
// Returns (fingerprint, found).
func lookupKnownHost(hostname string) (string, bool) {
	h := hostname
	if !strings.Contains(h, ":") {
		h = h + ":22"
	}
	kh.mu.Lock()
	defer kh.mu.Unlock()
	if fp, ok := kh.hosts[h]; ok {
		return fp, true
	}
	// try without port
	if fp, ok := kh.hosts[hostname]; ok {
		return fp, true
	}
	// global known_hosts (read-only) — parse lazily each call is fine for small files
	gm := map[string]string{}
	if home, err := os.UserHomeDir(); err == nil {
		kh.readInto(gm, filepath.Join(home, ".ssh", "known_hosts"))
	}
	for _, key := range []string{h, hostname} {
		if fp, ok := gm[key]; ok {
			return fp, ok
		}
	}
	// hashed entries in global file
	if home, err := os.UserHomeDir(); err == nil {
		if fp, ok := matchHashedGlobal(hostname, filepath.Join(home, ".ssh", "known_hosts")); ok {
			return fp, true
		}
	}
	return "", false
}

func matchHashedGlobal(hostname, path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "|1|") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		parts := strings.Split(fields[0], "|") // "", "1", salt, hash
		if len(parts) != 4 {
			continue
		}
		salt, err1 := base64.StdEncoding.DecodeString(parts[2])
		want, err2 := base64.StdEncoding.DecodeString(parts[3])
		if err1 != nil || err2 != nil {
			continue
		}
		got := sha1Hash(append(append([]byte{}, salt...), []byte(hostname)...))
		if bytes.Equal(got, want) {
			keyBytes, err := base64.StdEncoding.DecodeString(fields[2])
			if err != nil {
				continue
			}
			if pub, err := ssh.ParsePublicKey(keyBytes); err == nil {
				return fpSHA256(pub), true
			}
		}
	}
	return "", false
}

// appendKnownHost stores host key fingerprint in app-local known_hosts (hashed).
func appendKnownHost(hostname string, key ssh.PublicKey) error {
	kh.mu.Lock()
	defer kh.mu.Unlock()
	h, err := hashHost(hostname)
	if err != nil {
		return err
	}
	line := h + " " + key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal()) + "\n"
	f, err := os.OpenFile(knownHostsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	if !strings.Contains(hostname, ":") {
		kh.hosts[hostname+":22"] = fpSHA256(key)
	} else {
		kh.hosts[hostname] = fpSHA256(key)
	}
	return nil
}

// replaceKnownHost removes old entries for hostname and adds the new key.
func replaceKnownHost(hostname string, key ssh.PublicKey) error {
	kh.mu.Lock()
	defer kh.mu.Unlock()
	// rewrite app-local file without this host
	if data, err := os.ReadFile(knownHostsPath()); err == nil {
		var out []string
		for _, line := range strings.Split(string(data), "\n") {
			l := strings.TrimSpace(line)
			if l == "" {
				continue
			}
			fields := strings.Fields(l)
			if len(fields) < 3 {
				continue
			}
			hostPart := fields[0]
			if strings.HasPrefix(hostPart, "|1|") {
				// hashed: decode and compare
				parts := strings.Split(hostPart, "|")
				if len(parts) == 4 {
					salt, e1 := base64.StdEncoding.DecodeString(parts[2])
					want, e2 := base64.StdEncoding.DecodeString(parts[3])
					if e1 == nil && e2 == nil {
						got := sha1Hash(append(append([]byte{}, salt...), []byte(hostname)...))
						if bytes.Equal(got, want) {
							continue // drop
						}
					}
				}
				out = append(out, l)
			} else {
				norm := normalizeHostPattern(hostPart)
				if norm == hostname || norm == hostname+":22" || strings.HasPrefix(norm, hostname+",") {
					continue // drop
				}
				out = append(out, l)
			}
		}
		_ = os.WriteFile(knownHostsPath(), []byte(strings.Join(out, "\n")+"\n"), 0600)
	}
	kh.mu.Unlock()
	_ = appendKnownHost(hostname, key)
	kh.mu.Lock()
	return nil
}

// ================= SSH client =================

type SSHCreds struct {
	Host       string
	Port       int
	User       string
	Password   string
	KeyPath    string
	KeyPass    string
	RootPass   string // for su - root escalation
	StrictKey  string // ask|accept-new|no
	TimeoutSec int
}

// ErrHostKeyChanged is returned when host key differs from stored one.
var ErrHostKeyChanged = errors.New("host key changed")

var (
	hostKeyMu      sync.Mutex
	pendingKeyInfo hostKeyChange
)

type hostKeyChange struct {
	Host      string
	OldFP     string
	NewFP     string
	NewKeyRaw []byte
	Key       ssh.PublicKey
}

// HostKeyVerifier builds an ssh.HostKeyCallback honoring strict mode.
func HostKeyVerifier(strict string, uiConfirm func(hc *hostKeyChange) bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		host := hostname
		if !strings.Contains(host, ":") {
			host = fmt.Sprintf("%s:%d", hostname, remote.(*net.TCPAddr).Port)
		}
		old, found := lookupKnownHost(hostname)
		newFP := fpSHA256(key)
		if found && old != newFP {
			hostKeyMu.Lock()
			pendingKeyInfo = hostKeyChange{
				Host: host, OldFP: old, NewFP: newFP, NewKeyRaw: key.Marshal(), Key: key,
			}
			hc := &pendingKeyInfo
			hostKeyMu.Unlock()
			if strict == "no" {
				_ = replaceKnownHost(hostname, key)
				return nil
			}
			if uiConfirm == nil {
				return ErrHostKeyChanged
			}
			if uiConfirm(hc) {
				if err := replaceKnownHost(hostname, key); err != nil {
					return err
				}
				return nil
			}
			return ErrHostKeyChanged
		}
		if !found {
			if strict == "accept-new" || strict == "no" {
				_ = appendKnownHost(hostname, key)
				return nil
			}
			// "ask": store + log warning on first connect (TOFU with fingerprint log)
			_ = appendKnownHost(hostname, key)
			logf("warn", "🔑 Новый хост %s — ключ принят: %s", host, newFP)
			return nil
		}
		return nil
	}
}

// Connect establishes an SSH connection with retry and auth fallback:
// key (if configured) -> keyboard-interactive -> password.
func Connect(c SSHCreds) (*ssh.Client, error) {
	var lastErr error
	retries := 2
	if store != nil && store.State.Settings.Retries >= 0 {
		retries = store.State.Settings.Retries
	}
	timeout := 10 * time.Second
	if c.TimeoutSec > 0 {
		timeout = time.Duration(c.TimeoutSec) * time.Second
	}
	port := c.Port
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", c.Host, port)

	var auths []ssh.AuthMethod
	if c.KeyPath != "" {
		if signer, err := loadSigner(c.KeyPath, c.KeyPass); err == nil {
			auths = append(auths, ssh.PublicKeys(signer))
		} else {
			lastErr = fmt.Errorf("ключ %s: %w", c.KeyPath, err)
		}
	}
	if c.Password != "" {
		auths = append(auths, ssh.Password(c.Password))
		auths = append(auths, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = c.Password
			}
			return answers, nil
		}))
	}
	if len(auths) == 0 && lastErr == nil {
		lastErr = errors.New("нет способа аутентификации (укажите пароль или ключ)")
	}
	if lastErr != nil && len(auths) == 0 {
		return nil, lastErr
	}

	cfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            auths,
		HostKeyCallback: HostKeyVerifier(c.StrictKey, confirmHostKeyChanged),
		Timeout:         timeout,
	}

	for attempt := 0; attempt <= retries; attempt++ {
		cl, err := ssh.Dial("tcp", addr, cfg)
		if err == nil {
			return cl, nil
		}
		lastErr = err
		if strings.Contains(err.Error(), "unable to authenticate") ||
			strings.Contains(err.Error(), "host key") ||
			strings.Contains(err.Error(), "knownhosts") {
			return nil, err // non-retryable
		}
		if attempt < retries {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return nil, lastErr
}

func loadSigner(path, passphrase string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if passphrase == "" {
		return ssh.ParsePrivateKey(data)
	}
	return ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
}

// Exec runs a command and returns combined output. Subject to exec timeout.
func Exec(cl *ssh.Client, cmd string, timeoutSec int) (string, error) {
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		sess, err := cl.NewSession()
		if err != nil {
			ch <- res{"", err}
			return
		}
		defer sess.Close()
		out, err := sess.CombinedOutput(cmd)
		ch <- res{string(out), err}
	}()
	timeout := time.Duration(timeoutSec) * time.Second
	if timeoutSec <= 0 {
		timeout = 120 * time.Second
	}
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(timeout):
		return "", fmt.Errorf("таймаут команды (%d с)", timeoutSec)
	}
}

// syncBuffer is a mutex-protected bytes.Buffer. SSH session copies stdout
// and stderr from TWO goroutines; sharing a plain bytes.Buffer loses output
// (bytes.Buffer.ReadFrom mutates its state even on empty reads — confirmed
// with the race detector).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ExecRoot runs a command with root privileges via `su - root -c`.
func ExecRoot(cl *ssh.Client, cmd string, rootPass string, timeoutSec int) (string, error) {
	sess, err := cl.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	quoted := shellQuote(cmd)
	sess.Stdin = strings.NewReader(rootPass + "\n")
	done := make(chan struct{})
	var buf syncBuffer
	sess.Stdout = &buf
	sess.Stderr = &buf
	go func() {
		_ = sess.Run("su - root -c " + quoted)
		close(done)
	}()
	timeout := time.Duration(timeoutSec) * time.Second
	if timeoutSec <= 0 {
		timeout = 120 * time.Second
	}
	select {
	case <-done:
		return buf.String(), nil
	case <-time.After(timeout):
		return buf.String(), fmt.Errorf("таймаут команды (root) (%d с)", timeoutSec)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
