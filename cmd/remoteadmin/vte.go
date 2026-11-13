package main

// vte.go — встраивание настоящего терминального эмулятора (libvte-2.91,
// тот же движок, что в GNOME Terminal) во вкладку «Терминалы».
//
// Зачем: TextView-рендер не умеет курсорную адресацию, поэтому полно-
// экранные программы (mc, top, vim) в нём разваливаются. VTE — полный
// терминал: mc/top/htop работают «как в настоящем терминале», клавиатура
// идёт прямо в нужную сессию, есть прокрутка и скроллбэк.
//
// Схема данных: VTE управляет своим PTY (vte_pty_new_sync). Мы берём
// master-fd этого PTY, открываем slave (ptsname) в режиме RAW и делаем
// мост с сессией x/crypto/ssh:
//
//      ssh stdout ──► запись в slave ──► VTE (рендер)
//      клавиатура VTE ──► master ──► чтение slave ──► ssh stdin
//
// При изменении размера сетки терминала (char-size-changed) отправляем
// WindowChange на ssh-сессию — программы внутри узнают о новом размере.

/*
#cgo pkg-config: vte-2.91
#define _GNU_SOURCE
#include <vte/vte.h>
#include <gtk/gtk.h>
#include <stdlib.h>
#include <string.h>
#include <fcntl.h>
#include <unistd.h>
#include <termios.h>

// опрос размера сетки из Go (сигналы не используем — без //export проще)
static void ra_vte_size(VteTerminal *t, int *rows, int *cols) {
        *rows = (int)vte_terminal_get_row_count(t);
        *cols = (int)vte_terminal_get_column_count(t);
}

// создаёт VteTerminal с настройками и упаковывает его вместе со
// скроллбаром в GtkBox (boxPtr — GtkWidget* из gotk3 как unsafe.Pointer).
// Возвращает GtkWidget* терминала.
static GtkWidget *ra_vte_pack(GtkWidget *boxPtr, double fr, double fg, double fb,
        double br, double bg, double bb) {
        GtkWidget *term = vte_terminal_new();
        VteTerminal *vt = VTE_TERMINAL(term);

        // цвет по теме приложения: тёмный фон в тёмной, белый — в светлой
        GdkRGBA fore = {fr, fg, fb, 1.0};
        GdkRGBA back = {br, bg, bb, 1.0};
        vte_terminal_set_colors(vt, &fore, &back, NULL, 0);

        PangoFontDescription *fd = pango_font_description_from_string("Monospace 11");
        vte_terminal_set_font(vt, fd);
        pango_font_description_free(fd);

        vte_terminal_set_scrollback_lines(vt, 5000);
        vte_terminal_set_audible_bell(vt, FALSE);
        vte_terminal_set_input_enabled(vt, TRUE);
        vte_terminal_set_mouse_autohide(vt, TRUE);

        GtkWidget *bar = gtk_scrollbar_new(GTK_ORIENTATION_VERTICAL,
                gtk_scrollable_get_vadjustment(GTK_SCROLLABLE(vt)));
        gtk_box_pack_start(GTK_BOX(boxPtr), term, TRUE, TRUE, 0);
        gtk_box_pack_start(GTK_BOX(boxPtr), bar, FALSE, FALSE, 0);
        gtk_widget_show_all(term);
        gtk_widget_show_all(bar);
        return term;
}

// создаёт PTY, открывает slave и переводит его в RAW (мост не должен
// вмешиваться в поток: без echo, без канонического режима — иначе
// задвоение ввода и проблемы с полноэкранными программами).
// Возвращает slave fd или -1.
static int ra_vte_attach_pty(VteTerminal *t) {
        GError *err = NULL;
        VtePty *pty = vte_pty_new_sync(VTE_PTY_DEFAULT, NULL, &err);
        if (pty == NULL) {
                if (err != NULL) {
                        g_error_free(err);
                }
                return -1;
        }
        vte_terminal_set_pty(t, pty);
        int mfd = vte_pty_get_fd(pty);
        if (mfd < 0) {
                return -1;
        }
        char path[256];
        if (ptsname_r(mfd, path, sizeof(path)) != 0) {
                return -1;
        }
        int sfd = open(path, O_RDWR | O_NOCTTY);
        if (sfd < 0) {
                return -1;
        }
        struct termios ios;
        if (tcgetattr(sfd, &ios) == 0) {
                cfmakeraw(&ios);
                tcsetattr(sfd, TCSANOW, &ios);
        }
        return sfd;
}

// разрешить/запретить ввод (при мёртвой сессии клавиатура не нужна)
static void ra_vte_input_enabled(VteTerminal *t, gboolean on) {
        vte_terminal_set_input_enabled(t, on);
}
*/
import "C"
import (
        "fmt"
        "io"
        "sync"
        "syscall"
        "time"
        "unsafe"

        "github.com/gotk3/gotk3/gtk"
)

