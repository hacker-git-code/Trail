package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hacker-git-code/trail/ledger"
)

// `trail net` records which app on your computer talks to which site — any
// app, not only AI agents. Traffic is encrypted, so Trail can't (and doesn't
// try to) read what's inside; it records who connected where, flags AI
// services and destinations it hasn't seen before, and can block an app or a
// site. For AI agents connected through hooks, `trail sent` shows the content.

type conn struct {
	PID  int
	Proc string
	IP   string
	Port int
}

// ---- reading the connection table on each OS -------------------------------

func connections() ([]conn, error) {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("netstat", "-ano").Output()
		if err != nil {
			return nil, err
		}
		names := map[int]string{}
		if tl, err := exec.Command("tasklist", "/fo", "csv", "/nh").Output(); err == nil {
			names = parseTasklist(tl)
		}
		return parseNetstat(out, names), nil
	case "darwin":
		out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:ESTABLISHED", "-Fpcn").Output()
		if err != nil && len(out) == 0 {
			return nil, err
		}
		return parseLsof(out), nil
	default:
		return procConnections()
	}
}

// netstat -ano:  TCP    192.168.1.5:52311   142.250.183.4:443   ESTABLISHED   1234
func parseNetstat(out []byte, names map[int]string) []conn {
	var cs []conn
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || f[0] != "TCP" || f[3] != "ESTABLISHED" {
			continue
		}
		ip, port := splitHostPort(f[2])
		pid, _ := strconv.Atoi(f[4])
		cs = append(cs, conn{PID: pid, Proc: names[pid], IP: ip, Port: port})
	}
	return cs
}

// tasklist /fo csv /nh:  "chrome.exe","1234","Console","1","123,456 K"
func parseTasklist(out []byte) map[int]string {
	m := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), `","`)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.Trim(f[1], `"`))
		if err == nil {
			m[pid] = strings.Trim(f[0], `"`)
		}
	}
	return m
}

// lsof -Fpcn: p<pid> / c<command> / n<local>-><remote>
func parseLsof(out []byte) []conn {
	var cs []conn
	pid, name := 0, ""
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'c':
			name = line[1:]
		case 'n':
			if i := strings.Index(line, "->"); i >= 0 {
				ip, port := splitHostPort(line[i+2:])
				cs = append(cs, conn{PID: pid, Proc: name, IP: ip, Port: port})
			}
		}
	}
	return cs
}

func splitHostPort(s string) (string, int) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, 0
	}
	port, _ := strconv.Atoi(s[i+1:])
	return strings.Trim(s[:i], "[]"), port
}

// Linux: /proc/net/tcp{,6} lists sockets by inode; /proc/<pid>/fd links to them.
func procConnections() ([]conn, error) {
	byInode := map[string]conn{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, c := range parseProcNet(b) {
			byInode[c.Proc] = c // Proc holds the inode until matched
		}
	}
	if len(byInode) == 0 {
		return nil, nil
	}
	var cs []conn
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", p.Name(), "fd"))
		if err != nil {
			continue
		}
		var name string
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", p.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			c, ok := byInode[inode]
			if !ok {
				continue
			}
			if name == "" {
				b, _ := os.ReadFile(filepath.Join("/proc", p.Name(), "comm"))
				name = strings.TrimSpace(string(b))
			}
			c.PID, c.Proc = pid, name
			cs = append(cs, c)
			delete(byInode, inode)
		}
	}
	return cs, nil
}

func parseProcNet(b []byte) []conn {
	var cs []conn
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != "01" { // 01 = ESTABLISHED
			continue
		}
		ipHex, portHex, ok := strings.Cut(f[2], ":")
		if !ok {
			continue
		}
		raw, err := hex.DecodeString(ipHex)
		if err != nil {
			continue
		}
		for i := 0; i+4 <= len(raw); i += 4 { // each 32-bit word is little-endian
			raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
		}
		port, _ := strconv.ParseInt(portHex, 16, 32)
		cs = append(cs, conn{IP: net.IP(raw).String(), Port: int(port), Proc: f[9]})
	}
	return cs
}

// ---- turning addresses into names -----------------------------------------

func private(ip string) bool {
	p := net.ParseIP(ip)
	return p == nil || p.IsLoopback() || p.IsPrivate() || p.IsLinkLocalUnicast() || p.IsUnspecified()
}

// ipconfig /displaydns:  "Record Name . . . : api.anthropic.com" then "A (Host) Record . . : 160.79.104.10"
func parseDisplayDNS(out []byte) map[string]string {
	m := map[string]string{}
	name := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(strings.Trim(k, ". ")), strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(k, "Record Name"):
			name = v
		case strings.HasPrefix(k, "A (Host) Record"), strings.HasPrefix(k, "AAAA Record"):
			if name != "" && v != "" {
				m[v] = name
			}
		}
	}
	return m
}

