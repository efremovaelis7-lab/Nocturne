package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const caseInsensitiveFS = true

// PlatformName is used in user-facing texts.
const PlatformName = "windows"

var (
	modShell32               = syscall.NewLazyDLL("shell32.dll")
	modOle32                 = syscall.NewLazyDLL("ole32.dll")
	procSHGetKnownFolderPath = modShell32.NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree        = modOle32.NewProc("CoTaskMemFree")

	folderDocuments = syscall.GUID{Data1: 0xFDD39AD0, Data2: 0x238F, Data3: 0x46AF, Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}}
	folderDownloads = syscall.GUID{Data1: 0x374DE290, Data2: 0x123F, Data3: 0x4565, Data4: [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}
)

func knownFolder(id *syscall.GUID) string {
	if procSHGetKnownFolderPath.Find() != nil {
		return ""
	}
	var p *uint16
	hr, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(id)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if hr != 0 || p == nil {
		return ""
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Pointer(uintptr(ptr) + 2)
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		h = os.Getenv("USERPROFILE")
	}
	return h
}

func platformSupportDir() string {
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "NOCTURNE")
	}
	return filepath.Join(home(), "AppData", "Roaming", "NOCTURNE")
}

func documentsDir() string {
	if p := knownFolder(&folderDocuments); p != "" {
		return p
	}
	return filepath.Join(home(), "Documents")
}

func downloadsDir() string {
	if p := knownFolder(&folderDownloads); p != "" {
		return p
	}
	return filepath.Join(home(), "Downloads")
}

// HiddenProcess hides the console window of helper processes.
func HiddenProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// openFolder shows a folder in Explorer.
func openFolder(dir string) error {
	cmd := exec.Command("explorer.exe", filepath.Clean(dir))
	return cmd.Start()
}

var (
	modUser32                = syscall.NewLazyDLL("user32.dll")
	procSHBrowseForFolderW   = modShell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = modShell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx       = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize       = modOle32.NewProc("CoUninitialize")
	procGetForegroundWindow  = modUser32.NewProc("GetForegroundWindow")
)

// browseInfo is BROWSEINFOW.
type browseInfo struct {
	owner       uintptr
	root        uintptr
	displayName *uint16
	title       *uint16
	flags       uint32
	callback    uintptr
	param       uintptr
	image       int32
}

// chooseFolder shows the standard Windows folder dialog on top of the
// NOCTURNE window (the window the person has just clicked in).
func chooseFolder(prompt string) (string, error) {
	if v := os.Getenv("NOCTURNE_TEST_CHOOSE"); v != "" {
		return v, nil
	}
	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// The dialog needs a single-threaded COM apartment on its own thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		const coinitApartmentThreaded = 0x2
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
		if int32(hr) >= 0 {
			defer procCoUninitialize.Call()
		}
		owner, _, _ := procGetForegroundWindow.Call()
		title, _ := syscall.UTF16PtrFromString(prompt)
		name := make([]uint16, syscall.MAX_PATH)
		const bifReturnOnlyFSDirs, bifEditBox, bifNewDialogStyle = 0x1, 0x10, 0x40
		bi := browseInfo{owner: owner, displayName: &name[0], title: title, flags: bifReturnOnlyFSDirs | bifEditBox | bifNewDialogStyle}
		pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			done <- result{"", errChooseCancelled}
			return
		}
		defer procCoTaskMemFree.Call(pidl)
		buf := make([]uint16, 32768)
		ok, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
		p := syscall.UTF16ToString(buf)
		if ok == 0 || p == "" {
			done <- result{"", errors.New("эту папку нельзя выбрать: выбери обычную папку на диске")}
			return
		}
		done <- result{filepath.Clean(p), nil}
	}()
	r := <-done
	return r.path, r.err
}
