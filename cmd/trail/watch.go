package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hacker-git-code/trail/ledger"
)

// `trail watch` keeps a copy of every file in the folders you choose and
// records every change to them — from any app or AI, connected or not, inside
// git or not. Because the copy exists before anything changes, a deleted or
// overwritten file can always be brought back.

const watchUsage = `usage:
  trail watch add <folder>...    keep a copy of a folder and record every change to it
  trail watch rm <folder>        stop watching a folder (its history stays)
  trail watch list               what's watched, and whether the watcher is running
  trail watch                    run the watcher in this terminal
  trail watch autostart on|off   run it in the background whenever your computer starts
  trail watch stop               stop a background watcher`

func cmdWatch(args []string) error {
	sub := "run"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "run", "start":
		return watchRun(args)
	case "add":
		return watchAdd(args)
	case "rm", "remove":
		return watchRemove(args)
	case "list", "ls", "status":
		return watchList()
	case "autostart":
		return watchAutostart(args)
	case "stop":
		return watchStop()
	case "help", "-h", "--help":
		fmt.Println(watchUsage)
		return nil
	}
	return errors.New(watchUsage)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func watchAdd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trail watch add <folder>...   (for example: trail watch add ~/Documents ~/Desktop)")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	added := 0
	for _, a := range args {
		abs, err := filepath.Abs(expandHome(a))
		if err != nil {
			return err
		}
		fi, err := os.Stat(abs)
		if err != nil || !fi.IsDir() {
			return fmt.Errorf("%s is not a folder", a)
		}
		if abs == home || filepath.Dir(abs) == abs {
			return fmt.Errorf("%s is too broad to watch — pick the folders inside it that matter, like Documents, Desktop and your projects", pretty(abs))
		}
		if within(abs, s.Dir) || within(s.Dir, abs) {
			return fmt.Errorf("%s holds Trail's own ledger, so it can't be watched", pretty(abs))
		}
		skip := false
		var kept []string
		for _, w := range s.Config.Watch {
			switch {
			case within(abs, w):
				fmt.Printf("%s %s is already covered by %s\n", dim("·"), pretty(abs), pretty(w))
				skip = true
			case within(w, abs):
				// the new folder contains an old one: the old entry is no longer needed
			default:
				kept = append(kept, w)
			}
		}
		if skip {
			continue
		}
		files, size := s.Measure(abs, s.Config.WatchMaxFiles)
		if files > s.Config.WatchMaxFiles {
			return fmt.Errorf("%s has more than %d files — watch smaller folders inside it, or list what to skip in a .trailignore there", pretty(abs), s.Config.WatchMaxFiles)
		}
		s.Config.Watch = append(kept, abs)
		added++
		fmt.Printf("%s Watching %s %s\n", green("✓"), bold(pretty(abs)),
			dim(fmt.Sprintf("— %d files, %s to keep a first copy of (stored once, compressed)", files, humanBytes(size))))
	}
	if err := s.SaveConfig(); err != nil {
		return err
	}
	if added > 0 {
		fmt.Println()
		if watcherAlive(s) {
			fmt.Println("  The running watcher picks this up within a minute.")
		} else {
			fmt.Println("  Start it now:              " + bold("trail watch"))
			fmt.Println("  Start it with your computer: " + bold("trail watch autostart on"))
		}
	}
	return nil
}

func watchRemove(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trail watch rm <folder>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	for _, a := range args {
		abs, _ := filepath.Abs(expandHome(a))
		var kept []string
		found := false
		for _, w := range s.Config.Watch {
			if w == abs {
				found = true
				continue
			}
			kept = append(kept, w)
		}
		if !found {
			return fmt.Errorf("%s isn't watched (see `trail watch list`)", pretty(abs))
		}
		s.Config.Watch = append([]string{}, kept...)
		fmt.Printf("%s Stopped watching %s. Its history stays on your timeline.\n", green("✓"), pretty(abs))
	}
	return s.SaveConfig()
}

func watchList() error {
	s, err := openStore()
	if err != nil {
		return err
	}
	if len(s.Config.Watch) == 0 {
		fmt.Println("No folders watched yet. Try: " + bold("trail watch add ~/Documents"))
		return nil
	}
	for _, w := range s.Config.Watch {
		fmt.Println("  " + pretty(w))
	}
	fmt.Println()
	state := yellow("not running") + dim(" — start it with `trail watch`")
	if watcherAlive(s) {
		state = green("running")
	}
	fmt.Printf("  %s  %s\n", dim("watcher  "), state)
	auto := "off"
	if p, _ := autostartPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			auto = "on"
		}
	}
	fmt.Printf("  %s  %s\n", dim("autostart"), auto)
	return nil
}