type resolver struct {
	cache   map[string]string
	dnsAt   time.Time
	dnsview map[string]string
}

func (r *resolver) name(ip string) string {
	if r.cache == nil {
		r.cache = map[string]string{}
	}
	if runtime.GOOS == "windows" && time.Since(r.dnsAt) > 20*time.Second {
		if out, err := exec.Command("ipconfig", "/displaydns").Output(); err == nil {
			r.dnsview = parseDisplayDNS(out)
		}
		r.dnsAt = time.Now()
	}
	if n := r.dnsview[ip]; n != "" {
		return n
	}
	if n, ok := r.cache[ip]; ok {
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	n := ip
	if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil && len(names) > 0 {
		n = strings.TrimSuffix(names[0], ".")
	}
	r.cache[ip] = n
	return n
}

// site is the part of a host name a person recognises: api.openai.com → openai.com.
func site(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	l := strings.Split(strings.ToLower(host), ".")
	if len(l) <= 2 {
		return strings.ToLower(host)
	}
	if len(l[len(l)-1]) == 2 && contains([]string{"co", "com", "org", "net", "ac", "gov", "edu"}, l[len(l)-2]) {
		return strings.Join(l[len(l)-3:], ".")
	}
	return strings.Join(l[len(l)-2:], ".")
}

var aiSites = []string{"openai.com", "anthropic.com", "claude.ai", "gemini.google.com", "generativelanguage.googleapis.com",
	"mistral.ai", "cohere.com", "cohere.ai", "groq.com", "together.xyz", "perplexity.ai", "x.ai", "deepseek.com",
	"huggingface.co", "replicate.com", "openrouter.ai", "cursor.sh", "cursor.com", "codeium.com", "windsurf.com",
	"githubcopilot.com", "copilot.microsoft.com", "chatgpt.com"}

func isAI(host string) bool {
	h := strings.ToLower(host)
	for _, a := range aiSites {
		if h == a || strings.HasSuffix(h, "."+a) {
			return true
		}
	}
	return false
}

// ---- the monitor ----------------------------------------------------------

type netMonitor struct {
	seen map[string]bool // "app|site"
	apps map[string]bool
	res  resolver
}

func seenPath(s *ledger.Store) string { return s.File("net-seen.json") }

func loadMonitor(s *ledger.Store) *netMonitor {
	m := &netMonitor{seen: map[string]bool{}, apps: map[string]bool{}}
	if b, err := os.ReadFile(seenPath(s)); err == nil {
		var keys []string
		if json.Unmarshal(b, &keys) == nil {
			for _, k := range keys {
				m.seen[k] = true
				app, _, _ := strings.Cut(k, "|")
				m.apps[app] = true
			}
		}
	}
	return m
}

func (m *netMonitor) save(s *ledger.Store) {
	keys := make([]string, 0, len(m.seen))
	for k := range m.seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.Marshal(keys)
	os.WriteFile(seenPath(s), b, 0o600)
}

// poll records every app→site pair seen for the first time, as one entry.
func (m *netMonitor) poll(s *ledger.Store) (*ledger.Entry, error) {
	cs, err := connections()
	if err != nil {
		return nil, err
	}
	first := len(m.seen) == 0
	var items []ledger.Sent
	var flags []string
	newApps := 0
	for _, c := range cs {
		if private(c.IP) || c.Proc == "" {
			continue
		}
		host := m.res.name(c.IP)
		key := strings.ToLower(c.Proc) + "|" + site(host)
		if m.seen[key] {
			continue
		}
		m.seen[key] = true
		if !m.apps[strings.ToLower(c.Proc)] {
			m.apps[strings.ToLower(c.Proc)] = true
			newApps++
		}
		src := c.Proc + " → " + host
		if isAI(host) {
			src += "  [AI service]"
			flags = append(flags, c.Proc+" → "+site(host))
		}
		items = append(items, ledger.Sent{Source: src})
	}
	if len(items) == 0 {
		return nil, nil
	}
	m.save(s)
	summary := fmt.Sprintf("%s connected to %s", appsIn(items), plural(len(items), "new site"))
	if first {
		summary = fmt.Sprintf("first look: %s talking to %s", plural(countApps(items), "app"), plural(len(items), "site"))
	}
	body := ledger.Body{
		Actor:  ledger.Actor{Kind: "system", Name: "net"},
		Action: ledger.Action{Type: "network", Summary: summary},
		Sent:   items,
	}
	if len(flags) > 0 && !first {
		body.Note = "talking to an AI service: " + strings.Join(flags, ", ")
	}
	return s.Append(body)
}

func appsIn(items []ledger.Sent) string {
	var apps []string
	for _, it := range items {
		a, _, _ := strings.Cut(it.Source, " → ")
		if !contains(apps, a) {
			apps = append(apps, a)
		}
	}
	if len(apps) > 3 {
		return strings.Join(apps[:3], ", ") + fmt.Sprintf(" +%d", len(apps)-3)
	}
	return strings.Join(apps, ", ")
}

func countApps(items []ledger.Sent) int {
	seen := map[string]bool{}
	for _, it := range items {
		a, _, _ := strings.Cut(it.Source, " → ")
		seen[a] = true
	}
	return len(seen)
}

// ---- trail net -------------------------------------------------------------

func cmdNet(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "on", "off":
		s.Config.Net = sub == "on"
		if err := s.SaveConfig(); err != nil {
			return err
		}
		if s.Config.Net {
			fmt.Println(green("✓") + " Trail will record which apps talk to which sites.")
			if watcherAlive(s) {
				fmt.Println(dim("  The running watcher picks this up within a minute."))
			} else {
				fmt.Println("  It runs inside the watcher: " + bold("trail watch") + dim("  (or trail watch autostart on)"))
			}
		} else {
			fmt.Println(green("✓") + " Network recording is off.")
		}
		return nil
	case "now", "watch":
		return netLive(s)
	case "block", "unblock":
		if len(args) != 1 {
			return fmt.Errorf("usage: trail net %s <app.exe | site.com>", sub)
		}
		return netBlock(s, args[0], sub == "block")
	case "blocked":
		return netBlocked()
	case "show", "list":
		return netShow(s)
	}
	return errors.New("usage: trail net [on | off | now | block <app|site> | unblock <app|site> | blocked]")
}