// vteTerm — обёртка над одним VteTerminal.
type vteTerm struct {
        ptr      *C.VteTerminal
        onResize func(rows, cols int)
        lastRows int
        lastCols int
        mu       sync.Mutex
        dead     bool
}

// реестр активных VTE — общий опрос размеров (без сигналов)
var (
        vteRegMu  sync.Mutex
        vteReg    = map[*vteTerm]struct{}{}
        vtePollOn bool
)

func vteRegAdd(v *vteTerm) {
        vteRegMu.Lock()
        vteReg[v] = struct{}{}
        need := !vtePollOn
        vtePollOn = true
        vteRegMu.Unlock()
        if need {
                go vtePollLoop()
        }
}

func vteRegDel(v *vteTerm) {
        vteRegMu.Lock()
        delete(vteReg, v)
        vteRegMu.Unlock()
}

// vtePollLoop раз в полсекунды сравнивает размер сетки каждого VTE
// с последним известным и вызывает onResize (WindowChange на ssh).
func vtePollLoop() {
        for {
                time.Sleep(500 * time.Millisecond)
                vteRegMu.Lock()
                items := make([]*vteTerm, 0, len(vteReg))
                for v := range vteReg {
                        items = append(items, v)
                }
                vteRegMu.Unlock()
                if len(items) == 0 {
                        vteRegMu.Lock()
                        vtePollOn = false
                        vteRegMu.Unlock()
                        return
                }
                for _, v := range items {
                        if v == nil || v.ptr == nil {
                                continue
                        }
                        v.mu.Lock()
                        cb := v.onResize
                        lastR, lastC := v.lastRows, v.lastCols
                        v.mu.Unlock()
                        if cb == nil {
                                continue
                        }
                        var rows, cols C.int
                        // вызов в главный поток GTK — VTE не потокобезопасен
                        done := make(chan struct{})
                        glibIdle(func() {
                                if v.ptr != nil {
                                        C.ra_vte_size(v.ptr, &rows, &cols)
                                }
                                close(done)
                        })
                        <-done
                        r, c := int(rows), int(cols)
                        if r > 0 && c > 0 && (r != lastR || c != lastC) {
                                v.mu.Lock()
                                v.lastRows, v.lastCols = r, c
                                v.mu.Unlock()
                                cb(r, c)
                        }
                }
        }
}

// newVTE создаёт терминал и упаковывает его в контейнер.
// isLight — светлая тема приложения (меняем фон под неё).
func newVTE(container *gtk.Box, isLight bool) *vteTerm {
        v := &vteTerm{}
        fr, fg, fb, _ := hexToRGBA("#d6dce8")
        br, bg, bb, _ := hexToRGBA("#10131a")
        if isLight {
                fr, fg, fb, _ = hexToRGBA("#20242e")
                br, bg, bb, _ = hexToRGBA("#ffffff")
        }
        boxObj := unsafe.Pointer(container.GObject)
        term := C.ra_vte_pack((*C.GtkWidget)(boxObj), C.double(fr), C.double(fg), C.double(fb), C.double(br), C.double(bg), C.double(bb))
        v.ptr = (*C.VteTerminal)(unsafe.Pointer(term))
        vteRegAdd(v)
        return v
}

