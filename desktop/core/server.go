package core

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a NOCTURNE server.
type Options struct {
	// Platform is "mac" or "windows" for the desktop shells; it switches on
	// the small bridge script (native.js) that the page gets.
	Platform string
	// HTMLPath / AppJSPath are read on every request when they exist, so a
	// new index.html/app.js can be dropped in without rebuilding.
	HTMLPath, AppJSPath string
	// HTML / AppJS are used when the files above are missing.
	HTML, AppJS []byte
	Favicon     []byte
	// Ports are tried in order before falling back to a random port. A
	// stable port keeps the page origin (and its small UI preferences) stable.
	Ports []int
	Log   *log.Logger
	// OnQuit is called after the page asked the app to quit.
	OnQuit func()
}

// Server is the local NOCTURNE library server.
type Server struct {
	opts     Options
	mu       sync.Mutex // library writes, revision, config
	backupMu sync.Mutex
	cfg      config
	revision int64
	key      string
	lock     *fileLock
	ln       net.Listener
	srv      *http.Server
	port     int
	lastBeat atomic.Int64
	lastBye  atomic.Int64
	closed   atomic.Bool
}

var idRe = regexp.MustCompile(`^[\w-]{1,140}$`)

// New acquires the library lock and reads the settings.
func New(opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = log.New(io.Discard, "", 0)
	}
	if err := os.MkdirAll(SupportDir(), 0o700); err != nil {
		return nil, err
	}
	lk, err := acquireLock(filepath.Join(SupportDir(), "nocturne-server.lock"))
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, lock: lk, revision: 1}
	s.cfg = readConfig()
	if err := os.MkdirAll(s.cfg.DataDir, 0o700); err != nil {
		lk.release()
		return nil, fmt.Errorf("Не удалось открыть папку библиотеки %s: %v", s.cfg.DataDir, err)
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		lk.release()
		return nil, err
	}
	s.key = hex.EncodeToString(b[:])
	s.firstRunCopy()
	return s, nil
}

// Listen binds 127.0.0.1 on the first free preferred port.
func (s *Server) Listen() (int, error) {
	var ln net.Listener
	var err error
	for _, p := range s.opts.Ports {
		ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			break
		}
	}
	if ln == nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
	}
	s.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	rt, _ := json.Marshal(map[string]any{"port": s.port, "pid": os.Getpid(), "app": "NOCTURNE " + s.opts.Platform})
	_ = atomicWrite(filepath.Join(SupportDir(), "runtime.json"), rt, 0o600)
	s.srv = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 20 * time.Second}
	return s.port, nil
}

// URL is the address of the page.
func (s *Server) URL() string { return fmt.Sprintf("http://127.0.0.1:%d/", s.port) }

// Port returns the bound port.
func (s *Server) Port() int { return s.port }

// Serve blocks until Shutdown.
func (s *Server) Serve() error {
	go func() {
		time.Sleep(5 * time.Second)
		s.musicSelfCheck()
		s.autoBackup()
	}()
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown waits for running requests (a save in progress finishes), then
// releases the lock.
func (s *Server) Shutdown() {
	if s.closed.Swap(true) {
		return
	}
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_ = s.srv.Shutdown(ctx)
		cancel()
	}
	s.mu.Lock()
	rtPath := filepath.Join(SupportDir(), "runtime.json")
	if b, err := os.ReadFile(rtPath); err == nil {
		var rt struct{ Pid int }
		if json.Unmarshal(b, &rt) == nil && rt.Pid == os.Getpid() {
			os.Remove(rtPath)
		}
	}
	s.lock.release()
	s.mu.Unlock()
}

// LastHeartbeat is the time the page last reported it is open.
func (s *Server) LastHeartbeat() time.Time {
	v := s.lastBeat.Load()
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
}

// LastBye is the time the page said it is closing (pagehide). A reload says
// goodbye too, but the new page sends a heartbeat right after it.
func (s *Server) LastBye() time.Time {
	v := s.lastBye.Load()
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
}

func (s *Server) logf(format string, a ...any) { s.opts.Log.Printf(format, a...) }

