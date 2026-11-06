package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	user32      = syscall.NewLazyDLL("user32.dll")
	messageBoxW = user32.NewProc("MessageBoxW")
)

// findBrowser returns Microsoft Edge (part of Windows 10/11) or, failing
// that, Google Chrome. Both open the page as an app window.
func findBrowser() string {
	if p := os.Getenv("NOCTURNE_BROWSER"); p != "" {
		return p
	}
	var roots []string
	for _, v := range []string{"ProgramFiles(x86)", "ProgramFiles", "ProgramW6432", "LOCALAPPDATA"} {
		if d := os.Getenv(v); d != "" {
			roots = append(roots, d)
		}
	}
	for _, rel := range []string{`Microsoft\Edge\Application\msedge.exe`, `Google\Chrome\Application\chrome.exe`} {
		for _, r := range roots {
			p := filepath.Join(r, rel)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

func openDefault(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func showError(title, text string) {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	const mbOK, mbIconError, mbSetForeground = 0x0, 0x10, 0x10000
	_, _, _ = messageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), mbOK|mbIconError|mbSetForeground)
}
