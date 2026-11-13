package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Version is reported by /api/settings and /api/ping.
const Version = "1.0 RC3.8.21 beta"

const (
	libraryFile   = "library.json"
	musicFolder   = "Музыка"
	backupsFolder = "Резервные копии"
)

// SupportDir is where config.json, runtime.json, the lock file and logs live.
// It is the same folder the original browser build used, so both builds
// share settings and can never open the library at the same time.
func SupportDir() string {
	if v := os.Getenv("NOCTURNE_TEST_SUPPORT"); v != "" {
		return v
	}
	return platformSupportDir()
}

// DefaultDataDir is the default library folder (~/Documents/NOCTURNE).
func DefaultDataDir() string {
	if v := os.Getenv("NOCTURNE_TEST_DATA"); v != "" {
		return v
	}
	return filepath.Join(documentsDir(), "NOCTURNE")
}

// DownloadsDir is where files produced by the page (archives, exports) go.
func DownloadsDir() string {
	if v := os.Getenv("NOCTURNE_TEST_DOWNLOADS"); v != "" {
		return v
	}
	return downloadsDir()
}

type config struct {
	DataDir   string `json:"dataDir"`
	BackupDir string `json:"backupDir,omitempty"`
}

func configPath() string { return filepath.Join(SupportDir(), "config.json") }

func readConfig() config {
	var c config
	if b, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		c.DataDir = DefaultDataDir()
	}
	c.DataDir = filepath.Clean(c.DataDir)
	if c.BackupDir != "" {
		c.BackupDir = filepath.Clean(c.BackupDir)
	}
	return c
}

func writeConfig(c config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(configPath(), b, 0o600)
}

func (c config) backupDir() string {
	if c.BackupDir != "" {
		return c.BackupDir
	}
	return filepath.Join(c.DataDir, backupsFolder)
}

// within reports whether child is the same as, or inside, parent.
func within(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if samePath(parent, child) {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if caseInsensitiveFS {
		return strings.EqualFold(a, b)
	}
	return a == b
}