// ---- the watcher -----------------------------------------------------------

func heartbeat(s *ledger.Store) string { return s.File("watch.alive") }
func stopFile(s *ledger.Store) string  { return s.File("watch.stop") }

func watcherAlive(s *ledger.Store) bool {
	fi, err := os.Stat(heartbeat(s))
	return err == nil && time.Since(fi.ModTime()) < 45*time.Second
}

type watched struct {
	root   string
	m      ledger.Manifest
	broken string // why this folder can't be watched right now, if it can't
}

func watchRun(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return errors.New("add folders with `trail watch add <folder>`, then run `trail watch`")
	}
	if len(s.Config.Watch) == 0 {
		fmt.Println("No folders to watch yet. Add some first:")
		fmt.Println()
		fmt.Println("  " + bold("trail watch add ~/Documents ~/Desktop"))
		return nil
	}
	if watcherAlive(s) {
		fmt.Println("A watcher is already running. See `trail watch list`, or stop it with `trail watch stop`.")
		return errQuiet
	}
	os.Remove(stopFile(s))
	touch(heartbeat(s))
	defer os.Remove(heartbeat(s))

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	say := func(format string, a ...any) {
		fmt.Printf("%s  %s\n", dim(time.Now().Format("15:04")), fmt.Sprintf(format, a...))
	}

	byRoot := map[string]*watched{}
	sync := func() {
		for _, root := range s.Config.Watch {
			if _, ok := byRoot[root]; ok {
				continue
			}
			w := &watched{root: root}
			m, err := s.SnapshotMax(root, s.Config.WatchMaxFiles)
			if err != nil {
				w.broken = err.Error()
				say("%s %s: %v", yellow("!"), pretty(root), err)
			} else {
				w.m = m
				say("watching %s %s", bold(pretty(root)), dim(fmt.Sprintf("· %d files saved", len(m))))
			}
			byRoot[root] = w
		}
		for root := range byRoot {
			if !contains(s.Config.Watch, root) {
				delete(byRoot, root)
			}
		}
	}
	sync()
	fmt.Println(dim("   every change is recorded · Ctrl-C to stop"))

	// Changes another tool already recorded (a Claude Code hook, an undo) are
	// not recorded twice. Start from the end of the ledger.
	var off int64
	if fi, err := os.Stat(s.File("ledger.jsonl")); err == nil {
		off = fi.Size()
	}
	recorded := map[string]time.Time{}
	refreshRecorded := func() {
		entries, next, err := s.EntriesFrom(off)
		if err != nil {
			return
		}
		off = next
		now := time.Now()
		for _, e := range entries {
			for _, c := range e.Body.Changes {
				recorded[c.Path+"\x00"+c.After] = now
			}
		}
		for k, t := range recorded {
			if now.Sub(t) > 10*time.Minute {
				delete(recorded, k)
			}
		}
	}

	lastConfig := time.Now()
	for {
		touch(heartbeat(s))
		if _, err := os.Stat(stopFile(s)); err == nil {
			os.Remove(stopFile(s))
			say("stopped")
			return nil
		}
		if time.Since(lastConfig) > 30*time.Second {
			if fresh, err := openStore(); err == nil {
				s.Config.Watch = fresh.Config.Watch
				sync()
			}
			lastConfig = time.Now()
		}
		start := time.Now()
		for _, root := range s.Config.Watch {
			w := byRoot[root]
			if w == nil {
				continue
			}
			cur, err := s.SnapshotMax(root, s.Config.WatchMaxFiles)
			if err != nil {
				if w.broken == "" {
					say("%s %s: %v", yellow("!"), pretty(root), err)
				}
				w.broken = err.Error()
				continue
			}
			if w.broken != "" || w.m == nil {
				if w.broken != "" {
					say("watching %s again", pretty(root))
				}
				w.broken, w.m = "", cur
				continue
			}
			if ledger.SameManifest(w.m, cur) {
				continue
			}
			// Let a burst of writes settle, so one save or one agent step is one entry.
			for i := 0; i < 6; i++ {
				time.Sleep(1500 * time.Millisecond)
				next, err := s.SnapshotMax(root, s.Config.WatchMaxFiles)
				if err != nil {
					break
				}
				same := ledger.SameManifest(next, cur)
				cur = next
				if same {
					break
				}
			}
			changes := s.DiffManifests(root, w.m, cur)
			w.m = cur
			refreshRecorded()
			var fresh []ledger.Change
			for _, c := range changes {
				if _, dup := recorded[c.Path+"\x00"+c.After]; !dup {
					fresh = append(fresh, c)
				}
			}
			if len(fresh) == 0 {
				continue
			}
			e, err := s.Append(ledger.Body{
				Actor:   ledger.Actor{Kind: "unknown", Name: "unknown app"},
				Action:  ledger.Action{Type: "observed", Summary: watchSummary(fresh, root)},
				Root:    root,
				Changes: fresh,
			})
			if err != nil {
				say("%s could not record: %v", red("!"), err)
				continue
			}
			for _, c := range fresh {
				recorded[c.Path+"\x00"+c.After] = time.Now()
			}
			say("%s %s  %s  %s", marker(e), dim(e.Short()), oneLine(e.Body.Action.Summary, 60), stats(fresh))
		}
		// Rescanning is cheap (unchanged files are only stat'ed), but stay
		// polite on large folders: wait at least 4× as long as a scan took.
		wait := max(3*time.Second, 4*time.Since(start))
		select {
		case <-quit:
			fmt.Println()
			say("stopped")
			return nil
		case <-time.After(wait):
		}
	}
}

