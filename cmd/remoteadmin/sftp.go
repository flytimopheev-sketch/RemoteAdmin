package main

import (
        "encoding/base64"
        "fmt"
        "io"
        "os"
        "path/filepath"
        "strconv"
        "strings"
        "time"

        "github.com/pkg/sftp"
        "golang.org/x/crypto/ssh"
)

// SFTPClient wraps an sftp client bound to a host IP with its SSH client
// so both can be closed together.
type SFTPClient struct {
        SSH    *ssh.Client
        SFTP   *sftp.Client
        IP     string
        AsRoot bool // root-режим включён: недоступное пользователю выполняется через su
}

// OpenSFTP открывает SFTP-сессию. Схема входа (v1.11.0) — как у терминалов:
// СНАЧАЛА логин+пароль учётной записи, затем root-операции выполняются
// через `su` (пароль root подставляется автоматически). Прямой вход под
// root по SSH больше не используется — на РЕД ОС он обычно запрещён
// (PermitRootLogin no), а последовательность «пользователь → su root»
// работает всегда.
func OpenSFTP(creds SSHCreds) (*SFTPClient, error) {
        cl, err := Connect(creds)
        if err != nil {
                return nil, err
        }
        sf, err := sftp.NewClient(cl)
        if err != nil {
                cl.Close()
                return nil, err
        }
        asRoot := store != nil && store.State.UseRoot && creds.RootPass != "" && creds.User != "root"
        if asRoot {
                logf("ok", "🔌 SFTP %s: вход как %s, операции с правами root — через su", creds.Host, creds.User)
        } else {
                logf("ok", "🔌 SFTP %s: вход как %s", creds.Host, creds.User)
        }
        return &SFTPClient{SSH: cl, SFTP: sf, IP: creds.Host, AsRoot: asRoot}, nil
}

func (s *SFTPClient) Close() {
        if s.SFTP != nil {
                s.SFTP.Close()
        }
        if s.SSH != nil {
                s.SSH.Close()
        }
}

// suEnabled — включён ли root-режим через su.
func (s *SFTPClient) suEnabled() bool {
        return s.AsRoot && store != nil && store.RootPassword() != ""
}

// suCapture выполняет команду через su и возвращает её вывод между
// маркерами (само приглашение «Пароль:» и служебный шум отбрасываются).
func suCapture(cl *ssh.Client, cmd, rootPass string, timeout int) (string, error) {
        wrapped := fmt.Sprintf("echo RA_SU_B; ( %s ); echo RA_SU_E=$?", cmd)
        out, err := ExecRoot(cl, wrapped, rootPass, timeout)
        if err != nil {
                return out, err
        }
        i := strings.Index(out, "RA_SU_B")
        if i < 0 {
                return out, fmt.Errorf("su: не найден маркер вывода")
        }
        body := out[i+len("RA_SU_B"):]
        body = strings.TrimPrefix(body, "\r\n")
        body = strings.TrimPrefix(body, "\n")
        j := strings.Index(body, "RA_SU_E=")
        if j < 0 {
                return body, fmt.Errorf("su: команда не завершилась маркером")
        }
        tail := body[j+len("RA_SU_E="):]
        rc := strings.TrimSpace(strings.SplitN(tail, "\n", 2)[0])
        body = body[:j]
        if rc != "0" {
                return body, fmt.Errorf("su: код возврата %s", rc)
        }
        return body, nil
}

// suListDir — список каталога через `su` + find -printf (machine-readable).
func (s *SFTPClient) suListDir(path string) ([]os.FileInfo, error) {
        cmd := fmt.Sprintf("find %s -maxdepth 1 -mindepth 1 -printf '%%y\\t%%s\\t%%T@\\t%%m\\t%%f\\n'", shellQuote(path))
        out, err := suCapture(s.SSH, cmd, store.RootPassword(), 30)
        if err != nil {
                return nil, err
        }
        var res []os.FileInfo
        for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
                line = strings.TrimRight(line, "\r")
                if strings.TrimSpace(line) == "" {
                        continue
                }
                parts := strings.SplitN(line, "\t", 5)
                if len(parts) < 5 {
                        continue
                }
                y, sz, mt, md, name := parts[0], parts[1], parts[2], parts[3], parts[4]
                if name == "" {
                        continue
                }
                fi := &suFileInfo{name: name}
                fi.size, _ = strconv.ParseInt(sz, 10, 64)
                if ts, e := strconv.ParseFloat(mt, 64); e == nil {
                        fi.mtime = time.Unix(int64(ts), 0)
                }
                mode64, _ := strconv.ParseUint(md, 8, 32)
                fi.mode = os.FileMode(mode64 & 0777)
                switch y {
                case "d":
                        fi.mode |= os.ModeDir | 0755
                case "l":
                        fi.mode |= os.ModeSymlink | 0777
                default:
                        fi.mode |= 0644
                }
                res = append(res, fi)
        }
        if res == nil {
                return nil, fmt.Errorf("su: каталог пуст или недоступен")
        }
        return res, nil
}

