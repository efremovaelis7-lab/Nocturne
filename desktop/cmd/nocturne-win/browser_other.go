//go:build !windows

package main

// Test-only stand-ins so the launcher logic can be exercised on Linux/macOS
// with NOCTURNE_BROWSER pointing to a Chromium binary.

import (
	"fmt"
	"os"
	"os/exec"
)

func findBrowser() string { return os.Getenv("NOCTURNE_BROWSER") }

func openDefault(url string) error { return exec.Command("xdg-open", url).Start() }

func showError(title, text string) { fmt.Fprintf(os.Stderr, "%s: %s\n", title, text) }
