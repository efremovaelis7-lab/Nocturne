// NOCTURNE.exe — Windows build: one file with the library server inside.
// It starts the server, opens the library in a Microsoft Edge window in
// app mode (no tabs or address bar; Edge is part of Windows 10 and 11) and
// stops the server after the window is closed. The page saves every change
// within half a second, and the "Завершить NOCTURNE" button waits for the
// last save before closing (native.js → windowsQuit).
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"nocturne/core"
)

//go:embed assets/index.html
var indexHTML []byte

//go:embed assets/app.js
var appJS []byte

//go:embed assets/icon.png
var iconPNG []byte

// How long the page may stay silent before the window counts as closed.
// While NOCTURNE's own Edge window is open, silence alone never stops the
// server: a minimized, covered or busy window may stay quiet for a while,
// and a laptop lid closes for hours. Only when the page lives in an
// ordinary browser tab (no Edge or Chrome found) does long silence count.
//
// RC3.8.11: the Edge process NOCTURNE starts may hand its window over to
// another Edge process and exit (seen on a real PC: the server stopped
// while the window stayed open, and later edits were not saved). So the
// window counts as closed only when the Edge profile of NOCTURNE is no
// longer in use (Chromium deletes its "lockfile" on exit) or when the
// page itself said goodbye (pagehide) and stayed silent.
const (
	silentAfterBrowserExit = 12 * time.Second
	silentInDefaultBrowser = 2 * time.Hour
	startGrace             = 40 * time.Second
	sleepGap               = 15 * time.Second
)