// suFileInfo — os.FileInfo поверх вывода find.
type suFileInfo struct {
        name  string
        size  int64
        mtime time.Time
        mode  os.FileMode
}

func (f *suFileInfo) Name() string       { return f.name }
func (f *suFileInfo) Size() int64        { return f.size }
func (f *suFileInfo) Mode() os.FileMode  { return f.mode }
func (f *suFileInfo) ModTime() time.Time { return f.mtime }
func (f *suFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f *suFileInfo) Sys() interface{}   { return nil }

func (s *SFTPClient) ListDir(path string) ([]os.FileInfo, error) {
        res, err := s.SFTP.ReadDir(path)
        if err == nil {
                return res, nil
        }
        if s.suEnabled() {
                if res2, err2 := s.suListDir(path); err2 == nil {
                        logf("info", "📂 %s: каталог прочитан через su (доступ пользователю запрещён)", path)
                        return res2, nil
                }
        }
        return res, err
}

// ReadFile читает удалённый файл целиком (для просмотра/редактирования).
// Файлы, недоступные пользователю, читаются через su (base64 — безопасно
// для бинарных данных).
func (s *SFTPClient) ReadFile(path string) ([]byte, error) {
        f, err := s.SFTP.Open(path)
        if err == nil {
                defer f.Close()
                return io.ReadAll(f)
        }
        if s.suEnabled() {
                out, err2 := suCapture(s.SSH, fmt.Sprintf("base64 -w0 %s", shellQuote(path)), store.RootPassword(), 120)
                if err2 == nil {
                        if data, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(out)); derr == nil {
                                logf("info", "📂 %s: файл прочитан через su", path)
                                return data, nil
                        }
                }
        }
        return nil, err
}

// WriteFileWithBackup записывает удалённый файл, предварительно сохранив
// копию «имя.ra_bak_ГГГГММДД_ЧЧММСС» рядом (бэкап при неудаче не фатален).
// Файлы, недоступные пользователю, пишутся через su: upload в /tmp, затем
// `cp` от root.
func (s *SFTPClient) WriteFileWithBackup(path string, data []byte, mode os.FileMode) (string, error) {
        bak := ""
        _, statErr := s.SFTP.Stat(path)
        if statErr == nil {
                bak = fmt.Sprintf("%s.ra_bak_%s", path, time.Now().Format("20060102_150405"))
                if err := s.SFTP.Rename(path, bak); err != nil {
                        // не смогли переименовать — попробуем через su
                        if s.suEnabled() {
                                if _, e2 := suCapture(s.SSH, fmt.Sprintf("cp -f %s %s", shellQuote(path), shellQuote(bak)), store.RootPassword(), 20); e2 != nil {
                                        bak = ""
                                }
                        } else {
                                bak = ""
                        }
                }
        }
        f, err := s.SFTP.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
        if err != nil {
                if !s.suEnabled() {
                        return bak, err
                }
                // root-путь: пишем во временный файл в /tmp и переносим через su
                tmp := fmt.Sprintf("/tmp/.ra_write_%d", time.Now().UnixNano())
                tf, terr := s.SFTP.Create(tmp)
                if terr != nil {
                        return bak, err
                }
                if _, werr := tf.Write(data); werr != nil {
                        tf.Close()
                        _ = s.SFTP.Remove(tmp)
                        return bak, werr
                }
                tf.Close()
                cmd := fmt.Sprintf("cp -f %s %s && rm -f %s", shellQuote(tmp), shellQuote(path), shellQuote(tmp))
                if mode != 0 {
                        cmd += fmt.Sprintf(" && chmod %o %s", mode.Perm(), shellQuote(path))
                }
                if _, serr := suCapture(s.SSH, cmd, store.RootPassword(), 60); serr != nil {
                        return bak, serr
                }
                logf("info", "💾 %s: записано через su", path)
                return bak, nil
        }
        defer f.Close()
        if _, err := f.Write(data); err != nil {
                return bak, err
        }
        if mode != 0 {
                _ = s.SFTP.Chmod(path, mode.Perm())
        }
        return bak, nil
}

