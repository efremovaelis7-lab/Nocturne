package core

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	errLocked          = errors.New("Другая копия NOCTURNE уже использует библиотеку")
	errChooseCancelled = errors.New("Выбор папки отменён")
)

// ErrLocked is returned by New when another NOCTURNE (old or new) runs.
var ErrLocked = errLocked

// atomicWrite writes data to a temp file in the same folder, syncs it and
// renames it over the target, so a crash never leaves a half-written file.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".nocturne-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(mode); err != nil {
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameRetry(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// atomicWriteFrom streams r into path the same way atomicWrite does.
func atomicWriteFrom(path string, r io.Reader, mode os.FileMode, limit int64) (int64, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".nocturne-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if runtime.GOOS != "windows" {
		_ = tmp.Chmod(mode)
	}
	n, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, errTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return n, err
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	if err := renameRetry(tmpName, path); err != nil {
		return n, err
	}
	ok = true
	return n, nil
}

var errTooLarge = errors.New("Файл слишком большой")

// renameRetry retries briefly: on Windows an antivirus or indexer can hold
// the target open for a moment.
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func fileSHA(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// copyFileVerified copies src to dst (which must not exist) and checks the
// SHA-256 of the copy against the original.
func copyFileVerified(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("В новой папке уже существует файл %s; выбери пустую папку", filepath.Base(dst))
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if _, err := atomicWriteFrom(dst, in, st.Mode().Perm()|0o600, 1<<40); err != nil {
		return err
	}
	_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	a, err := fileSHA(src)
	if err != nil {
		return err
	}
	b, err := fileSHA(dst)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return fmt.Errorf("Контрольная сумма копии не совпадает: %s", filepath.Base(dst))
	}
	return nil
}

// safeCopyDir copies every regular file of src into dst, skipping the
// folders listed in skip (absolute paths) and temp files.
func safeCopyDir(src, dst string, skip []string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, s := range skip {
			if s != "" && samePath(path, s) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Символические ссылки не копируются: %s", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if isTempName(d.Name()) || !d.Type().IsRegular() {
			return nil
		}
		return copyFileVerified(path, target)
	})
}

func isTempName(name string) bool {
	return strings.HasPrefix(name, ".nocturne-") && strings.HasSuffix(name, ".tmp")
}

var storedExt = map[string]bool{".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".flac": true,
	".zip": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".mp4": true, ".webm": true}

// zipDir writes all regular files of dataDir into a ZIP, skipping the backup
// folders (they would make every archive contain all previous archives).
func zipDir(w io.Writer, dataDir string, skip []string) error {
	zw := zip.NewWriter(w)
	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dataDir {
				return err
			}
			return nil
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil || rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == backupsFolder || strings.HasPrefix(slashRel, backupsFolder+"/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		for _, s := range skip {
			if s != "" && samePath(path, s) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() || !d.Type().IsRegular() || isTempName(d.Name()) || d.Name() == ".DS_Store" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = slashRel
		hdr.Method = zip.Deflate
		if storedExt[strings.ToLower(filepath.Ext(path))] {
			hdr.Method = zip.Store
		}
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, f)
		f.Close()
		return err
	})
	if err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

// uniquePath returns dir/name, or dir/name (2).ext etc. when taken.
func uniquePath(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; i < 10000; i++ {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	return p
}

// cleanFileName removes characters that are not allowed in file names.
func cleanFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "NOCTURNE"
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		r := []rune(strings.TrimSuffix(name, ext))
		if len(r) > 120 {
			r = r[:120]
		}
		name = string(r) + ext
	}
	return name
}