// firstRunCopy keeps one untouched copy of library.json made before the
// desktop app wrote to the library for the first time.
func (s *Server) firstRunCopy() {
	marker := filepath.Join(SupportDir(), "app-first-run.json")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	lib := filepath.Join(s.cfg.DataDir, libraryFile)
	data, err := os.ReadFile(lib)
	if err == nil && len(data) > 0 {
		name := "library_до_приложения_" + time.Now().Format("2006-01-02_15-04-05") + ".json"
		if err := atomicWrite(filepath.Join(s.cfg.backupDir(), name), data, 0o600); err != nil {
			s.logf("first-run copy: %v", err)
			return
		}
	}
	b, _ := json.Marshal(map[string]any{"at": time.Now().Format(time.RFC3339), "version": Version})
	_ = atomicWrite(marker, b, 0o600)
}

// ---------------------------------------------------------------- HTTP

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.homepage)
	mux.HandleFunc("/app.js", s.appJS)
	mux.HandleFunc("/native.js", s.nativeJS)
	mux.HandleFunc("/favicon.png", s.favicon)
	mux.HandleFunc("/favicon.ico", s.favicon)
	mux.HandleFunc("/api/boot", s.boot)
	mux.HandleFunc("/api/save", s.save)
	mux.HandleFunc("/api/settings", s.settings)
	mux.HandleFunc("/api/settings/choose", s.choose)
	mux.HandleFunc("/api/backup", s.backup)
	mux.HandleFunc("/api/import-archive", s.restore)
	mux.HandleFunc("/api/music/", s.music)
	mux.HandleFunc("/media/music/", s.media)
	mux.HandleFunc("/api/quit", s.quit)
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 200, map[string]any{"name": "NOCTURNE", "version": Version})
	})
	mux.HandleFunc("/api/native/download", s.nativeDownload)
	mux.HandleFunc("/api/native/heartbeat", s.heartbeat)
	mux.HandleFunc("/api/native/bye", s.bye)
	mux.HandleFunc("/api/native/stay", s.stay)
	mux.HandleFunc("/api/native/backup", s.nativeBackup)
	mux.HandleFunc("/api/native/log", s.nativeLog)
	mux.HandleFunc("/api/native/rename", s.nativeRename)
	mux.HandleFunc("/api/native/open-folder", s.openFolderHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != "127.0.0.1:"+strconv.Itoa(s.port) && host != "localhost:"+strconv.Itoa(s.port) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

func sendJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	sendJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) secure(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Nocturne-Key") != s.key {
		jsonErr(w, http.StatusForbidden, "Неизвестная сессия NOCTURNE")
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
		jsonErr(w, http.StatusForbidden, "Запрос не из NOCTURNE")
		return false
	}
	return true
}

func readAsset(p string, fallback []byte) ([]byte, error) {
	if p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fs.ErrNotExist
}

var titleRe = regexp.MustCompile(`(?is)<title>.*?</title>`)
var cmdKeyRe = regexp.MustCompile(`⌘([A-Z])`)

// windowsTexts adjusts a few Mac-specific words for the Windows build.
func windowsTexts(b []byte) []byte {
	t := string(b)
	t = strings.NewReplacer("на Mac", "на компьютере", "с Mac", "с компьютера", "окне macOS", "окне Windows").Replace(t)
	t = cmdKeyRe.ReplaceAllString(t, "Ctrl+$1")
	return []byte(t)
}

func (s *Server) homepage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	b, err := readAsset(s.opts.HTMLPath, s.opts.HTML)
	if err != nil {
		http.Error(w, "NOCTURNE: index.html не найден", http.StatusInternalServerError)
		return
	}
	html := titleRe.ReplaceAllLiteralString(string(b), "<title>NOCTURNE</title>")
	head := `<link rel="icon" type="image/png" href="/favicon.png">`
	if s.opts.Platform != "" {
		head += `<script>window.__NOCTURNE_NATIVE={platform:"` + s.opts.Platform + `",version:"` + Version + `"};</script>`
	}
	if i := strings.Index(strings.ToLower(html), "</head>"); i >= 0 {
		html = html[:i] + head + html[i:]
	}
	if s.opts.Platform != "" {
		tag := `<script src="/native.js"></script>`
		if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
			html = html[:i] + tag + html[i:]
		} else {
			html += tag
		}
	}
	if s.opts.Platform == "windows" {
		html = string(windowsTexts([]byte(html)))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, html)
}