func (s *SFTPClient) Upload(localPath, remotePath string) (int64, error) {
        src, err := os.Open(localPath)
        if err != nil {
                return 0, err
        }
        defer src.Close()
        fi, _ := src.Stat()
        dst, err := s.SFTP.Create(remotePath)
        if err != nil {
                if s.suEnabled() {
                        // путь недоступен пользователю — через /tmp + su
                        tmp := fmt.Sprintf("/tmp/.ra_upload_%d", time.Now().UnixNano())
                        tf, terr := s.SFTP.Create(tmp)
                        if terr != nil {
                                return 0, err
                        }
                        src.Seek(0, 0)
                        n, werr := io.Copy(tf, src)
                        tf.Close()
                        if werr != nil {
                                _ = s.SFTP.Remove(tmp)
                                return n, werr
                        }
                        cmd := fmt.Sprintf("cp -f %s %s && rm -f %s", shellQuote(tmp), shellQuote(remotePath), shellQuote(tmp))
                        if fi != nil {
                                cmd += fmt.Sprintf(" && chmod %o %s", fi.Mode().Perm(), shellQuote(remotePath))
                        }
                        if _, serr := suCapture(s.SSH, cmd, store.RootPassword(), 120); serr != nil {
                                return n, serr
                        }
                        logf("info", "📤 %s → %s: через su", filepath.Base(localPath), remotePath)
                        return n, nil
                }
                return 0, err
        }
        defer dst.Close()
        n, err := io.Copy(dst, src)
        if err != nil {
                return n, err
        }
        if fi != nil {
                _ = s.SFTP.Chmod(remotePath, fi.Mode().Perm())
        }
        return n, nil
}

func (s *SFTPClient) Download(remotePath, localPath string) (int64, error) {
        src, err := s.SFTP.Open(remotePath)
        if err == nil {
                defer src.Close()
                dst, derr := os.Create(localPath)
                if derr != nil {
                        return 0, derr
                }
                defer dst.Close()
                return io.Copy(dst, src)
        }
        // su-фолбэк: base64 → декодирование (бинарные файлы тоже ок)
        if s.suEnabled() {
                out, err2 := suCapture(s.SSH, fmt.Sprintf("base64 -w0 %s", shellQuote(remotePath)), store.RootPassword(), 120)
                if err2 == nil {
                        if data, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(out)); derr == nil {
                                werr := os.WriteFile(localPath, data, 0644)
                                if werr == nil {
                                        logf("info", "📥 %s: скачано через su", remotePath)
                                        return int64(len(data)), nil
                                }
                                return 0, werr
                        }
                }
        }
        return 0, err
}

func (s *SFTPClient) MkdirAllP(path string) error {
        if path == "/" || path == "" {
                return nil
        }
        if _, err := s.SFTP.Stat(path); err == nil {
                return nil
        }
        parent := filepath.Dir(path)
        // filepath.Dir gives OS paths; normalise for remote (always "/")
        if parent != path && parent != "." && parent != "/" {
                _ = s.MkdirAllP(parent)
        }
        err := s.SFTP.Mkdir(path)
        if err != nil && s.suEnabled() {
                _, err2 := suCapture(s.SSH, fmt.Sprintf("mkdir -p -- %s", shellQuote(path)), store.RootPassword(), 30)
                if err2 == nil {
                        return nil
                }
        }
        return err
}

// remoteJoin joins remote path elements with "/" (never filepath.Separator
// dependent) and keeps absolute paths absolute.
func remoteJoin(base, name string) string {
        if base == "" {
                base = "."
        }
        if strings.HasSuffix(base, "/") {
                return base + name
        }
        return base + "/" + name
}

// UploadPath copies a local file or a whole directory tree to the remote
// side, creating directories as needed. Returns the number of bytes sent.
func (s *SFTPClient) UploadPath(localPath, remotePath string) (int64, error) {
        fi, err := os.Stat(localPath)
        if err != nil {
                return 0, err
        }
        if !fi.IsDir() {
                return s.Upload(localPath, remotePath)
        }
        if err := s.MkdirAllP(remotePath); err != nil {
                return 0, err
        }
        entries, err := os.ReadDir(localPath)
        if err != nil {
                return 0, err
        }
        var total int64
        for _, e := range entries {
                n, err := s.UploadPath(filepath.Join(localPath, e.Name()), remoteJoin(remotePath, e.Name()))
                total += n
                if err != nil {
                        return total, err
                }
        }
        // preserve directory mode
        _ = s.SFTP.Chmod(remotePath, fi.Mode().Perm())
        return total, nil
}

