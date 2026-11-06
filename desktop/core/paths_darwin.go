package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const caseInsensitiveFS = true

func home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		h = os.Getenv("HOME")
	}
	return h
}

func platformSupportDir() string {
	return filepath.Join(home(), "Library", "Application Support", "NOCTURNE")
}
func documentsDir() string { return filepath.Join(home(), "Documents") }
func downloadsDir() string { return filepath.Join(home(), "Downloads") }

// PlatformName is used in user-facing texts.
const PlatformName = "mac"

// openFolder shows a folder in Finder.
func openFolder(dir string) error { return exec.Command("/usr/bin/open", dir).Start() }

func chooseFolder(prompt string) (string, error) {
	if v := os.Getenv("NOCTURNE_TEST_CHOOSE"); v != "" {
		return v, nil
	}
	script := `tell me to activate
POSIX path of (choose folder with prompt "` + strings.ReplaceAll(prompt, `"`, `'`) + `")`
	out, err := exec.Command("/usr/bin/osascript", "-e", script).Output()
	if err != nil {
		return "", errChooseCancelled
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errChooseCancelled
	}
	return filepath.Clean(p), nil
}
