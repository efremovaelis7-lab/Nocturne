//go:build !darwin && !windows

package core

import (
	"os"
	"os/exec"
	"path/filepath"
)

const caseInsensitiveFS = false

// PlatformName is used in user-facing texts.
const PlatformName = "linux"

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func platformSupportDir() string { return filepath.Join(home(), ".config", "NOCTURNE") }
func documentsDir() string       { return filepath.Join(home(), "Documents") }
func downloadsDir() string       { return filepath.Join(home(), "Downloads") }

func openFolder(dir string) error {
	if os.Getenv("NOCTURNE_TEST_CHOOSE") != "" {
		return nil
	}
	return exec.Command("xdg-open", dir).Start()
}

func chooseFolder(prompt string) (string, error) {
	if v := os.Getenv("NOCTURNE_TEST_CHOOSE"); v != "" {
		return v, nil
	}
	return "", errChooseCancelled
}