// attachPTY подключает PTY к ssh-сессии. Возвращает slave fd для моста.
func (v *vteTerm) attachPTY() int {
        return int(C.ra_vte_attach_pty(v.ptr))
}

// feed печатает текст в терминал как будто его вывела удалённая сторона
// (служебные сообщения: «подключено», «сессия закрыта» и т.п.).
func (v *vteTerm) feed(text string) {
        if v == nil || v.ptr == nil {
                return
        }
        c := C.CString(text)
        defer C.free(unsafe.Pointer(c))
        C.vte_terminal_feed(v.ptr, c, C.glong(len(text)))
}

// feedChild отправляет данные «как с клавиатуры» (в сторону child).
func (v *vteTerm) feedChild(data []byte) {
        if v == nil || v.ptr == nil || len(data) == 0 {
                return
        }
        c := C.CBytes(data)
        defer C.free(c)
        C.vte_terminal_feed_child(v.ptr, (*C.char)(c), C.glong(len(data)))
}

func (v *vteTerm) setInputEnabled(on bool) {
        if v == nil || v.ptr == nil {
                return
        }
        C.ra_vte_input_enabled(v.ptr, gbool(on))
}

func gbool(b bool) C.gboolean {
        if b {
                return C.gboolean(1)
        }
        return C.gboolean(0)
}

// ptyBridge — мост «ssh-сессия ↔ VTE PTY».
type ptyBridge struct {
        slave int
        stop  chan struct{}
        once  sync.Once
}

func startPTYBridge(v *vteTerm, stdout io.Reader, stdin io.WriteCloser, onOut func([]byte)) *ptyBridge {
        sfd := v.attachPTY()
        if sfd < 0 {
                return nil
        }
        b := &ptyBridge{slave: sfd, stop: make(chan struct{})}

        // stdout: ssh → slave (VTE отрисует)
        go func() {
                buf := make([]byte, 8192)
                for {
                        select {
                        case <-b.stop:
                                return
                        default:
                        }
                        n, err := stdout.Read(buf)
                        if n > 0 {
                                chunk := make([]byte, n)
                                copy(chunk, buf[:n])
                                if onOut != nil {
                                        onOut(chunk)
                                }
                                if _, werr := writeAll(b.slave, chunk); werr != nil {
                                        return
                                }
                        }
                        if err != nil {
                                return
                        }
                }
        }()

        // stdin: VTE (master) → slave → ssh stdin
        go func() {
                buf := make([]byte, 4096)
                for {
                        select {
                        case <-b.stop:
                                return
                        default:
                        }
                        n, err := readFD(b.slave, buf)
                        if n > 0 {
                                if _, werr := stdin.Write(buf[:n]); werr != nil {
                                        return
                                }
                        }
                        if err != nil {
                                return
                        }
                }
        }()
        return b
}

func (b *ptyBridge) close() {
        if b == nil {
                return
        }
        b.once.Do(func() {
                close(b.stop)
                if b.slave >= 0 {
                        closeFD(b.slave)
                        b.slave = -1
                }
        })
}

// sshSessionLike — минимальный интерфейс ssh.Session, нужный мосту
// (настоящий *ssh.Session подходит напрямую).
type sshSessionLike interface {
        WindowChange(rows, cols int) error
}

// ---------- тонкие обёртки над POSIX-вызовами ----------

func writeAll(fd int, p []byte) (int, error) {
        total := 0
        for total < len(p) {
                n, err := writeFD(fd, p[total:])
                if n > 0 {
                        total += n
                }
                if err != nil {
                        return total, err
                }
                if n == 0 {
                        return total, fmt.Errorf("write: 0 bytes")
                }
        }
        return total, nil
}

func writeFD(fd int, p []byte) (int, error) {
        return syscall.Write(fd, p)
}

func readFD(fd int, p []byte) (int, error) {
        return syscall.Read(fd, p)
}

func closeFD(fd int) {
        _ = syscall.Close(fd)
}
