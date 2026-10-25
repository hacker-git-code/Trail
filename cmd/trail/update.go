package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// `trail update` replaces this binary with the latest release from GitHub.
// Your ledger lives in ~/.trail and is never touched.

const releasesAPI = "https://api.github.com/repos/hacker-git-code/trail/releases/latest"

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetName is the release file built for this computer.
func assetName() string {
	osName := map[string]string{"darwin": "macos"}[runtime.GOOS]
	if osName == "" {
		osName = runtime.GOOS
	}
	name := "trail-" + osName + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// newer reports whether version a (like "v0.2.1") is later than b.
func newer(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	api := releasesAPI
	if v := os.Getenv("TRAIL_RELEASES_API"); v != "" { // for testing against another repo
		api = v
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("GET", api, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "trail/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach GitHub to check for updates (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("couldn't check for updates: GitHub answered %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return fmt.Errorf("couldn't read the release info: %v", err)
	}
	if !newer(rel.Tag, version) {
		fmt.Printf("%s You're on the latest version (%s).\n", green("✓"), version)
		return nil
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	if *check {
		fmt.Printf("trail %s is out (you have %s). Run %s to get it.\n", latest, version, bold("trail update"))
		return nil
	}
	want := assetName()
	var url string
	var size int64
	for _, a := range rel.Assets {
		if a.Name == want {
			url, size = a.URL, a.Size
		}
	}
	if url == "" {
		return fmt.Errorf("trail %s has no build for this computer (%s) yet", latest, want)
	}

	exe, err := executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".trail-update-"+strconv.Itoa(os.Getpid()))
	if runtime.GOOS == "windows" {
		tmp += ".exe"
	}
	defer os.Remove(tmp)

	fmt.Printf("Downloading trail %s…\n", latest)
	if err := download(client, url, tmp, size); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("no permission to write to %s — run this from an administrator terminal, or move trail to a folder you own", dir)
		}
		return err
	}
	// Run the new binary before trusting it.
	out, err := exec.Command(tmp, "version").Output()
	if err != nil || !strings.Contains(string(out), latest) {
		return fmt.Errorf("the downloaded file didn't run correctly, so nothing was changed")
	}
	// A running watcher keeps the old code until it restarts, so restart it.
	var restart bool
	if s, err := openStore(); err == nil && watcherAlive(s) {
		restart = true
		os.WriteFile(stopFile(s), []byte("stop"), 0o600)
		for i := 0; i < 40 && watcherAlive(s); i++ {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if err := swapBinary(exe, tmp); err != nil {
		return err
	}
	fmt.Printf("%s Updated trail %s → %s. Your history is untouched.\n", green("✓"), version, latest)
	if restart {
		if s, err := openStore(); err == nil {
			p, _ := autostartPath()
			if _, err := os.Stat(p); err == nil {
				startBackground(exe, p, s.File("watch.log"))
				fmt.Println(dim("  Restarted the background watcher on the new version."))
			} else {
				fmt.Println(dim("  The watcher was stopped for the update — start it again with `trail watch`."))
			}
		}
	}
	fmt.Println(dim("  What's new: https://github.com/hacker-git-code/trail/releases/latest"))
	return nil
}

func download(client *http.Client, url, dest string, size int64) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "trail/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download failed: %v", err)
	}
	if size > 0 && n != size {
		return fmt.Errorf("download was cut short (%d of %d bytes), so nothing was changed", n, size)
	}
	return nil
}

// swapBinary puts the new binary where the running one is. Windows won't let
// a running .exe be overwritten, but it can be renamed out of the way.
func swapBinary(exe, fresh string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(fresh, exe)
	}
	old := strings.TrimSuffix(exe, ".exe") + ".old.exe"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("couldn't replace %s (%v) — close any running `trail watch` and try again", exe, err)
	}
	if err := os.Rename(fresh, exe); err != nil {
		os.Rename(old, exe) // put the old one back
		return err
	}
	return nil
}

// cleanupOldBinary removes the copy a Windows update leaves behind.
func cleanupOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		os.Remove(strings.TrimSuffix(exe, ".exe") + ".old.exe")
	}
}
