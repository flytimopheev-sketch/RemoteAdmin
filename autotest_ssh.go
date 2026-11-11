package main

// autotest_ssh.go — minimal in-process SSH server for --autotest.
// Supports: password & keyboard-interactive auth, "exec" requests (including
// the app's `su - root -c '…'` escalation form) and the "sftp" subsystem
// (via github.com/pkg/sftp request server). This lets the whole app stack be
// tested end-to-end without an external SSH host.

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type fakeSSHServer struct {
	ln     net.Listener
	port   int
	config *ssh.ServerConfig
	closed chan struct{}
}

// startFakeSSHServer listens on a random port.
func startFakeSSHServer(user, pass string) (*fakeSSHServer, error) {
	return startFakeSSHServerOn("127.0.0.1:0", user, pass)
}

// startFakeSSHServerOn listens on the given address.
func startFakeSSHServerOn(addr, user, pass string) (*fakeSSHServer, error) {
	// host key
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}

	accept := func(conn ssh.ConnMetadata, candidate string) bool {
		if user == "*" { // demo mode: accept anything
			return true
		}
		return conn.User() == user && candidate == pass
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if accept(conn, string(password)) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("отказ в доступе для %q", conn.User())
		},
		KeyboardInteractiveCallback: func(conn ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := client(conn.User(), "autotest", []string{"Пароль:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(ans) > 0 && accept(conn, ans[0]) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("отказ в доступе для %q", conn.User())
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &fakeSSHServer{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, config: cfg, closed: make(chan struct{})}
	go s.serve()
	return s, nil
}

func (s *fakeSSHServer) Close() {
	close(s.closed)
	_ = s.ln.Close()
}

func (s *fakeSSHServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *fakeSSHServer) handleConn(c net.Conn) {
	defer c.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.config)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, _ := newCh.Accept()
		go s.handleSession(ch, chReqs)
	}
}

func (s *fakeSSHServer) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &p)
			_ = req.Reply(true, nil)
			s.runCommand(ch, p.Command)
			// real sshd keeps the session channel open until the client closes
			// its write side (stdin EOF); closing early races with the client's
			// stdin copy and loses output.
			_, _ = io.Copy(io.Discard, ch)
			time.Sleep(40 * time.Millisecond)
			return
		case "subsystem":
			var p struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &p)
			if p.Name == "sftp" {
				_ = req.Reply(true, nil)
				srv, err := sftp.NewServer(ch)
				if err == nil {
					_ = srv.Serve() // serves the real FS; tests use /tmp only
				}
				return
			}
			_ = req.Reply(false, nil)
		case "shell":
			// interactive shell (демо-режим и локальные тесты):
			// /bin/sh -i c stdin/stdout поверх канала.
			_ = req.Reply(true, nil)
			sc := exec.Command("/bin/sh", "-i")
			sin, _ := sc.StdinPipe()
			sc.Stdout = ch
			sc.Stderr = ch.Stderr()
			if sc.Start() == nil {
				go func() {
					_, _ = io.Copy(sin, ch)
					_ = sin.Close()
				}()
				go func() {
					_ = sc.Wait()
					_ = ch.CloseWrite()
				}()
			}
		case "pty-req", "env":
			_ = req.Reply(true, nil)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// runCommand executes a command like a real shell would. The app's root
// escalation form `su - root -c 'CMD'` is unwrapped and run directly (the
// fake server has no real root; escalation logic itself is what we verify).
func (s *fakeSSHServer) runCommand(ch ssh.Channel, cmd string) {
	dbg := os.Getenv("RA_DEBUG_SSH") != ""
	if dbg {
		fmt.Printf("[fake-ssh] exec %q\n", cmd)
	}
	code := 0
	if inner, ok := unwrapSuRoot(cmd); ok {
		cmd = inner
		if dbg {
			fmt.Printf("[fake-ssh] su unwrapped → %q\n", cmd)
		}
	}
	if strings.HasPrefix(cmd, "exit ") {
		fmt.Sscanf(strings.TrimSpace(cmd[5:]), "%d", &code)
	} else {
		c := exec.Command("/bin/sh", "-c", cmd)
		c.Stdout = ch
		c.Stderr = ch.Stderr()
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				fmt.Fprintf(ch.Stderr(), "exec: %v\n", err)
				code = 127
			}
		}
	}
	if dbg {
		fmt.Printf("[fake-ssh] exit=%d\n", code)
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
	_ = ch.CloseWrite()
}

// unwrapSuRoot recognises `su - root -c 'CMD'` and returns CMD.
func unwrapSuRoot(cmd string) (string, bool) {
	const prefix = "su - root -c "
	if !strings.HasPrefix(cmd, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(cmd[len(prefix):])
	if len(rest) >= 2 && strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'") {
		rest = rest[1 : len(rest)-1]
		rest = strings.ReplaceAll(rest, `'\''`, `'`)
	}
	return rest, true
}

// waitPortReadable waits until TCP connect to addr succeeds (test helper).
func waitPortReadable(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

var _ = os.Getenv
