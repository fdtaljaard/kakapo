// kakapo is a portable SQL Server monitor: one exe, one DuckDB file beside it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"sqlmonitor/internal/api"
	"sqlmonitor/internal/cred"
	"sqlmonitor/internal/service"
	"sqlmonitor/internal/store"
	"sqlmonitor/web"
)

// version is stamped by build.ps1 (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}

	port := flag.Int("port", 7331, "port to listen on (127.0.0.1 only)")
	dataDir := flag.String("data", exeDir, "folder for kakapo.duckdb")
	noBrowser := flag.Bool("no-browser", false, "do not open a window; just serve")
	flag.Parse()

	if err := run(*port, *dataDir, !*noBrowser); err != nil {
		log.Fatal(err)
	}
}

func run(port int, dataDir string, openWindow bool) (err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Log to a file beside the data as well as the console, so a double-clicked exe that
	// stops can still be understood afterwards.
	if f, err := os.OpenFile(filepath.Join(dataDir, "kakapo.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	// Why it stopped, written while the log file is still open (main's log.Fatal comes after the
	// file is closed, so a start that failed left no reason in the log).
	defer func() {
		if err != nil {
			log.Printf("stopped: %v", err)
		}
	}()
	api.Version = version
	log.Printf("kakapo %s starting (pid %d)", version, os.Getpid())

	dbPath := dataFile(dataDir)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Printf("database %s", dbPath)

	installID, err := st.InstallID(ctx)
	if err != nil {
		return err
	}
	svc := service.New(st, cred.New(installID), version)
	presence := &api.Presence{}
	svc.SetBusy(presence) // a long fix running in the background keeps the app up (docs/phase4f-contract.md)
	srv := &http.Server{Handler: api.WithEvents(api.Handler(svc, web.Dist()), presence), ReadHeaderTimeout: 10 * time.Second}

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if alreadyRunning(url) {
			// A second double-click should bring up the app, not die quietly.
			log.Printf("kakapo is already running on %s; opening its window", url)
			if openWindow {
				openAppWindow(url) // the running instance owns the lifetime; leave the profile to it
			}
			return nil
		}
		return fmt.Errorf("port %d is in use by another program: %w (start with -port <other>)", port, err)
	}
	log.Printf("listening on %s", url)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	// The app lives as long as a window is connected to /api/events. When the last one has
	// been gone for the grace period, stop. Without a window (-no-browser) it runs until Ctrl+C.
	windowGone := make(chan struct{})
	if openWindow {
		cleanup := openAppWindow(url)
		defer cleanup()
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for range t.C {
				if presence.Gone(10 * time.Second) {
					close(windowGone)
					return
				}
			}
		}()
	} else {
		fmt.Println("Open", url, "in a browser. Press Ctrl+C to stop.")
	}

	select {
	case <-ctx.Done():
		log.Print("stopping")
	case <-windowGone:
		log.Print("window closed, stopping")
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// dataFile is kakapo.duckdb in dir. The app was called SQLMonitor until 2026-11-11: an old
// sqlmonitor.duckdb (and its .wal) beside the exe is renamed once, so the servers, history and
// fixes carry over. When the rename fails (an old exe still has it open), the old file is used.
func dataFile(dir string) string {
	newPath, oldPath := filepath.Join(dir, "kakapo.duckdb"), filepath.Join(dir, "sqlmonitor.duckdb")
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	if _, err := os.Stat(oldPath); err != nil {
		return newPath
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		log.Printf("could not rename %s to kakapo.duckdb (%v): using it as it is", oldPath, err)
		return oldPath
	}
	if _, err := os.Stat(oldPath + ".wal"); err == nil {
		if err := os.Rename(oldPath+".wal", newPath+".wal"); err != nil {
			// The data file and its write-ahead log must stay together: put the data file back.
			log.Printf("could not rename %s.wal (%v): using sqlmonitor.duckdb as it is", oldPath, err)
			if err := os.Rename(newPath, oldPath); err != nil {
				log.Printf("could not put back %s: %v", oldPath, err)
				return newPath
			}
			return oldPath
		}
	}
	log.Printf("renamed sqlmonitor.duckdb to kakapo.duckdb")
	return newPath
}

// alreadyRunning says whether the thing on our port is another kakapo.
func alreadyRunning(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// openAppWindow opens the UI as an Edge app window (no address bar) with the app's own
// profile folder, or in the default browser when Edge is not there. It does not wait on
// the browser's process: Edge hands windows between processes, so that is no measure of
// whether the window is open. The page's /api/events connection is. The returned cleanup
// is a no-op today and stays for callers.
func openAppWindow(url string) (cleanup func()) {
	cleanup = func() {}
	if runtime.GOOS == "windows" {
		for _, edge := range edgeCandidates(edgeAppPaths(), os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")) {
			if _, err := os.Stat(edge); err != nil {
				continue
			}
			// One profile folder, kept between runs: creating a fresh Edge profile costs
			// seconds every start, and the app no longer needs Edge's process to be its own.
			base, err := os.UserCacheDir()
			if err != nil {
				base = os.TempDir()
			}
			profile := filepath.Join(base, "kakapo", "edge-profile")
			// The folder was SQLMonitor before the rename: move it once, keeping the window's size.
			if _, err := os.Stat(profile); err != nil {
				if _, err := os.Stat(filepath.Join(base, "SQLMonitor", "edge-profile")); err == nil {
					_ = os.MkdirAll(filepath.Join(base, "kakapo"), 0o755)
					_ = os.Rename(filepath.Join(base, "SQLMonitor", "edge-profile"), profile)
				}
			}
			if err := os.MkdirAll(profile, 0o755); err != nil {
				log.Printf("edge profile folder: %v", err)
				break
			}
			cmd := exec.Command(edge, "--app="+url, "--user-data-dir="+profile, "--no-first-run",
				"--no-default-browser-check", "--window-size=1200,800")
			if err := cmd.Start(); err != nil {
				log.Printf("edge: %v", err)
				break
			}
			go func() { _ = cmd.Wait() }()
			return cleanup
		}
		// The last resort: the default browser, which on a server can be Internet Explorer.
		if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err == nil {
			fmt.Println("Opened", url, "in your browser.")
			return cleanup
		}
	}
	fmt.Println("Open", url, "in a browser.")
	return cleanup
}

// edgeCandidates are the places msedge.exe may be, in the order they are tried: where the App
// Paths registry key says it is (machine, then user), the per-user install under
// %LOCALAPPDATA%, then the two Program Files folders. Quotes around a registry value are
// removed, blanks and repeats left out; the caller tries each until one exists.
func edgeCandidates(appPaths []string, localAppData, programFilesX86, programFiles string) []string {
	const rel = `Microsoft\Edge\Application\msedge.exe`
	out, seen := []string{}, map[string]bool{}
	add := func(p string) {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	for _, p := range appPaths {
		add(p)
	}
	for _, base := range []string{localAppData, programFilesX86, programFiles} {
		if strings.TrimSpace(base) != "" {
			add(filepath.Join(base, rel))
		}
	}
	return out
}