func (s *Server) appJS(w http.ResponseWriter, r *http.Request) {
	b, err := readAsset(s.opts.AppJSPath, s.opts.AppJS)
	if err != nil {
		http.Error(w, "NOCTURNE: app.js не найден", http.StatusInternalServerError)
		return
	}
	if s.opts.Platform == "windows" {
		b = windowsTexts(b)
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) nativeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, nativeScript)
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	if len(s.opts.Favicon) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(s.opts.Favicon)
}

func (s *Server) libraryPath() string { return filepath.Join(s.cfg.DataDir, libraryFile) }

func (s *Server) boot(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.libraryPath())
	var state json.RawMessage
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			jsonErr(w, 500, "Не удалось прочитать библиотеку; файл не изменён: "+err.Error())
			return
		}
		state = json.RawMessage("null")
	} else {
		if !json.Valid(data) {
			jsonErr(w, 500, "Файл library.json повреждён. NOCTURNE не станет его перезаписывать. Восстанови резервную копию.")
			return
		}
		state = data
	}
	sendJSON(w, 200, map[string]any{"state": state, "key": s.key, "revision": s.revision})
}

func validLibrary(state []byte) bool {
	if len(state) == 0 || !json.Valid(state) {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(state, &m); err != nil || m == nil {
		return false
	}
	b, ok := m["books"]
	return ok && len(b) > 0 && string(b) != "null"
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	var req struct {
		State        json.RawMessage `json:"state"`
		BaseRevision int64           `json:"baseRevision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<30))
	if err := dec.Decode(&req); err != nil {
		jsonErr(w, 400, "Неверные данные библиотеки")
		return
	}
	if !validLibrary(req.State) {
		jsonErr(w, 400, "Недопустимая структура библиотеки; файл не изменён")
		return
	}
	s.mu.Lock()
	if req.BaseRevision != s.revision {
		s.mu.Unlock()
		jsonErr(w, 409, "Другая вкладка уже изменила библиотеку. Сохрани отдельный JSON перед обновлением страницы.")
		return
	}
	if err := atomicWrite(s.libraryPath(), req.State, 0o600); err != nil {
		s.mu.Unlock()
		s.logf("save: %v", err)
		jsonErr(w, 500, "Не удалось записать книгу: "+err.Error())
		return
	}
	s.revision++
	rev := s.revision
	s.mu.Unlock()
	go s.autoBackup()
	sendJSON(w, 200, map[string]any{"revision": rev})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	sendJSON(w, 200, map[string]any{"dataDir": c.DataDir, "backupDir": c.backupDir(), "version": Version})
}

func onlyJunk(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		switch e.Name() {
		case ".DS_Store", "desktop.ini", "Thumbs.db", ".localized":
			continue
		}
		return false, nil
	}
	return true, nil
}

func (s *Server) choose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "data" && kind != "backup" {
		jsonErr(w, 400, "Неверный вид папки")
		return
	}
	prompt := "Выбери папку для библиотеки NOCTURNE"
	if kind == "backup" {
		prompt = "Выбери папку для резервных копий NOCTURNE"
	}
	chosen, err := chooseFolder(prompt)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	if kind == "backup" {
		if within(chosen, cfg.DataDir) {
			jsonErr(w, 400, "Папка копий не должна совпадать с библиотекой или содержать её. Выбери другую папку.")
			return
		}
		cfg.BackupDir = chosen
		if err := writeConfig(cfg); err != nil {
			jsonErr(w, 500, "Не удалось сохранить настройку: "+err.Error())
			return
		}
		s.cfg = cfg
		sendJSON(w, 200, map[string]any{"dataDir": cfg.DataDir, "backupDir": cfg.backupDir(), "message": "Папка резервных копий сохранена"})
		return
	}
	if samePath(chosen, cfg.DataDir) || within(cfg.DataDir, chosen) || within(chosen, cfg.DataDir) {
		jsonErr(w, 400, "Нельзя переносить библиотеку внутрь неё самой или в родительскую папку")
		return
	}
	if cfg.BackupDir != "" && within(chosen, cfg.BackupDir) {
		jsonErr(w, 400, "В выбранной папке или её подпапке лежат резервные копии. Выбери другую пустую папку.")
		return
	}
	empty, err := onlyJunk(chosen)
	if err != nil || !empty {
		jsonErr(w, 400, "Выбери пустую папку: существующие файлы не будут перезаписаны")
		return
	}
	var skip []string
	if within(cfg.DataDir, cfg.backupDir()) {
		skip = append(skip, cfg.backupDir())
	}
	if err := safeCopyDir(cfg.DataDir, chosen, skip); err != nil {
		jsonErr(w, 500, "Не удалось скопировать библиотеку: "+err.Error())
		return
	}
	if _, err := os.Stat(s.libraryPath()); err == nil {
		a, err1 := fileSHA(s.libraryPath())
		b, err2 := fileSHA(filepath.Join(chosen, libraryFile))
		if err1 != nil || err2 != nil || hex.EncodeToString(a) != hex.EncodeToString(b) {
			jsonErr(w, 500, "Проверка копии библиотеки не прошла. Старая папка сохранена.")
			return
		}
	}
	cfg.DataDir = chosen
	if err := writeConfig(cfg); err != nil {
		jsonErr(w, 500, "Не удалось сохранить настройку: "+err.Error())
		return
	}
	s.cfg = cfg
	sendJSON(w, 200, map[string]any{"dataDir": cfg.DataDir, "backupDir": cfg.backupDir(), "message": "Библиотека скопирована; прежняя папка сохранена как дополнительная копия"})
}

// makeZip writes a ZIP of the library into dir/name and returns its path.
// Music is left out of automatic and protective copies: it does not change
// when text is saved, and copying gigabytes every hour fills the disk and
// slows the whole computer down. Full archives (the "Архив" button) keep it.
func (s *Server) makeZip(dir, name string, withMusic bool) (string, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if dir == "" {
		dir = cfg.backupDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".nocturne-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	var skip []string
	if bdir := cfg.backupDir(); within(cfg.DataDir, bdir) {
		skip = append(skip, bdir)
	}
	if !withMusic {
		skip = append(skip, filepath.Join(cfg.DataDir, musicFolder))
	}
	err = zipDir(tmp, cfg.DataDir, skip)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return "", err
	}
	final := uniquePath(dir, name)
	if err := renameRetry(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return final, nil
}

var autoNameRe = regexp.MustCompile(`^NOCTURNE_auto_(\d{4}-\d{2}-\d{2})_\d{2}\.zip$`)

// pruneAutoBackups keeps the newest 24 hourly copies and, before them, the
// last copy of each day for two weeks. Only NOCTURNE_auto_*.zip files are
// touched; manual archives and other files stay.
func pruneAutoBackups(dir string, now time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && autoNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	cutoff := now.AddDate(0, 0, -14).Format("2006-01-02")
	seenDay := map[string]bool{}
	var removed []string
	for i, n := range names {
		day := autoNameRe.FindStringSubmatch(n)[1]
		keep := i < 24 || (!seenDay[day] && day >= cutoff)
		seenDay[day] = true
		if keep {
			continue
		}
		if os.Remove(filepath.Join(dir, n)) == nil {
			removed = append(removed, n)
		}
	}
	return removed
}

func (s *Server) autoBackup() {
	if s.closed.Load() {
		return
	}
	if !s.backupMu.TryLock() {
		return
	}
	defer s.backupMu.Unlock()
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if _, err := os.Stat(filepath.Join(cfg.DataDir, libraryFile)); err != nil {
		return
	}
	name := "NOCTURNE_auto_" + time.Now().Format("2006-01-02_15") + ".zip"
	if _, err := os.Stat(filepath.Join(cfg.backupDir(), name)); err == nil {
		return
	}
	if _, err := s.makeZip("", name, false); err != nil {
		s.logf("Автокопия: %v", err)
		return
	}
	if removed := pruneAutoBackups(cfg.backupDir(), time.Now()); len(removed) > 0 {
		s.logf("Автокопии: удалены старые %v", removed)
	}
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	s.backupMu.Lock()
	p, err := s.makeZip("", "NOCTURNE_"+time.Now().Format("2006-01-02_15-04-05")+".zip", true)
	s.backupMu.Unlock()
	if err != nil {
		s.logf("backup: %v", err)
		jsonErr(w, 500, "Не удалось создать архив: "+err.Error())
		return
	}
	f, err := os.Open(p)
	if err != nil {
		jsonErr(w, 500, "Не удалось прочитать архив: "+err.Error())
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=NOCTURNE_backup.zip")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "NOCTURNE_backup.zip", st.ModTime(), f)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	tmp, err := os.CreateTemp(SupportDir(), ".nocturne-import-*.tmp")
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	const maxArchive = 4 << 30
	n, err := io.Copy(tmp, io.LimitReader(r.Body, maxArchive+1))
	if err != nil {
		jsonErr(w, 400, "Не удалось получить архив: "+err.Error())
		return
	}
	if n > maxArchive {
		jsonErr(w, 413, "Архив слишком большой")
		return
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		jsonErr(w, 400, "Некорректный ZIP")
		return
	}
	// Locate library.json (at the root or inside one top folder).
	root := ""
	var libEntry *zip.File
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		if name == libraryFile {
			root, libEntry = "", f
			break
		}
		if strings.HasSuffix(name, "/"+libraryFile) && strings.Count(name, "/") == 1 && libEntry == nil {
			root, libEntry = strings.TrimSuffix(name, libraryFile), f
		}
	}
	if libEntry == nil {
		jsonErr(w, 400, "В архиве нет корректного library.json")
		return
	}
	libData, err := readZipEntry(libEntry, 1<<30)
	if err != nil || !validLibrary(libData) {
		jsonErr(w, 400, "В архиве нет корректного library.json")
		return
	}
	type item struct {
		f   *zip.File
		rel string
	}
	var items []item
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, root) || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		rel := strings.TrimPrefix(name, root)
		if rel == "" || strings.HasSuffix(rel, "/") || f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			jsonErr(w, 400, "Архив с символьными ссылками не поддерживается")
			return
		}
		clean := path.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.Contains(clean, ":") {
			jsonErr(w, 400, "Небезопасный путь в архиве: "+rel)
			return
		}
		if clean == libraryFile || clean == backupsFolder || strings.HasPrefix(clean, backupsFolder+"/") || path.Base(clean) == ".DS_Store" || isTempName(path.Base(clean)) {
			continue
		}
		if f.UncompressedSize64 > 2<<30 {
			jsonErr(w, 400, "Слишком большой файл в архиве")
			return
		}
		items = append(items, item{f, clean})
	}
	// Protective copy of the current library first.
	s.backupMu.Lock()
	_, err = s.makeZip("", "NOCTURNE_до_импорта_"+time.Now().Format("20060102_150405")+".zip", false)
	s.backupMu.Unlock()
	if err != nil {
		jsonErr(w, 500, "Перед импортом не удалось создать защитную копию")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		rc, err := it.f.Open()
		if err != nil {
			jsonErr(w, 500, "Не удалось прочитать "+it.rel+": "+err.Error())
			return
		}
		_, err = atomicWriteFrom(filepath.Join(s.cfg.DataDir, filepath.FromSlash(it.rel)), rc, 0o600, 2<<30)
		rc.Close()
		if err != nil {
			jsonErr(w, 500, "Не удалось записать "+it.rel+": "+err.Error())
			return
		}
	}
	if err := atomicWrite(s.libraryPath(), libData, 0o600); err != nil {
		jsonErr(w, 500, "Не удалось записать библиотеку: "+err.Error())
		return
	}
	s.revision++
	sendJSON(w, 200, map[string]any{"ok": true, "revision": s.revision})
}

func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

func (s *Server) musicPath(id string) string {
	s.mu.Lock()
	d := s.cfg.DataDir
	s.mu.Unlock()
	return filepath.Join(d, musicFolder, id)
}

func (s *Server) music(w http.ResponseWriter, r *http.Request) {
	id, _ := strings.CutPrefix(r.URL.Path, "/api/music/")
	if !s.secure(w, r) {
		return
	}
	if !idRe.MatchString(id) {
		jsonErr(w, 400, "Неверный идентификатор")
		return
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		if _, err := atomicWriteFrom(s.musicPath(id), r.Body, 0o600, 2<<30); err != nil {
			jsonErr(w, 500, "Не получилось сохранить аудио: "+err.Error())
			return
		}
		sendJSON(w, 200, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := os.Remove(s.musicPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			jsonErr(w, 500, "Не удалось удалить аудио: "+err.Error())
			return
		}
		sendJSON(w, 200, map[string]any{"ok": true})
	default:
		jsonErr(w, 405, "Метод не разрешён")
	}
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	id, _ := strings.CutPrefix(r.URL.Path, "/media/music/")
	if !idRe.MatchString(id) {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	p := s.musicPath(id)
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	if f, err := os.Open(p); err != nil {
		s.debugf("media open %s: %v", id, err)
		jsonErr(w, 500, "Не удалось открыть аудиофайл: "+err.Error())
		return
	} else {
		f.Close()
	}
	w.Header().Set("Content-Type", sniffAudio(p))
	w.Header().Set("Cache-Control", "no-cache")
	rw := &statusWriter{ResponseWriter: w, code: 200}
	http.ServeFile(rw, r, p)
	if rw.code >= 400 || r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
		s.debugf("media %s range=%q -> %d ua=%q", id, r.Header.Get("Range"), rw.code, r.Header.Get("User-Agent"))
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// sniffAudio names the audio format from the first bytes of the file
// (music files are stored without extensions).
func sniffAudio(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	b := make([]byte, 12)
	n, _ := io.ReadFull(f, b)
	b = b[:n]
	switch {
	case len(b) >= 3 && string(b[:3]) == "ID3":
		return "audio/mpeg"
	case len(b) >= 8 && string(b[4:8]) == "ftyp":
		return "audio/mp4"
	case len(b) >= 4 && string(b[:4]) == "OggS":
		return "audio/ogg"
	case len(b) >= 4 && string(b[:4]) == "fLaC":
		return "audio/flac"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xF6) == 0xF0:
		return "audio/aac"
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0:
		return "audio/mpeg"
	}
	return "audio/mpeg"
}

// musicSelfCheck records whether music files can be read (diagnostics).
func (s *Server) musicSelfCheck() {
	s.mu.Lock()
	dir := filepath.Join(s.cfg.DataDir, musicFolder)
	s.mu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.debugf("selfcheck readdir: %v", err)
		return
	}
	bad, first := 0, ""
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			bad++
			if first == "" {
				st, _ := os.Stat(filepath.Join(dir, e.Name()))
				mode := ""
				if st != nil {
					mode = st.Mode().String()
				}
				first = fmt.Sprintf("%s: %v (mode %s, uid %d)", e.Name(), err, mode, os.Getuid())
			}
			continue
		}
		f.Close()
	}
	s.debugf("selfcheck music: %d files, %d unreadable %s", len(entries), bad, first)
}

// debugf writes temporary diagnostics next to the library (music-debug.log).
func (s *Server) debugf(format string, a ...any) {
	s.logf(format, a...)
	s.mu.Lock()
	dir := s.cfg.DataDir
	s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "music-debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func (s *Server) nativeLog(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 2000))
	s.debugf("page: %s", string(b))
	sendJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) quit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	sendJSON(w, 200, map[string]any{"ok": true})
	if s.opts.OnQuit != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.opts.OnQuit()
		}()
	}
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	s.lastBeat.Store(time.Now().UnixNano())
	sendJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) bye(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	s.lastBye.Store(time.Now().UnixNano())
	sendJSON(w, 200, map[string]any{"ok": true})
}

// stay is called by a second NOCTURNE.exe that is about to open one more
// window for this server: the server must not stop in the seconds before the
// new window sends its first heartbeat. It can only keep the server running,
// so it needs no key; requests from web pages (with an Origin) are refused.
func (s *Server) stay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
		jsonErr(w, 403, "Запрещено")
		return
	}
	s.lastBeat.Store(time.Now().UnixNano())
	sendJSON(w, 200, map[string]any{"ok": true})
}

// KeepRunning asks a running NOCTURNE server to wait for a new window.
func KeepRunning(port int) {
	c := &http.Client{Timeout: 2 * time.Second}
	if resp, err := c.Post(fmt.Sprintf("http://127.0.0.1:%d/api/native/stay", port), "text/plain", nil); err == nil {
		resp.Body.Close()
	}
}

// nativeDownload saves a file the page wants to "download" into Downloads.
// WebKit windows have no download bar, so the bridge script posts here.
func (s *Server) nativeDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	name := cleanFileName(r.URL.Query().Get("name"))
	dir := DownloadsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		jsonErr(w, 500, "Нет доступа к папке Загрузки: "+err.Error())
		return
	}
	dest := uniquePath(dir, name)
	if _, err := atomicWriteFrom(dest, r.Body, 0o644, 8<<30); err != nil {
		jsonErr(w, 500, "Не удалось сохранить файл: "+err.Error())
		return
	}
	sendJSON(w, 200, map[string]any{"path": dest, "name": filepath.Base(dest), "folder": dir})
}

// nativeBackup builds the full archive (with music) straight into Downloads.
// The page then gets only a tiny placeholder instead of gigabytes of data.
func (s *Server) nativeBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	s.backupMu.Lock()
	p, err := s.makeZip(DownloadsDir(), "NOCTURNE_полный_архив_"+time.Now().Format("2006-01-02_15-04-05")+".zip", true)
	s.backupMu.Unlock()
	if err != nil {
		s.logf("native backup: %v", err)
		jsonErr(w, 500, "Не удалось создать архив: "+err.Error())
		return
	}
	var size int64
	if st, err := os.Stat(p); err == nil {
		size = st.Size()
	}
	sendJSON(w, 200, map[string]any{"path": p, "name": filepath.Base(p), "size": size})
}

// nativeRename gives an archive made by nativeBackup the name the page chose.
func (s *Server) nativeRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	dir := DownloadsDir()
	from := filepath.Clean(r.URL.Query().Get("from"))
	base := filepath.Base(from)
	if !samePath(filepath.Dir(from), dir) || !strings.HasPrefix(base, "NOCTURNE_") || !strings.HasSuffix(strings.ToLower(base), ".zip") {
		jsonErr(w, 400, "Неверный файл")
		return
	}
	name := cleanFileName(r.URL.Query().Get("name"))
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	if name == base {
		sendJSON(w, 200, map[string]any{"path": from, "name": base})
		return
	}
	dest := uniquePath(dir, name)
	if err := os.Rename(from, dest); err != nil {
		jsonErr(w, 500, "Не удалось переименовать архив: "+err.Error())
		return
	}
	sendJSON(w, 200, map[string]any{"path": dest, "name": filepath.Base(dest)})
}

// ---------------------------------------------------------------- old build

// RunningServer describes a NOCTURNE server found through runtime.json.
type RunningServer struct {
	Port int
	Pid  int
	App  string
}

// FindRunning returns the server recorded in runtime.json if it answers.
func FindRunning() (*RunningServer, bool) {
	b, err := os.ReadFile(filepath.Join(SupportDir(), "runtime.json"))
	if err != nil {
		return nil, false
	}
	var rt struct {
		Port int    `json:"port"`
		Pid  int    `json:"pid"`
		App  string `json:"app"`
	}
	if json.Unmarshal(b, &rt) != nil || rt.Port <= 0 {
		return nil, false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", rt.Port))
	if err != nil {
		return &RunningServer{Port: rt.Port, Pid: rt.Pid, App: rt.App}, false
	}
	resp.Body.Close()
	return &RunningServer{Port: rt.Port, Pid: rt.Pid, App: rt.App}, resp.StatusCode == 200
}

// StopRunning asks a running NOCTURNE server (for example the old browser
// build) to finish its work and exit, then waits until the lock is free.
func StopRunning(timeout time.Duration) error {
	rs, alive := FindRunning()
	if rs != nil && alive {
		base := fmt.Sprintf("http://127.0.0.1:%d", rs.Port)
		c := &http.Client{Timeout: 10 * time.Second}
		resp, err := c.Get(base + "/api/boot")
		if err == nil {
			var boot struct {
				Key string `json:"key"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&boot)
			resp.Body.Close()
			req, _ := http.NewRequest(http.MethodPost, base+"/api/quit", nil)
			req.Header.Set("X-Nocturne-Key", boot.Key)
			if resp2, err := c.Do(req); err == nil {
				resp2.Body.Close()
			}
		}
	}
	lockPath := filepath.Join(SupportDir(), "nocturne-server.lock")
	deadline := time.Now().Add(timeout)
	killed := false
	for time.Now().Before(deadline) {
		if lk, err := acquireLock(lockPath); err == nil {
			lk.release()
			return nil
		}
		if !killed && rs != nil && time.Until(deadline) < timeout/2 {
			killed = true
			terminateNocturne(rs.Pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errLocked
}

// openFolderHandler shows the library or backup folder in Explorer / Finder.
func (s *Server) openFolderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	dir := c.DataDir
	switch r.URL.Query().Get("kind") {
	case "backup":
		dir = c.backupDir()
	case "downloads":
		dir = DownloadsDir()
	}
	_ = os.MkdirAll(dir, 0o700)
	if err := openFolder(dir); err != nil {
		jsonErr(w, 500, "Не удалось открыть папку: "+err.Error())
		return
	}
	sendJSON(w, 200, map[string]any{"ok": true, "path": dir})
}