// netLive prints connections as they appear, for a quick look (and demos).
func netLive(s *ledger.Store) error {
	m := loadMonitor(s)
	fmt.Println(dim("Watching which apps talk to which sites · Ctrl-C to stop"))
	for {
		e, err := m.poll(s)
		if err != nil {
			return fmt.Errorf("can't read connections: %v", err)
		}
		if e != nil {
			for _, it := range e.Body.Sent {
				line := it.Source
				if strings.Contains(line, "[AI service]") {
					line = strings.Replace(line, "  [AI service]", "", 1) + "  " + purple("AI service")
				}
				fmt.Printf("%s  %s %s\n", dim(time.Now().Format("15:04:05")), dim("⇢"), line)
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func netShow(s *ledger.Store) error {
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	type dest struct {
		host string
		ai   bool
		when time.Time
	}
	byApp := map[string][]dest{}
	var order []string
	for _, e := range entries {
		if e.Body.Action.Type != "network" {
			continue
		}
		for _, it := range e.Body.Sent {
			app, rest, ok := strings.Cut(it.Source, " → ")
			if !ok {
				continue
			}
			ai := strings.Contains(rest, "[AI service]")
			host := strings.TrimSpace(strings.Replace(rest, "[AI service]", "", 1))
			if byApp[app] == nil {
				order = append(order, app)
			}
			byApp[app] = append(byApp[app], dest{host, ai, e.When()})
		}
	}
	state := yellow("off") + dim(" — turn on: trail net on")
	if s.Config.Net {
		state = green("on")
		if !watcherAlive(s) {
			state += dim(" (the watcher isn't running: trail watch)")
		}
	}
	fmt.Printf("  %s  %s\n\n", dim("network recording"), state)
	if len(order) == 0 {
		fmt.Println("Nothing recorded yet. For a quick look right now: " + bold("trail net now"))
		return nil
	}
	sort.Slice(order, func(i, j int) bool { return len(byApp[order[i]]) > len(byApp[order[j]]) })
	for _, app := range order {
		ds := byApp[app]
		ai := 0
		for _, d := range ds {
			if d.ai {
				ai++
			}
		}
		head := fmt.Sprintf("%s  %s", bold(app), dim(plural(len(ds), "site")))
		if ai > 0 {
			head += "  " + purple(plural(ai, "AI service"))
		}
		fmt.Println(head)
		for i, d := range ds {
			if i == 6 {
				fmt.Println(dim(fmt.Sprintf("    … and %d more", len(ds)-6)))
				break
			}
			mark := dim("⇢")
			name := d.host
			if d.ai {
				mark, name = purple("⇢"), d.host+"  "+purple("AI service")
			}
			fmt.Printf("    %s %s  %s\n", mark, name, dim("first seen "+d.when.Local().Format("2 Jan 15:04")))
		}
	}
	fmt.Println()
	fmt.Println(dim("  Don't trust one? trail net block <app.exe>  or  trail net block <site.com>"))
	return nil
}

// ---- blocking --------------------------------------------------------------

const hostsMark = "# added by trail"

func hostsPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

func adminHint() string {
	if runtime.GOOS == "windows" {
		return "run this from a terminal opened with \"Run as administrator\""
	}
	return "run it with sudo"
}

func netBlock(s *ledger.Store, target string, block bool) error {
	isApp := strings.HasSuffix(strings.ToLower(target), ".exe") || !strings.Contains(target, ".")
	if isApp {
		return netBlockApp(s, target, block)
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://"), "/"))
	p := hostsPath()
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.Fields(l)
		if strings.Contains(l, hostsMark) && len(f) >= 2 && (f[1] == host || f[1] == "www."+host) {
			continue
		}
		lines = append(lines, l)
	}
	if block {
		lines = append(lines, "0.0.0.0 "+host+" "+hostsMark, "0.0.0.0 www."+host+" "+hostsMark)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("blocking a site needs admin rights — %s", adminHint())
		}
		return err
	}
	if runtime.GOOS == "windows" {
		exec.Command("ipconfig", "/flushdns").Run()
	}
	verb := "unblocked"
	if block {
		verb = "blocked"
	}
	recordGuard(s, ledger.Actor{Kind: "human", Name: username()}, "", verb+" "+host+" for every app on this computer", nil, "")
	fmt.Printf("%s %s %s for every app on this computer.\n", green("✓"), strings.ToUpper(verb[:1])+verb[1:], bold(host))
	if block {
		fmt.Println(dim("  Undo any time: trail net unblock " + host))
	}
	return nil
}

func netBlockApp(s *ledger.Store, app string, block bool) error {
	if runtime.GOOS != "windows" {
		return errors.New("blocking an app is Windows-only for now; you can block the site it talks to: trail net block <site.com>")
	}
	name := strings.TrimSuffix(filepath.Base(app), filepath.Ext(app))
	rule := "trail: block " + name
	if !block {
		out, err := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+rule).CombinedOutput()
		if err != nil {
			return fmt.Errorf("couldn't remove the block (%s) — %s", strings.TrimSpace(string(out)), adminHint())
		}
		recordGuard(s, ledger.Actor{Kind: "human", Name: username()}, "", "unblocked "+name+" from the internet", nil, "")
		fmt.Printf("%s %s can reach the internet again.\n", green("✓"), bold(name))
		return nil
	}
	path := app
	if !filepath.IsAbs(path) {
		out, err := exec.Command("powershell", "-NoProfile", "-Command",
			"(Get-Process -Name '"+strings.ReplaceAll(name, "'", "''")+"' -ErrorAction SilentlyContinue | Select-Object -First 1).Path").Output()
		path = strings.TrimSpace(string(out))
		if err != nil || path == "" {
			return fmt.Errorf("%s isn't running, so Trail can't find where it lives — pass the full path to its .exe", name)
		}
	}
	out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule", "name="+rule, "dir=out", "action=block", "program="+path, "enable=yes").CombinedOutput()
	if err != nil {
		return fmt.Errorf("couldn't add the block (%s) — %s", strings.TrimSpace(string(out)), adminHint())
	}
	recordGuard(s, ledger.Actor{Kind: "human", Name: username()}, "", "blocked "+name+" from the internet", nil, "")
	fmt.Printf("%s %s can no longer send anything to the internet.\n", green("✓"), bold(name))
	fmt.Println(dim("  Undo any time: trail net unblock " + name))
	return nil
}

func netBlocked() error {
	var sites []string
	if b, err := os.ReadFile(hostsPath()); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); strings.Contains(l, hostsMark) && len(f) >= 2 && !strings.HasPrefix(f[1], "www.") {
				sites = append(sites, f[1])
			}
		}
	}
	var apps []string
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=all").Output(); err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				if i := strings.Index(l, "trail: block "); i >= 0 {
					apps = append(apps, strings.TrimSpace(l[i+len("trail: block "):]))
				}
			}
		}
	}
	if len(sites)+len(apps) == 0 {
		fmt.Println("Nothing blocked.")
		return nil
	}
	for _, a := range apps {
		fmt.Printf("  %s %s  %s\n", red("⊘"), bold(a), dim("app · no internet"))
	}
	for _, h := range sites {
		fmt.Printf("  %s %s  %s\n", red("⊘"), bold(h), dim("site · blocked for every app"))
	}
	return nil
}
