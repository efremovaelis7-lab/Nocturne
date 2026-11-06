// nocturne-server is the library server started by the NOCTURNE window
// (Contents/MacOS/NOCTURNE). It stores everything exactly where the browser
// build did, so both builds read the same library.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"nocturne/core"
)

func writeStatus(path string, v map[string]any) {
	if path == "" {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
		return
	}
	b, _ := json.Marshal(v)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}

// loadTestEnv lets a test copy of the app use its own folders: when
// Resources/nocturne-test.json exists, its keys become environment variables
// (NOCTURNE_TEST_SUPPORT, NOCTURNE_TEST_DATA, NOCTURNE_TEST_DOWNLOADS).
// Release builds do not contain this file.
func loadTestEnv(resources string) {
	if resources == "" {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		resources = filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources")
	}
	b, err := os.ReadFile(filepath.Join(resources, "nocturne-test.json"))
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for k, v := range m {
		if len(k) > 14 && k[:14] == "NOCTURNE_TEST_" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	appMode := flag.Bool("app", false, "run for the desktop window")
	statusFile := flag.String("status-file", "", "where to report the port or an error")
	parentPid := flag.Int("parent-pid", 0, "exit when this process is gone")
	takeover := flag.Bool("takeover", false, "ask a running NOCTURNE server to quit first")
	resources := flag.String("resources", "", "folder with index.html and app.js")
	platformFlag := flag.String("platform", "", "override the platform name (tests)")
	flag.Parse()

	loadTestEnv(*resources)
	_ = os.MkdirAll(core.SupportDir(), 0o700)
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if lf, err := os.OpenFile(filepath.Join(core.SupportDir(), "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		if st, err := lf.Stat(); err == nil && st.Size() > 4<<20 {
			_ = lf.Truncate(0)
		}
		logger = log.New(lf, "", log.LstdFlags)
	}

	res := *resources
	if res == "" {
		if exe, err := os.Executable(); err == nil {
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				exe = real
			}
			res = filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources")
		}
	}

	platform := ""
	if *appMode {
		platform = core.PlatformName
		if *platformFlag != "" {
			platform = *platformFlag
		}
	}
	var srv *core.Server
	quit := make(chan struct{}, 1)
	opts := core.Options{
		Platform:  platform,
		HTMLPath:  filepath.Join(res, "index.html"),
		AppJSPath: filepath.Join(res, "app.js"),
		Ports:     []int{41731, 41732, 41733, 41734, 41735, 41736, 41737, 41738},
		Log:       logger,
		OnQuit:    func() { quit <- struct{}{} },
	}
	if icon, err := os.ReadFile(filepath.Join(res, "icon.png")); err == nil {
		opts.Favicon = icon
	}

	var err error
	if *takeover {
		if err := core.StopRunning(20 * time.Second); err != nil {
			logger.Printf("takeover: %v", err)
		}
	}
	// A previous window of this app may still be finishing its last save.
	for i := 0; i < 25; i++ {
		srv, err = core.New(opts)
		if !errors.Is(err, core.ErrLocked) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		logger.Printf("start: %v", err)
		st := map[string]any{"error": err.Error()}
		if errors.Is(err, core.ErrLocked) {
			st["code"] = "locked"
			if rs, alive := core.FindRunning(); rs != nil {
				st["alive"] = alive
				st["app"] = rs.App
				st["port"] = rs.Port
			}
		}
		writeStatus(*statusFile, st)
		os.Exit(3)
	}
	port, err := srv.Listen()
	if err != nil {
		logger.Printf("listen: %v", err)
		srv.Shutdown()
		writeStatus(*statusFile, map[string]any{"error": "Не удалось запустить сервер NOCTURNE: " + err.Error()})
		os.Exit(4)
	}
	logger.Printf("NOCTURNE %s: %s", core.Version, srv.URL())
	writeStatus(*statusFile, map[string]any{"ok": true, "port": port, "url": srv.URL(), "version": core.Version})

	done := make(chan struct{})
	go func() {
		if err := srv.Serve(); err != nil {
			logger.Printf("serve: %v", err)
		}
		close(done)
	}()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	var orphan <-chan time.Time
	if *parentPid > 0 {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		orphan = t.C
	}
	for {
		select {
		case s := <-sig:
			logger.Printf("stop: %v", s)
		case <-quit:
			logger.Printf("stop: quit requested by the page")
		case <-done:
			logger.Printf("stop: server closed")
		case <-orphan:
			if os.Getppid() == *parentPid {
				continue
			}
			logger.Printf("stop: window process is gone")
		}
		break
	}
	srv.Shutdown()
	logger.Printf("stopped")
}