func main() {
	_ = os.MkdirAll(core.SupportDir(), 0o700)
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if lf, err := os.OpenFile(filepath.Join(core.SupportDir(), "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		if st, err := lf.Stat(); err == nil && st.Size() > 4<<20 {
			_ = lf.Truncate(0)
		}
		logger = log.New(lf, "", log.LstdFlags)
	}
	logger.Printf("NOCTURNE %s (Windows) starting", core.Version)

	// index.html / app.js live inside the exe. A copy next to the settings lets
	// a newer interface be dropped in without a new exe (same as on Mac).
	res := filepath.Join(core.SupportDir(), "app")
	_ = os.MkdirAll(res, 0o700)
	writeIfChanged(filepath.Join(res, "index.html"), indexHTML)
	writeIfChanged(filepath.Join(res, "app.js"), appJS)

	quit := make(chan struct{}, 1)
	opts := core.Options{
		Platform: "windows",
		HTML:     indexHTML, AppJS: appJS, Favicon: iconPNG,
		Ports:  []int{41731, 41732, 41733, 41734, 41735, 41736, 41737, 41738},
		Log:    logger,
		OnQuit: func() { quit <- struct{}{} },
	}
	if os.Getenv("NOCTURNE_USE_APP_COPY") == "1" {
		opts.HTMLPath, opts.AppJSPath = filepath.Join(res, "index.html"), filepath.Join(res, "app.js")
	}

	var srv *core.Server
	var err error
	for i := 0; i < 15; i++ {
		srv, err = core.New(opts)
		if !errors.Is(err, core.ErrLocked) {
			break
		}
		// Another NOCTURNE window is open: show it instead of a second copy.
		if rs, alive := core.FindRunning(); rs != nil && alive {
			logger.Printf("already running on port %d, opening another window", rs.Port)
			core.KeepRunning(rs.Port)
			if _, e := openWindow(fmt.Sprintf("http://127.0.0.1:%d/", rs.Port), logger); e != nil {
				fail("Не удалось открыть окно NOCTURNE", e.Error(), logger)
			}
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		if errors.Is(err, core.ErrLocked) {
			fail("NOCTURNE уже работает", "Библиотека занята другой копией NOCTURNE. Закрой её и открой NOCTURNE снова.", logger)
		}
		fail("Не удалось открыть библиотеку", err.Error()+"\n\nПодробности: "+filepath.Join(core.SupportDir(), "server.log"), logger)
	}
	if _, err := srv.Listen(); err != nil {
		srv.Shutdown()
		fail("Не удалось запустить NOCTURNE", err.Error(), logger)
	}
	url := srv.URL()
	logger.Printf("library server: %s", url)
	served := make(chan struct{})
	go func() {
		if err := srv.Serve(); err != nil {
			logger.Printf("serve: %v", err)
		}
		close(served)
	}()

	browser, err := openWindow(url, logger)
	if err != nil {
		srv.Shutdown()
		fail("Не удалось открыть окно NOCTURNE", err.Error(), logger)
	}
	exited := make(chan struct{})
	if browser != nil {
		go func() { _ = browser.Wait(); close(exited) }()
	}

	started := time.Now()
	browserGone := browser == nil
	tracked := browser != nil
	processExited := false
	profileLock := filepath.Join(core.SupportDir(), "Window", "lockfile")
	lockMissing := 0
	awake, lastTick := time.Now(), time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case <-quit:
			logger.Printf("stop: quit requested by the page")
			break loop
		case <-served:
			logger.Printf("stop: server closed")
			break loop
		case <-exited:
			processExited = true
			exited = nil
			if fileExists(profileLock) {
				logger.Printf("browser process exited, the window lives on in another Edge process")
			}
		case <-tick.C:
			// After sleep the page needs a moment to wake up and send a heartbeat.
			if time.Since(lastTick) > sleepGap {
				logger.Printf("woke up after %v", time.Since(lastTick).Round(time.Second))
				awake = time.Now()
			}
			lastTick = time.Now()
			if tracked && processExited {
				if fileExists(profileLock) {
					lockMissing = 0
				} else {
					lockMissing++
				}
				// two ticks in a row: Edge may re-create the lock while relaunching
				browserGone = lockMissing >= 2
			}
			last := srv.LastHeartbeat()
			if last.Before(awake) {
				last = awake
			}
			if srv.LastHeartbeat().IsZero() {
				if time.Since(started) > startGrace && browserGone && time.Since(awake) > startGrace {
					logger.Printf("stop: the window never answered")
					break loop
				}
				continue
			}
			silent := time.Since(last)
			bye := srv.LastBye()
			saidBye := !bye.IsZero() && bye.After(srv.LastHeartbeat())
			if (tracked && browserGone && silent > silentAfterBrowserExit) || (saidBye && silent > silentAfterBrowserExit) || (!tracked && silent > silentInDefaultBrowser) {
				logger.Printf("stop: window closed (silent %v, browser gone %v, page said bye %v)", silent.Round(time.Second), browserGone, saidBye)
				break loop
			}
		}
	}
	srv.Shutdown()
	if browser != nil && !browserGone {
		_ = browser.Process.Kill()
	}
	logger.Printf("stopped")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func writeIfChanged(path string, b []byte) {
	if old, err := os.ReadFile(path); err == nil && sum(old) == sum(b) {
		return
	}
	_ = os.WriteFile(path, b, 0o600)
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func fail(title, text string, logger *log.Logger) {
	logger.Printf("error: %s: %s", title, text)
	showError(title, text)
	os.Exit(1)
}

// openWindow starts the browser window. It returns the started process when
// it belongs to this NOCTURNE (it may hand the window over and exit at once).
func openWindow(url string, logger *log.Logger) (*exec.Cmd, error) {
	profile := filepath.Join(core.SupportDir(), "Window")
	_ = os.MkdirAll(profile, 0o700)
	if bin := findBrowser(); bin != "" {
		args := []string{
			"--app=" + url,
			"--user-data-dir=" + profile,
			"--no-first-run", "--no-default-browser-check",
			"--disable-features=Translate,msEdgeSidebarV2,msHubApps,msUndersideButton,msShoppingTrigger,msEdgeCollections",
			"--lang=ru", "--window-size=1420,920",
		}
		cmd := exec.Command(bin, args...)
		if err := cmd.Start(); err != nil {
			logger.Printf("browser %s: %v", bin, err)
		} else {
			logger.Printf("window: %s", bin)
			return cmd, nil
		}
	}
	logger.Printf("no Edge/Chrome found, opening the default browser")
	return nil, openDefault(url)
}