func watchSummary(changes []ledger.Change, root string) string {
	if len(changes) == 1 {
		return fileSummary(changes, "")
	}
	op := changes[0].Op
	for _, c := range changes {
		if c.Op != op {
			op = ""
			break
		}
	}
	verb := map[string]string{"create": "created", "delete": "deleted", "modify": "edited"}[op]
	if verb == "" {
		return fmt.Sprintf("%d files changed in %s", len(changes), filepath.Base(root))
	}
	return fmt.Sprintf("%s %d files in %s", verb, len(changes), filepath.Base(root))
}

func touch(p string) {
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		os.WriteFile(p, []byte(fmt.Sprint(os.Getpid())), 0o600)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func watchStop() error {
	s, err := openStore()
	if err != nil {
		return err
	}
	if !watcherAlive(s) {
		fmt.Println("No watcher is running.")
		return nil
	}
	if err := os.WriteFile(stopFile(s), []byte("stop"), 0o600); err != nil {
		return err
	}
	for i := 0; i < 40 && watcherAlive(s); i++ {
		if _, err := os.Stat(stopFile(s)); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	fmt.Println(green("✓") + " Watcher stopped. (If autostart is on, it starts again next time you log in.)")
	return nil
}

// ---- autostart ------------------------------------------------------------

func autostartPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "trail-watch.vbs"), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", "com.trail.watch.plist"), nil
	}
	return filepath.Join(home, ".config", "autostart", "trail-watch.desktop"), nil
}

func watchAutostart(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return errors.New("usage: trail watch autostart on|off")
	}
	on := args[0] == "on"
	s, err := openStore()
	if err != nil {
		return err
	}
	p, err := autostartPath()
	if err != nil {
		return err
	}
	if !on {
		if runtime.GOOS == "darwin" {
			exec.Command("launchctl", "unload", "-w", p).Run()
		}
		os.Remove(p)
		if watcherAlive(s) {
			watchStop()
		}
		fmt.Println(green("✓") + " Autostart is off.")
		return nil
	}
	if len(s.Config.Watch) == 0 {
		return errors.New("add a folder first: trail watch add ~/Documents")
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	logPath := s.File("watch.log")
	var content string
	switch runtime.GOOS {
	case "windows":
		// wscript runs it with no window; quotes are doubled inside the string.
		content = "CreateObject(\"WScript.Shell\").Run \"\"\"" + exe + "\"\" watch run\", 0, False\r\n"
	case "darwin":
		content = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.trail.watch</string>
  <key>ProgramArguments</key><array><string>` + xmlEscape(exe) + `</string><string>watch</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>` + xmlEscape(logPath) + `</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(logPath) + `</string>
</dict></plist>
`
	default:
		content = "[Desktop Entry]\nType=Application\nName=Trail watch\nExec=\"" + exe + "\" watch run\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n"
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return err
	}
	if !watcherAlive(s) {
		startBackground(exe, p, logPath)
	}
	fmt.Println(green("✓") + " Autostart is on. The watcher runs in the background from now on, and starts with your computer.")
	fmt.Println(dim("  Check on it any time: trail watch list"))
	return nil
}

func startBackground(exe, autostartFile, logPath string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("wscript.exe", autostartFile).Start()
	case "darwin":
		exec.Command("launchctl", "load", "-w", autostartFile).Run()
	default:
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		var c *exec.Cmd
		if setsid, err := exec.LookPath("setsid"); err == nil {
			c = exec.Command(setsid, exe, "watch", "run")
		} else {
			c = exec.Command(exe, "watch", "run")
		}
		c.Stdout, c.Stderr = f, f
		if c.Start() == nil {
			c.Process.Release()
		}
	}
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
