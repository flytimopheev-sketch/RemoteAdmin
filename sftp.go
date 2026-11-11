package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	AsRoot bool // сессия открыта от root — доступ ко всем каталогам
}

// OpenSFTP opens an SFTP session. Если включено повышение до root и задан
// пароль root — сначала пытается подключиться НАПРЯМУЮ как root@host:
// тогда доступны /root, /etc/shadow и все профили (частая жалоба — под
// обычным пользователем не зайти в чужие каталоги). Если прямой вход root
// запрещён сервером — возвращается сессия от имени обычного пользователя.
func OpenSFTP(creds SSHCreds) (*SFTPClient, error) {
	if store != nil && store.State.UseRoot && creds.RootPass != "" && creds.User != "root" {
		rc := creds
		rc.User = "root"
		rc.Password = creds.RootPass
		rc.KeyPath = ""
		rc.KeyPass = ""
		if cl, err := Connect(rc); err == nil {
			sf, err2 := sftp.NewClient(cl)
			if err2 == nil {
				logf("ok", "🔌 SFTP: вход как root@%s — доступны все каталоги", creds.Host)
				return &SFTPClient{SSH: cl, SFTP: sf, IP: creds.Host, AsRoot: true}, nil
			}
			cl.Close()
		} else {
			logf("warn", "🔒 Прямой вход root на %s не разрешён сервером — SFTP от пользователя %s", creds.Host, creds.User)
		}
	}
	cl, err := Connect(creds)
	if err != nil {
		return nil, err
	}
	sf, err := sftp.NewClient(cl)
	if err != nil {
		cl.Close()
		return nil, err
	}
	return &SFTPClient{SSH: cl, SFTP: sf, IP: creds.Host}, nil
}

func (s *SFTPClient) Close() {
	if s.SFTP != nil {
		s.SFTP.Close()
	}
	if s.SSH != nil {
		s.SSH.Close()
	}
}

func (s *SFTPClient) ListDir(path string) ([]os.FileInfo, error) {
	return s.SFTP.ReadDir(path)
}

// ReadFile читает удалённый файл целиком (для просмотра/редактирования).
func (s *SFTPClient) ReadFile(path string) ([]byte, error) {
	f, err := s.SFTP.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteFileWithBackup записывает удалённый файл, предварительно сохранив
// копию «имя.ra_bak_ГГГГММДД_ЧЧММСС» рядом (бэкап при неудаче не фатален).
func (s *SFTPClient) WriteFileWithBackup(path string, data []byte, mode os.FileMode) (string, error) {
	bak := ""
	if fi, err := s.SFTP.Stat(path); err == nil {
		bak = fmt.Sprintf("%s.ra_bak_%s", path, time.Now().Format("20060102_150405"))
		if err := s.SFTP.Rename(path, bak); err != nil {
			// не смогли переименовать — попробуем скопировать
			if _, e2 := ExecRoot(s.SSH, fmt.Sprintf("cp -f %s %s 2>/dev/null; true", shellQuote(path), shellQuote(bak)), store.RootPassword(), 20); e2 != nil {
				bak = ""
			}
		} else if fi != nil {
			mode = fi.Mode().Perm()
		}
	}
	f, err := s.SFTP.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return bak, err
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
	if err != nil {
		return 0, err
	}
	defer src.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	return io.Copy(dst, src)
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
	return s.SFTP.Mkdir(path)
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
// side. Returns the number of bytes received.
func (s *SFTPClient) DownloadPath(remotePath, localPath string) (int64, error) {
	fi, err := s.SFTP.Stat(remotePath)
	if err != nil {
		return 0, err
	}
	if !fi.IsDir() {
		return s.Download(remotePath, localPath)
	}
	if err := os.MkdirAll(localPath, 0755); err != nil {
		return 0, err
	}
	infos, err := s.SFTP.ReadDir(remotePath)
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

// RemoveAllP deletes a remote file or directory tree using SFTP only
// (no shell, no root password required).
func (s *SFTPClient) RemoveAllP(path string) error {
	fi, err := s.SFTP.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return s.SFTP.Remove(path)
	}
	infos, err := s.SFTP.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range infos {
		if err := s.RemoveAllP(remoteJoin(path, e.Name())); err != nil {
			return err
		}
	}
	return s.SFTP.RemoveDirectory(path)
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
