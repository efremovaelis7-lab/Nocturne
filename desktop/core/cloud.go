package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Синхронизация через Яндекс Диск пользователя.
//
// The server only carries files between the page and the user's own Yandex
// Disk; what to send and how to merge two devices is decided by the page
// (app.js). NOCTURNE gets access to its own folder on the Disk only
// ("Приложения/NOCTURNE", OAuth scope cloud_api:disk.app_folder), so the rest
// of the Disk stays invisible to it. Nothing leaves the computer until the
// user connects a Disk.

// YandexClientID is the NOCTURNE app registered at oauth.yandex.ru. It is not
// a secret. NOCTURNE_YANDEX_CLIENT_ID overrides it (tests).
var YandexClientID = "b571af05b0c640ac8875c89dc5c5a343"

const yandexAPIDefault = "https://cloud-api.yandex.net"

func yandexAPI() string {
	if v := os.Getenv("NOCTURNE_YANDEX_API"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return yandexAPIDefault
}

func yandexClientID() string {
	if v := os.Getenv("NOCTURNE_YANDEX_CLIENT_ID"); v != "" {
		return v
	}
	return YandexClientID
}

type cloudAccount struct {
	Provider    string `json:"provider"`
	Token       string `json:"token"`
	Login       string `json:"login,omitempty"`
	Name        string `json:"name,omitempty"`
	ConnectedAt string `json:"connectedAt"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

type cloudState struct {
	mu   sync.Mutex
	acct *cloudAccount
	dirs map[string]bool // folders already created on the Disk this session
}

var cloud cloudState

func cloudAccountPath() string { return filepath.Join(SupportDir(), "cloud.json") }
func cloudMetaPath() string    { return filepath.Join(SupportDir(), "cloud-sync.json") }

func (c *cloudState) account() *cloudAccount {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acct == nil {
		var a cloudAccount
		if b, err := os.ReadFile(cloudAccountPath()); err == nil && json.Unmarshal(b, &a) == nil && a.Token != "" {
			c.acct = &a
		}
	}
	return c.acct
}

func (c *cloudState) setAccount(a *cloudAccount) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs = nil
	if a == nil {
		c.acct = nil
		err1 := os.Remove(cloudAccountPath())
		err2 := os.Remove(cloudMetaPath())
		if err1 != nil && !errors.Is(err1, os.ErrNotExist) {
			return err1
		}
		if err2 != nil && !errors.Is(err2, os.ErrNotExist) {
			return err2
		}
		return nil
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	if err := atomicWrite(cloudAccountPath(), b, 0o600); err != nil {
		return err
	}
	c.acct = a
	return nil
}

var (
	apiClient  = &http.Client{Timeout: 45 * time.Second}
	fileClient = &http.Client{Timeout: 10 * time.Minute}
)

type cloudError struct {
	status int
	code   string
	msg    string
}

func (e *cloudError) Error() string { return e.msg }

var errNoCloud = &cloudError{http.StatusConflict, "off", "Яндекс Диск не подключён"}

// yandex calls the Disk REST API with the user's token.
func yandex(method, path string, q url.Values, token string) (*http.Response, error) {
	u := yandexAPI() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, &cloudError{http.StatusBadGateway, "network", "Нет связи с Яндекс Диском"}
	}
	if err := yandexStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func yandexStatus(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return &cloudError{http.StatusUnauthorized, "auth", "Яндекс Диск больше не пускает NOCTURNE — подключи его заново"}
	case resp.StatusCode == http.StatusNotFound:
		return &cloudError{http.StatusNotFound, "missing", "Нет такого файла на Диске"}
	case resp.StatusCode == http.StatusConflict:
		return &cloudError{http.StatusConflict, "conflict", "Такая папка уже есть или нет папки выше"}
	case resp.StatusCode == http.StatusInsufficientStorage, resp.StatusCode == http.StatusRequestEntityTooLarge:
		return &cloudError{http.StatusInsufficientStorage, "full", "На Яндекс Диске закончилось место"}
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusLocked, resp.StatusCode >= 500:
		return &cloudError{http.StatusServiceUnavailable, "busy", "Яндекс Диск сейчас занят — попробую позже"}
	case resp.StatusCode >= 400:
		var x struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&x)
		msg := "Яндекс Диск ответил ошибкой " + resp.Status
		if x.Message != "" {
			msg += ": " + x.Message
		}
		return &cloudError{http.StatusBadGateway, "error", msg}
	}
	return nil
}

var cloudPathRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// diskPath turns a page path ("nocturne/index.json") into a path inside
// NOCTURNE's own folder on the Disk.
func diskPath(rel string) (string, error) {
	if len(rel) > 300 || !cloudPathRe.MatchString(rel) || strings.Contains(rel, "..") {
		return "", &cloudError{http.StatusBadRequest, "path", "Недопустимый путь"}
	}
	return "app:/" + rel, nil
}

func cloudFail(w http.ResponseWriter, err error) {
	var ce *cloudError
	if errors.As(err, &ce) {
		sendJSON(w, ce.status, map[string]any{"error": ce.msg, "code": ce.code})
		return
	}
	sendJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "code": "error"})
}

func (s *Server) cloudRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/cloud/status", s.cloudStatus)
	mux.HandleFunc("/api/cloud/connect", s.cloudConnect)
	mux.HandleFunc("/api/cloud/disconnect", s.cloudDisconnect)
	mux.HandleFunc("/api/cloud/file", s.cloudFile)
	mux.HandleFunc("/api/cloud/list", s.cloudList)
	mux.HandleFunc("/api/cloud/delete", s.cloudDelete)
	mux.HandleFunc("/api/cloud/meta", s.cloudMeta)
}

func (s *Server) cloudStatus(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	out := map[string]any{"available": yandexClientID() != "", "connected": false}
	if id := yandexClientID(); id != "" {
		out["authUrl"] = "https://oauth.yandex.ru/authorize?response_type=token&force_confirm=yes&client_id=" + url.QueryEscape(id)
	}
	if a := cloud.account(); a != nil {
		out["connected"] = true
		out["login"] = a.Login
		out["name"] = a.Name
		out["connectedAt"] = a.ConnectedAt
		out["expiresAt"] = a.ExpiresAt
	}
	sendJSON(w, 200, out)
}

var tokenInURL = regexp.MustCompile(`access_token=([^&\s#]+)`)
var expiresInURL = regexp.MustCompile(`expires_in=([0-9]+)`)

// cloudConnect checks the token the user pasted (or the whole address from
// the Yandex page) and remembers it.
func (s *Server) cloudConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		jsonErr(w, 400, "Некорректный запрос")
		return
	}
	raw := strings.TrimSpace(in.Token)
	token, expires := raw, ""
	if m := tokenInURL.FindStringSubmatch(raw); m != nil {
		token, _ = url.QueryUnescape(m[1])
		if e := expiresInURL.FindStringSubmatch(raw); e != nil {
			var sec int64
			fmt.Sscan(e[1], &sec)
			if sec > 0 {
				expires = time.Now().Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
			}
		}
	}
	if len(token) < 20 || len(token) > 400 || strings.ContainsAny(token, " \t\r\n") {
		sendJSON(w, 400, map[string]any{"error": "Это не похоже на код от Яндекса. Скопируй длинную строку со страницы Яндекса целиком.", "code": "format"})
		return
	}
	resp, err := yandex(http.MethodGet, "/v1/disk/", nil, token)
	if err != nil {
		var ce *cloudError
		if errors.As(err, &ce) && ce.code == "auth" {
			sendJSON(w, 400, map[string]any{"error": "Яндекс не принял этот код. Получи новый и вставь его целиком.", "code": "auth"})
			return
		}
		cloudFail(w, err)
		return
	}
	defer resp.Body.Close()
	var disk struct {
		User struct {
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&disk)
	a := &cloudAccount{Provider: "yandex", Token: token, Login: disk.User.Login, Name: disk.User.DisplayName,
		ConnectedAt: time.Now().Format(time.RFC3339), ExpiresAt: expires}
	if err := cloud.setAccount(a); err != nil {
		jsonErr(w, 500, "Не удалось запомнить подключение: "+err.Error())
		return
	}
	_ = os.Remove(cloudMetaPath())
	s.logf("Яндекс Диск подключён (%s)", a.Login)
	sendJSON(w, 200, map[string]any{"ok": true, "login": a.Login, "name": a.Name})
}

func (s *Server) cloudDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	if err := cloud.setAccount(nil); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	s.logf("Яндекс Диск отключён")
	sendJSON(w, 200, map[string]any{"ok": true})
}

// cloudFile: GET downloads a file from NOCTURNE's folder on the Disk, POST
// uploads the request body there (replacing an existing file).
func (s *Server) cloudFile(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	a := cloud.account()
	if a == nil {
		cloudFail(w, errNoCloud)
		return
	}
	p, err := diskPath(r.URL.Query().Get("path"))
	if err != nil {
		cloudFail(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		href, err := yandexHref(http.MethodGet, "/v1/disk/resources/download", url.Values{"path": {p}}, a.Token)
		if err != nil {
			cloudFail(w, err)
			return
		}
		resp, err := fileClient.Get(href)
		if err != nil {
			cloudFail(w, &cloudError{http.StatusBadGateway, "network", "Нет связи с Яндекс Диском"})
			return
		}
		defer resp.Body.Close()
		if err := yandexStatus(resp); err != nil {
			cloudFail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.Copy(w, resp.Body)
	case http.MethodPost:
		const maxFile = 512 << 20
		if r.ContentLength > maxFile {
			jsonErr(w, 413, "Файл слишком большой")
			return
		}
		if err := s.cloudMkdirs(p, a.Token); err != nil {
			cloudFail(w, err)
			return
		}
		href, err := yandexHref(http.MethodGet, "/v1/disk/resources/upload", url.Values{"path": {p}, "overwrite": {"true"}}, a.Token)
		if err != nil {
			cloudFail(w, err)
			return
		}
		req, err := http.NewRequest(http.MethodPut, href, io.LimitReader(r.Body, maxFile))
		if err != nil {
			cloudFail(w, err)
			return
		}
		if r.ContentLength >= 0 {
			req.ContentLength = r.ContentLength
		}
		resp, err := fileClient.Do(req)
		if err != nil {
			cloudFail(w, &cloudError{http.StatusBadGateway, "network", "Нет связи с Яндекс Диском"})
			return
		}
		defer resp.Body.Close()
		if err := yandexStatus(resp); err != nil {
			cloudFail(w, err)
			return
		}
		sendJSON(w, 200, map[string]any{"ok": true})
	default:
		jsonErr(w, 405, "Метод не разрешён")
	}
}

// yandexHref asks the API for a one-time download or upload address.
func yandexHref(method, path string, q url.Values, token string) (string, error) {
	resp, err := yandex(method, path, q, token)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var x struct {
		Href string `json:"href"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&x); err != nil || x.Href == "" {
		return "", &cloudError{http.StatusBadGateway, "error", "Яндекс Диск прислал непонятный ответ"}
	}
	return x.Href, nil
}

// cloudMkdirs creates the folders above a file (app:/a/b/c.json → app:/a, app:/a/b).
func (s *Server) cloudMkdirs(p, token string) error {
	rel := strings.TrimPrefix(p, "app:/")
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		dir := "app:/" + strings.Join(parts[:i], "/")
		cloud.mu.Lock()
		done := cloud.dirs[dir]
		cloud.mu.Unlock()
		if done {
			continue
		}
		resp, err := yandex(http.MethodPut, "/v1/disk/resources", url.Values{"path": {dir}}, token)
		if err != nil {
			var ce *cloudError
			// 409: the folder is already there
			if !(errors.As(err, &ce) && ce.code == "conflict") {
				return err
			}
		} else {
			resp.Body.Close()
		}
		cloud.mu.Lock()
		if cloud.dirs == nil {
			cloud.dirs = map[string]bool{}
		}
		cloud.dirs[dir] = true
		cloud.mu.Unlock()
	}
	return nil
}

func (s *Server) cloudList(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	a := cloud.account()
	if a == nil {
		cloudFail(w, errNoCloud)
		return
	}
	p, err := diskPath(r.URL.Query().Get("path"))
	if err != nil {
		cloudFail(w, err)
		return
	}
	resp, err := yandex(http.MethodGet, "/v1/disk/resources", url.Values{"path": {p}, "limit": {"2000"},
		"fields": {"_embedded.items.name,_embedded.items.type,_embedded.items.size,_embedded.items.modified"}}, a.Token)
	if err != nil {
		var ce *cloudError
		if errors.As(err, &ce) && ce.code == "missing" {
			sendJSON(w, 200, map[string]any{"items": []any{}})
			return
		}
		cloudFail(w, err)
		return
	}
	defer resp.Body.Close()
	var x struct {
		Embedded struct {
			Items []map[string]any `json:"items"`
		} `json:"_embedded"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&x)
	if x.Embedded.Items == nil {
		x.Embedded.Items = []map[string]any{}
	}
	sendJSON(w, 200, map[string]any{"items": x.Embedded.Items})
}

func (s *Server) cloudDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "Метод не разрешён")
		return
	}
	if !s.secure(w, r) {
		return
	}
	a := cloud.account()
	if a == nil {
		cloudFail(w, errNoCloud)
		return
	}
	p, err := diskPath(r.URL.Query().Get("path"))
	if err != nil {
		cloudFail(w, err)
		return
	}
	resp, err := yandex(http.MethodDelete, "/v1/disk/resources", url.Values{"path": {p}, "permanently": {"true"}}, a.Token)
	if err != nil {
		var ce *cloudError
		if errors.As(err, &ce) && ce.code == "missing" {
			sendJSON(w, 200, map[string]any{"ok": true})
			return
		}
		cloudFail(w, err)
		return
	}
	resp.Body.Close()
	sendJSON(w, 200, map[string]any{"ok": true})
}

// cloudMeta keeps the page's notes about the last sync on this device (what
// the shared copy looked like then), next to the settings, not in the library.
func (s *Server) cloudMeta(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		b, err := os.ReadFile(cloudMetaPath())
		if err != nil || !json.Valid(b) {
			b = []byte("{}")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	case http.MethodPost:
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil || !json.Valid(b) {
			jsonErr(w, 400, "Некорректные данные")
			return
		}
		if cloud.account() == nil {
			cloudFail(w, errNoCloud)
			return
		}
		if err := atomicWrite(cloudMetaPath(), b, 0o600); err != nil {
			jsonErr(w, 500, err.Error())
			return
		}
		sendJSON(w, 200, map[string]any{"ok": true})
	default:
		jsonErr(w, 405, "Метод не разрешён")
	}
}
