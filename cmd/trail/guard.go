package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hacker-git-code/trail/ledger"
)

// The guard stands between an AI agent and your secrets. Before a tool runs,
// it checks whether the call would send a credential off your computer —
// reading a .env file, printing environment variables, uploading a file,
// passing an API key to a web request or an MCP server — and blocks it or
// asks you first. Every decision is recorded on the timeline.

// provider is where Claude Code sends what it reads.
func provider() string {
	switch {
	case os.Getenv("CLAUDE_CODE_USE_BEDROCK") != "":
		return "AWS Bedrock (Claude)"
	case os.Getenv("CLAUDE_CODE_USE_VERTEX") != "":
		return "Google Vertex AI (Claude)"
	}
	return "Anthropic"
}

// strongSecrets keeps the findings that are certainly credentials (a key with
// a known shape), dropping the "looks like a password" guesses.
func strongSecrets(ss []ledger.Secret) []ledger.Secret {
	var out []ledger.Secret
	for _, s := range ss {
		if s.Kind != "Password or secret" {
			out = append(out, s)
		}
	}
	return out
}

// an picks "a" or "an" for a key name: "an Anthropic API key", "a Stripe key".
func an(kind string) string {
	if kind != "" && strings.ContainsRune("AEIOUaeiou", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
}

// kindsWithArticle reads "a Stripe key" for one secret, "Stripe key, JWT" for several.
func kindsWithArticle(ss []ledger.Secret) string {
	if len(ss) == 1 {
		return an(ss[0].Kind)
	}
	return ledger.SecretKinds(ss)
}

func describe(ss []ledger.Secret) string {
	if len(ss) == 1 {
		return an(ss[0].Kind) + " (" + ss[0].Masked + ")"
	}
	return fmt.Sprintf("%d secrets (%s)", len(ss), ledger.SecretKinds(ss))
}

func guardAllowed(s *ledger.Store, p string) bool {
	for _, a := range s.Config.GuardAllow {
		if a == p || within(p, a) {
			return true
		}
		if ok, _ := filepath.Match(a, filepath.Base(p)); ok {
			return true
		}
	}
	return false
}

func scanFile(p string) []ledger.Secret {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() || fi.Size() > 8<<20 {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return ledger.ScanSecrets(b)
}

var (
	shellWord   = regexp.MustCompile(`[^\s;|&<>()'"` + "`" + `]+`)
	uploadShell = []struct {
		re  *regexp.Regexp
		why string
	}{
		{regexp.MustCompile(`(?i)\bcurl\b.*(\s-d\b|\s--data|\s-F\b|\s--form|\s-T\b|\s--upload-file|\s-X\s*(POST|PUT|PATCH))`), "sends data to a website"},
		{regexp.MustCompile(`(?i)\bwget\b.*--(post|body)-(data|file)`), "sends data to a website"},
		{regexp.MustCompile(`(?i)\b(Invoke-WebRequest|Invoke-RestMethod|iwr|irm)\b.*-(Method\s+(Post|Put|Patch)|Body|InFile)`), "sends data to a website"},
		{regexp.MustCompile(`(?i)(^|[\s;|&])(scp|sftp|ftp)\s`), "copies files to another computer"},
		{regexp.MustCompile(`(?i)\brsync\b.*\S+@\S+:`), "copies files to another computer"},
		{regexp.MustCompile(`(?i)(^|[\s;|&])(nc|ncat|netcat)\s+\S+\s+\d+`), "opens a raw connection to another computer"},
		{regexp.MustCompile(`(?i)(^|[\s;|&])(printenv|env)\s*($|[;|&])|Get-ChildItem\s+env:|\bgci\s+env:|(^|[\s;|&])set\s*($|[;|&])|\$env:[A-Za-z_]*(KEY|TOKEN|SECRET|PASSWORD)`), "prints your environment variables, which often hold API keys"},
	}
)

// shellRisk says why a command could leak something, or "" if it looks fine.
func shellRisk(s *ledger.Store, cmd, cwd string) string {
	for _, loc := range shellWord.FindAllStringIndex(cmd, -1) {
		w := cmd[loc[0]:loc[1]]
		before := strings.TrimRight(cmd[:loc[0]], " \t")
		if strings.HasSuffix(before, ">") {
			continue // writing to the file, not reading it
		}
		p := expandHome(w)
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if why, ok := ledger.SensitivePath(p); ok && !guardAllowed(s, filepath.Clean(p)) {
			return "touches " + w + " (" + why + ")"
		}
	}
	for _, u := range uploadShell {
		if u.re.MatchString(cmd) {
			return u.why
		}
	}
	return ""
}

// guardCheck decides whether a tool call may run: "" (yes), "ask", or "deny".
func guardCheck(s *ledger.Store, ev claudeEvent, input map[string]any) (decision, reason, summary string, secrets []ledger.Secret) {
	str := func(k string) string { v, _ := input[k].(string); return v }
	abs := func(p string) string {
		if p == "" {
			return ""
		}
		p = expandHome(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(ev.Cwd, p)
		}
		return filepath.Clean(p)
	}
	to := provider()
	allow := func(p string) string { return " To allow it: trail guard allow " + p }
	switch tool := ev.ToolName; {
	case tool == "Read" || tool == "Grep":
		field := "file_path"
		if tool == "Grep" {
			field = "path"
		}
		p := abs(str(field))
		if p == "" || guardAllowed(s, p) {
			return
		}
		name := filepath.Base(p)
		if why, ok := ledger.SensitivePath(p); ok {
			return "deny",
				fmt.Sprintf("Trail blocked this: %s is a %s. Anything an AI reads is sent to %s.%s", name, why, to, allow(name)),
				"blocked reading " + name + " (" + why + ")", nil
		}
		if tool == "Read" {
			if found := scanFile(p); len(found) > 0 {
				return "ask",
					fmt.Sprintf("Trail: %s contains %s. If Claude reads it, it's sent to %s. Only allow this if you're sure.", name, describe(found), to),
					"asked before reading " + name + ": it holds " + kindsWithArticle(found), found
			}
		}
	case claudeShellTools[tool]:
		cmd := str("command")
		if why := shellRisk(s, cmd, ev.Cwd); why != "" {
			return "ask",
				fmt.Sprintf("Trail: this command %s. Whatever it prints is sent to %s. Allow only if you meant to.", why, to),
				"asked before running `" + oneLine(cmd, 60) + "`: it " + why, nil
		}
	case tool == "WebFetch" || tool == "WebSearch":
		found := strongSecrets(ledger.ScanSecrets(ev.ToolInput))
		if len(found) > 0 {
			return "deny",
				fmt.Sprintf("Trail blocked this request: it would send %s to the internet.", describe(found)),
				"blocked a web request carrying " + kindsWithArticle(found), found
		}
	case strings.HasPrefix(tool, "mcp__"):
		server := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2)[0]
		found := ledger.ScanSecrets(ev.ToolInput)
		if strong := strongSecrets(found); len(strong) > 0 {
			return "deny",
				fmt.Sprintf("Trail blocked this: it would send %s to %s.", describe(strong), server),
				"blocked sending " + kindsWithArticle(strong) + " to " + server, strong
		}
		if len(found) > 0 {
			return "ask",
				fmt.Sprintf("Trail: this sends something that looks like a password to %s. Allow only if you meant to.", server),
				"asked before sending a possible password to " + server, found
		}
	}
	return
}

func recordGuard(s *ledger.Store, actor ledger.Actor, tool, summary string, secrets []ledger.Secret, why string) {
	if _, err := s.Append(ledger.Body{
		Actor:   actor,
		Action:  ledger.Action{Type: "guard", Tool: tool, Summary: summary},
		Why:     why,
		Secrets: secrets,
	}); err != nil {
		logError(err)
	}
}

// ---- what an agent sends to its model, batched per turn --------------------

func sentFile(s *ledger.Store, session string) string {
	h := sha256.Sum256([]byte(session))
	return s.File(filepath.Join("sessions", hex.EncodeToString(h[:12])+".sent.jsonl"))
}

// noteSent remembers what a tool handed back to the model. One line per call,
// appended in a single write so parallel tool calls don't lose each other.
func noteSent(s *ledger.Store, ev claudeEvent, input map[string]any) {
	if ev.SessionID == "" || len(ev.Response) < 3 {
		return
	}
	str := func(k string) string { v, _ := input[k].(string); return v }
	var source string
	switch {
	case ev.ToolName == "Read":
		p := str("file_path")
		if p != "" && !filepath.IsAbs(p) {
			p = filepath.Join(ev.Cwd, p)
		}
		source = p
	case ev.ToolName == "Grep":
		source = "search results for “" + oneLine(str("pattern"), 40) + "”"
	case claudeShellTools[ev.ToolName]:
		source = "output of `" + oneLine(str("command"), 70) + "`"
	default:
		return // edits the model wrote itself, web pages, MCP replies: not your data
	}
	item := ledger.Sent{Source: source, Bytes: int64(len(ev.Response)), Hash: ledger.HashBytes(ev.Response)[:16], Secrets: ledger.ScanSecrets(ev.Response)}
	b, _ := json.Marshal(item)
	f, err := os.OpenFile(sentFile(s, ev.SessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		logError(err)
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// flushSent writes one timeline entry for everything sent in the turn.
func flushSent(s *ledger.Store, session string, actor ledger.Actor) error {
	if session == "" {
		return nil
	}
	p := sentFile(s, session)
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []ledger.Sent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var it ledger.Sent
		if json.Unmarshal(sc.Bytes(), &it) == nil {
			items = append(items, it)
		}
	}
	f.Close()
	os.Remove(p)
	if len(items) == 0 {
		return nil
	}
	var total int64
	files, outputs := 0, 0
	seen := map[string]bool{}
	var secrets []ledger.Secret
	for _, it := range items {
		total += it.Bytes
		if filepath.IsAbs(it.Source) {
			files++
		} else {
			outputs++
		}
		for _, sec := range it.Secrets {
			if !seen[sec.Hash] {
				seen[sec.Hash] = true
				secrets = append(secrets, sec)
			}
		}
	}
	var parts []string
	if files > 0 {
		parts = append(parts, plural(files, "file"))
	}
	if outputs > 0 {
		parts = append(parts, plural(outputs, "output"))
	}
	body := ledger.Body{
		Actor:   actor,
		Action:  ledger.Action{Type: "sent", Summary: fmt.Sprintf("sent %s to %s (%s)", strings.Join(parts, " + "), provider(), humanBytes(total))},
		Why:     oneLine(s.SessionPrompt(session), 400),
		Dest:    provider(),
		Sent:    items,
		Secrets: secrets,
	}
	if len(secrets) > 0 {
		body.Recovery = "These keys left your computer. Run `trail leaks` to see how to cancel them."
	}
	_, err = s.Append(body)
	return err
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// ---- trail guard ----------------------------------------------------------

func cmdGuard(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "on", "off":
		v := sub == "on"
		s.Config.Guard = &v
		if err := s.SaveConfig(); err != nil {
			return err
		}
		if v {
			fmt.Println(green("✓") + " Guard is on. AI agents can't read your secrets or send them out without asking.")
		} else {
			fmt.Println(yellow("!") + " Guard is off. Trail still records what agents send, but won't stop it.")
		}
		return nil
	case "allow", "disallow":
		if len(args) == 0 {
			return fmt.Errorf("usage: trail guard %s <file, folder or pattern like *.pem>", sub)
		}
		for _, a := range args {
			v := a
			if strings.ContainsAny(a, `/\`) || a == "." || a == "~" {
				v, _ = filepath.Abs(expandHome(a))
			} else if _, err := os.Stat(a); err == nil {
				v, _ = filepath.Abs(a)
			}
			var kept []string
			for _, x := range s.Config.GuardAllow {
				if x != v {
					kept = append(kept, x)
				}
			}
			if sub == "allow" {
				kept = append(kept, v)
				fmt.Printf("%s AI agents may now read %s.\n", green("✓"), pretty(v))
			} else {
				fmt.Printf("%s %s is guarded again.\n", green("✓"), pretty(v))
			}
			s.Config.GuardAllow = append([]string{}, kept...)
		}
		return s.SaveConfig()
	case "status", "list":
	default:
		return errors.New("usage: trail guard [on | off | allow <path> | disallow <path>]")
	}
	state := green("on")
	if !s.Config.GuardOn() {
		state = yellow("off") + dim(" — turn it on with `trail guard on`")
	}
	fmt.Printf("  %s  %s\n", dim("guard   "), state)
	if len(s.Config.GuardAllow) > 0 {
		names := make([]string, len(s.Config.GuardAllow))
		for i, a := range s.Config.GuardAllow {
			names[i] = pretty(a)
		}
		fmt.Printf("  %s  %s\n", dim("allowed "), strings.Join(names, ", "))
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	var recent []*ledger.Entry
	for i := len(entries) - 1; i >= 0 && len(recent) < 10; i-- {
		if entries[i].Body.Action.Type == "guard" {
			recent = append(recent, entries[i])
		}
	}
	fmt.Println()
	if len(recent) == 0 {
		fmt.Println(dim("  Nothing blocked yet."))
		return nil
	}
	fmt.Println(bold("  Recently stopped"))
	for _, e := range recent {
		fmt.Printf("  %s  %s  %s  %s\n", dim(e.When().Local().Format("2 Jan 15:04")), red("⊘"), cyan(fmt.Sprintf("%-12s", oneLine(e.Body.Actor.Name, 12))), oneLine(e.Body.Action.Summary, 80))
	}
	return nil
}

// ---- trail sent -------------------------------------------------------------

func cmdSent(args []string) error {
	fs := flag.NewFlagSet("sent", flag.ContinueOnError)
	n := fs.Int("n", 20, "")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	var shown []*ledger.Entry
	var files, outputs, secrets int
	var bytes int64
	dests := map[string]int64{}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		t := e.Body.Action.Type
		if t != "sent" && t != "external" {
			continue
		}
		for _, it := range e.Body.Sent {
			bytes += it.Bytes
			dests[e.Body.Dest] += it.Bytes
			if filepath.IsAbs(it.Source) {
				files++
			} else {
				outputs++
			}
		}
		secrets += len(e.Body.Secrets)
		if len(shown) < *n || *n <= 0 {
			shown = append(shown, e)
		}
	}
	if len(shown) == 0 {
		fmt.Println("Nothing recorded leaving your computer yet.")
		fmt.Println(dim("  Connect an agent (trail install claude) and everything it sends shows up here."))
		return nil
	}
	fmt.Println(bold("Sent off your computer"))
	var parts []string
	for d, b := range dests {
		if d != "" {
			parts = append(parts, d+" "+humanBytes(b))
		}
	}
	sort.Strings(parts)
	var what []string
	if files > 0 {
		what = append(what, plural(files, "file"))
	}
	if outputs > 0 {
		what = append(what, plural(outputs, "command output"))
	}
	if len(what) == 0 {
		what = append(what, "actions")
	}
	line := "  " + strings.Join(what, ", ")
	if len(parts) > 0 {
		line += " → " + strings.Join(parts, ", ")
	}
	fmt.Println(dim(line))
	if secrets > 0 {
		fmt.Println("  " + red(plural(secrets, "secret")+" went with it") + dim(" — see trail leaks"))
	}
	fmt.Println()
	for _, e := range shown {
		t := e.When().Local()
		summary := oneLine(e.Body.Action.Summary, 70)
		if len(e.Body.Secrets) > 0 && e.Body.Action.Type == "sent" {
			summary += red(fmt.Sprintf("  %s inside", plural(len(e.Body.Secrets), "secret")))
		}
		fmt.Printf("  %s  %s %s  %s  %s\n", dim(t.Format("2 Jan 15:04")), marker(e), dim(e.Short()), cyan(fmt.Sprintf("%-12s", oneLine(e.Body.Actor.Name, 12))), summary)
	}
	fmt.Println()
	fmt.Println(dim("  Exactly what went: trail show <id>"))
	return nil
}
