package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ---------- desktop notifications ----------
//
// Strategy: try the native D-Bus org.freedesktop.Notifications protocol
// (no external deps); fall back to notify-send(1); otherwise log only.

var dbusConn net.Conn
var dbusSerial uint32
var dbusMu = false

// Notify sends a desktop notification (best effort).
func Notify(summary, body string) {
	if app != nil && app.window != nil && !store.State.Settings.Notifications {
		return
	}
	go func() {
		if err := dbusNotify(AppName, summary, body); err != nil {
			if err := notifySend(summary, body); err != nil {
				logf("info", "🔔 %s — %s (без уведомления: %v)", summary, body, err)
			} else {
				logf("info", "🔔 %s — %s", summary, body)
			}
		} else {
			logf("info", "🔔 %s — %s", summary, body)
		}
	}()
}

func notifySend(summary, body string) error {
	p, err := exec.LookPath("notify-send")
	if err != nil {
		return err
	}
	cmd := exec.Command(p, "-a", AppName, summary, body)
	cmd.Env = os.Environ()
	return cmd.Run()
}

// ---------- minimal D-Bus client (EXTERNAL auth, method calls only) ----------

func dbusAddress() (string, bool) {
	if a := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		for _, part := range strings.Split(a, ";") {
			if strings.HasPrefix(part, "unix:") {
				for _, kv := range strings.Split(strings.TrimPrefix(part, "unix:"), ",") {
					if strings.HasPrefix(kv, "path=") {
						return strings.TrimPrefix(kv, "path="), true
					}
					if strings.HasPrefix(kv, "abstract=") {
						return "@" + strings.TrimPrefix(kv, "abstract="), true
					}
				}
			}
		}
	}
	if rd := os.Getenv("XDG_RUNTIME_DIR"); rd != "" {
		p := rd + "/bus"
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

func dbusDial() (net.Conn, error) {
	addr, ok := dbusAddress()
	if !ok {
		return nil, errors.New("нет D-Bus адреса")
	}
	var c net.Conn
	var err error
	if strings.HasPrefix(addr, "@") {
		c, err = net.Dial("unix", "\x00"+addr[1:])
	} else {
		c, err = net.Dial("unix", addr)
	}
	if err != nil {
		return nil, err
	}
	// EXTERNAL auth: send hex-encoded uid
	uid := strconv.Itoa(os.Getuid())
	hex := fmt.Sprintf("%x", uid)
	auth := "\x00AUTH EXTERNAL " + hex + "\r\n"
	if _, err := c.Write([]byte(auth)); err != nil {
		c.Close()
		return nil, err
	}
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	if err != nil {
		c.Close()
		return nil, err
	}
	resp := string(buf[:n])
	if !strings.HasPrefix(resp, "OK") {
		c.Close()
		return nil, fmt.Errorf("dbus auth failed: %s", strings.TrimSpace(resp))
	}
	c.Write([]byte("BEGIN\r\n"))
	return c, nil
}

// dbus message constants
const (
	dbusLittleEndian  byte = 'l'
	dbusMsgMethodCall byte = 1
	dbusProtoVersion  byte = 1
)

type dbusString string

func dbusAppendString(buf *bytes.Buffer, s string) {
	var l uint32
	l = uint32(len(s))
	_ = binary.Write(buf, binary.LittleEndian, l)
	buf.WriteString(s)
	buf.WriteByte(0)
}

// dbusNotify calls org.freedesktop.Notifications.Notify
func dbusNotify(appName, summary, body string) error {
	if dbusMu {
		return errors.New("dbus busy")
	}
	dbusMu = true
	defer func() { dbusMu = false }()

	if dbusConn == nil {
		c, err := dbusDial()
		if err != nil {
			return err
		}
		dbusConn = c
	}
	c := dbusConn
	c.SetDeadline(time.Now().Add(3 * time.Second))

	dbusSerial++
	serial := dbusSerial

	// body: (s app_name, u replaces_id, s app_icon, s summary, s body, as actions, a{sv} hints, i timeout)
	var body_b bytes.Buffer
	dbusAppendString(&body_b, appName)
	_ = binary.Write(&body_b, binary.LittleEndian, uint32(0)) // replaces_id
	dbusAppendString(&body_b, "")                             // app_icon
	dbusAppendString(&body_b, summary)
	dbusAppendString(&body_b, body)
	_ = binary.Write(&body_b, binary.LittleEndian, uint32(0)) // actions: empty array
	_ = binary.Write(&body_b, binary.LittleEndian, byte(0))   // a{sv}: empty dict, no padding needed at offset? (align 4 — handled below)

	// NOTE: proper alignment for a{sv} would be 4; body length so far:
	// s(app) + u + s + s + s = sum(4+len+1)*... compute real alignment:
	// We rebuild body with correct alignment for the dict:
	body_b.Reset()
	align := func(buf *bytes.Buffer, a int) {
		for buf.Len()%a != 0 {
			buf.WriteByte(0)
		}
	}
	dbusAppendString(&body_b, appName)
	align(&body_b, 4)
	_ = binary.Write(&body_b, binary.LittleEndian, uint32(0))
	dbusAppendString(&body_b, "")
	dbusAppendString(&body_b, summary)
	dbusAppendString(&body_b, body)
	align(&body_b, 4)
	_ = binary.Write(&body_b, binary.LittleEndian, uint32(0)) // empty "as"
	align(&body_b, 4)
	_ = binary.Write(&body_b, binary.LittleEndian, uint32(0)) // empty "a{sv}"
	align(&body_b, 4)
	_ = binary.Write(&body_b, binary.LittleEndian, int32(-1)) // expire_timeout

	// header strings
	var sig = "susssasiai" // s u s s s as a{sv} i  → DBus sig: "susssasiai"
	hdr := func() []byte {
		var h bytes.Buffer
		// fixed header part:
		h.WriteByte(dbusLittleEndian)
		h.WriteByte(dbusMsgMethodCall)
		h.WriteByte(dbusProtoVersion)
		h.WriteByte(1) // body serial? actually BYTE 3 = bit l, message type, flags, version → next: UINT32 body length
		_ = binary.Write(&h, binary.LittleEndian, uint32(body_b.Len()))
		_ = binary.Write(&h, binary.LittleEndian, serial)
		// header fields array (a(yv)):
		var fields bytes.Buffer
		writeField := func(code byte, vtype byte, val []byte) {
			fields.WriteByte(code)
			// variant: signature string
			dbusAppendString(&fields, string(vtype))
			fields.Write(val)
		}
		// field 1 PATH = /org/freedesktop/Notifications (o)
		var tmp bytes.Buffer
		dbusAppendString(&tmp, "/org/freedesktop/Notifications")
		writeField(1, 'o', tmp.Bytes())
		// field 2 INTERFACE
		tmp.Reset()
		dbusAppendString(&tmp, "org.freedesktop.Notifications")
		writeField(2, 's', tmp.Bytes())
		// field 3 MEMBER Notify
		tmp.Reset()
		dbusAppendString(&tmp, "Notify")
		writeField(3, 's', tmp.Bytes())
		// field 6 DESTINATION
		tmp.Reset()
		dbusAppendString(&tmp, "org.freedesktop.Notifications")
		writeField(6, 's', tmp.Bytes())
		// field 8 SIGNATURE
		tmp.Reset()
		dbusAppendString(&tmp, sig)
		writeField(8, 'g', tmp.Bytes())

		// align fields array to 8 relative to message start (offset after serial is 16)
		for (16+fields.Len())%8 != 0 {
			fields.WriteByte(0)
		}
		_ = binary.Write(&h, binary.LittleEndian, uint32(fields.Len()))
		h.Write(fields.Bytes())
		return h.Bytes()
	}()
	out := append(hdr, body_b.Bytes()...)
	if _, err := c.Write(out); err != nil {
		dbusConn = nil
		return err
	}
	// read reply header minimally (reply type byte 1 of header)
	buf := make([]byte, 16)
	if _, err := c.Read(buf); err != nil {
		dbusConn = nil
		return err
	}
	msgType := buf[1]
	if msgType == 4 { // ERROR
		dbusConn = nil
		return errors.New("dbus error reply")
	}
	return nil
}