// DownloadPath copies a remote file or a whole directory tree to the local
// side. Returns the number of bytes received. Каталоги, невидимые
// пользователю, обходятся через su (v1.11.0).
func (s *SFTPClient) DownloadPath(remotePath, localPath string) (int64, error) {
        fi, err := s.SFTP.Stat(remotePath)
        if err != nil {
                // su-фолбэк: сначала пробуем как файл, потом как каталог
                if s.suEnabled() {
                        if n, ferr := s.Download(remotePath, localPath); ferr == nil {
                                return n, nil
                        }
                        infos, lerr := s.suListDir(remotePath)
                        if lerr == nil {
                                if mkerr := os.MkdirAll(localPath, 0755); mkerr != nil {
                                        return 0, mkerr
                                }
                                var total int64
                                for _, e := range infos {
                                        n, derr := s.DownloadPath(remoteJoin(remotePath, e.Name()), filepath.Join(localPath, e.Name()))
                                        total += n
                                        if derr != nil {
                                                return total, derr
                                        }
                                }
                                logf("info", "📥 %s: каталог скачан через su", remotePath)
                                return total, nil
                        }
                }
                return 0, err
        }
        if !fi.IsDir() {
                return s.Download(remotePath, localPath)
        }
        if err := os.MkdirAll(localPath, 0755); err != nil {
                return 0, err
        }
        infos, err := s.SFTP.ReadDir(remotePath)
        if err != nil && s.suEnabled() {
                if infos2, lerr := s.suListDir(remotePath); lerr == nil {
                        infos = infos2
                        err = nil
                }
        }
        if err != nil {
                return 0, err
        }
        var total int64
        for _, e := range infos {
                n, err := s.DownloadPath(remoteJoin(remotePath, e.Name()), filepath.Join(localPath, e.Name()))
                total += n
                if err != nil {
                        return total, err
                }
        }
        return total, nil
}

// RemoveAllP deletes a remote file or directory tree using SFTP;
// при недоступности пользователю — через su (root-режим).
func (s *SFTPClient) RemoveAllP(path string) error {
        fi, err := s.SFTP.Stat(path)
        if err != nil {
                if s.suEnabled() {
                        // возможно, путь вообще не виден пользователю — удаляем через su
                        _, err2 := suCapture(s.SSH, fmt.Sprintf("rm -rf -- %s", shellQuote(path)), store.RootPassword(), 120)
                        if err2 == nil {
                                logf("info", "🗑 %s: удалено через su", path)
                                return nil
                        }
                }
                return err
        }
        if !fi.IsDir() {
                if err := s.SFTP.Remove(path); err == nil {
                        return nil
                }
                if s.suEnabled() {
                        _, err2 := suCapture(s.SSH, fmt.Sprintf("rm -f -- %s", shellQuote(path)), store.RootPassword(), 60)
                        if err2 == nil {
                                return nil
                        }
                }
                return err
        }
        infos, err := s.SFTP.ReadDir(path)
        if err != nil {
                if s.suEnabled() {
                        _, err2 := suCapture(s.SSH, fmt.Sprintf("rm -rf -- %s", shellQuote(path)), store.RootPassword(), 120)
                        if err2 == nil {
                                return nil
                        }
                }
                return err
        }
        for _, e := range infos {
                if err := s.RemoveAllP(remoteJoin(path, e.Name())); err != nil {
                        return err
                }
        }
        err = s.SFTP.RemoveDirectory(path)
        if err != nil && s.suEnabled() {
                _, err2 := suCapture(s.SSH, fmt.Sprintf("rmdir -- %s", shellQuote(path)), store.RootPassword(), 60)
                if err2 == nil {
                        return nil
                }
        }
        return err
}

// UploadToLocalPath-agnostic helper for root destinations:
// upload to /tmp then `su - root -c` move.
func uploadThenSuRoot(cl *ssh.Client, localPath, remoteDest, rootPass string, timeout int) error {
        tmp := fmt.Sprintf("/tmp/.ra_upload_%d", time.Now().UnixNano())
        sf, err := sftp.NewClient(cl)
        if err != nil {
                return err
        }
        src, err := os.Open(localPath)
        if err != nil {
                sf.Close()
                return err
        }
        dst, err := sf.Create(tmp)
        if err != nil {
                src.Close()
                sf.Close()
                return err
        }
        if _, err := io.Copy(dst, src); err != nil {
                src.Close()
                dst.Close()
                sf.Close()
                return err
        }
        src.Close()
        dst.Close()
        sf.Close()
        cmd := fmt.Sprintf("cp -f %s %s && rm -f %s", shellQuote(tmp), shellQuote(remoteDest), shellQuote(tmp))
        _, err = ExecRoot(cl, cmd, rootPass, timeout)
        return err
}
