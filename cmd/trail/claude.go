package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hacker-git-code/trail/ledger"
)

// Claude Code hook integration. Claude Code runs `trail hook claude` before
// and after each tool call and passes the call as JSON on stdin. The hook
// never blocks Claude, never prints to stdout, and always exits 0: if
// recording fails, the failure goes to ~/.trail/errors.log and Claude carries on.

type claudeEvent struct {
	SessionID string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
	Event     string          `json:"hook_event_name"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
	Prompt    string          `json:"prompt"`
	AgentType string          `json:"agent_type"`
}

// Tools that edit one known file, and the input field naming it.
var claudeFileTools = map[string]string{
	"Edit": "file_path", "MultiEdit": "file_path", "Write": "file_path", "NotebookEdit": "notebook_path",
}

// Tools that can change anything under the folder they run in.
var claudeShellTools = map[string]bool{"Bash": true, "PowerShell": true}

func cmdHook(args []string) {
	defer func() {
		if r := recover(); r != nil {
			logError(fmt.Sprintf("hook panic: %v", r))
		}
		os.Exit(0)
	}()
	if len(args) == 0 || args[0] != "claude" {
		logError("hook: unknown integration " + strings.Join(args, " "))
		return
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		logError(err)
		return
	}
	if err := handleClaude(data); err != nil {
		logError(err)
	}
}

func handleClaude(data []byte) error {
	var ev claudeEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("claude hook: %w", err)
	}
	input := map[string]any{}
	json.Unmarshal(ev.ToolInput, &input)
	str := func(k string) string { v, _ := input[k].(string); return v }
	key := "claude:" + ev.ToolUseID
	if ev.ToolUseID == "" {
		key = "claude:" + ev.SessionID + ":" + ev.ToolName
	}
	isMCP := strings.HasPrefix(ev.ToolName, "mcp__")
	if ev.Event == "PreToolUse" && claudeFileTools[ev.ToolName] == "" && !claudeShellTools[ev.ToolName] {
		return nil // nothing to capture beforehand
	}
	if (ev.Event == "PostToolUse" || ev.Event == "PostToolUseFailure") && !isMCP &&
		claudeFileTools[ev.ToolName] == "" && !claudeShellTools[ev.ToolName] {
		return nil
	}

	s, err := openStore()
	if err != nil {
		return err
	}

	switch ev.Event {
	case "UserPromptSubmit":
		return s.SetSessionPrompt(ev.SessionID, ev.Prompt)

	case "PreToolUse":
		var p *ledger.Pending
		if field := claudeFileTools[ev.ToolName]; field != "" {
			f := str(field)
			if f == "" {
				return nil
			}
			if !filepath.IsAbs(f) {
				f = filepath.Join(ev.Cwd, f)
			}
			p = s.BeginFiles([]string{f})
		} else {
			root, scoped := ledger.FindRoot(ev.Cwd)
			p = s.BeginTree(root, scoped)
		}
		return s.SavePending(key, p)

	case "PostToolUse", "PostToolUseFailure":
		failed := ev.Event == "PostToolUseFailure"
		actor := ledger.Actor{Kind: "agent", Name: "claude-code", Session: ev.SessionID}
		if ev.AgentType != "" {
			actor.Name += "/" + ev.AgentType
		}
		why := oneLine(s.SessionPrompt(ev.SessionID), 400)

		if isMCP {
			// Something happened outside the file system. Trail can record it, not reverse it.
			_, err := s.Append(ledger.Body{
				Actor:  actor,
				Action: ledger.Action{Type: "external", Tool: ev.ToolName, Summary: mcpSummary(ev.ToolName), Command: oneLine(string(ev.ToolInput), 600), Failed: failed},
				Why:    why,
			})
			return err
		}

		p, err := s.TakePending(key)
		if err != nil || p == nil {
			return err
		}
		changes := s.Finish(p)
		if len(changes) == 0 && p.Kind != "none" {
			return nil // a read-only command: nothing changed, nothing to record
		}
		body := ledger.Body{
			Actor:   actor,
			Action:  ledger.Action{Type: "tool", Tool: ev.ToolName, Failed: failed},
			Why:     why,
			Root:    p.Root,
			Changes: changes,
			Note:    p.Note,
		}
		if claudeShellTools[ev.ToolName] {
			cmd := str("command")
			body.Action.Command = oneLine(cmd, 2000)
			body.Action.Summary = oneLine(str("description"), 80)
			if body.Action.Summary == "" {
				body.Action.Summary = "ran " + oneLine(cmd, 72)
			}
			if body.Root == "" {
				body.Root = ev.Cwd
			}
		} else {
			body.Action.Summary = fileSummary(changes, str(claudeFileTools[ev.ToolName]))
			if root, ok := ledger.FindRoot(ev.Cwd); ok {
				body.Root = root
			}
		}
		_, err = s.Append(body)
		return err
	}
	return nil
}

func fileSummary(changes []ledger.Change, fallback string) string {
	if len(changes) == 0 {
		return "edited " + filepath.Base(fallback)
	}
	verb := map[string]string{"create": "created", "delete": "deleted", "modify": "edited"}[changes[0].Op]
	return verb + " " + filepath.Base(changes[0].Path)
}

// mcp__github__create_issue → "github · create issue"
func mcpSummary(tool string) string {
	parts := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2)
	if len(parts) == 2 {
		return parts[0] + " · " + strings.ReplaceAll(parts[1], "_", " ")
	}
	return tool
}

// ---- install / uninstall --------------------------------------------------

func cmdInstall(args []string, remove bool) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	project := fs.Bool("project", false, "")
	withMCP := fs.Bool("with-mcp", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "claude" {
		return errors.New("usage: trail install claude [--project] [--with-mcp]\n" +
			"For any other CLI, use `trail wrap add <name>`; for your own tools, `trail begin`/`trail end`.")
	}
	path, err := claudeSettingsPath(*project)
	if err != nil {
		return err
	}
	if err := writeClaudeHooks(path, remove, *withMCP); err != nil {
		return err
	}
	if remove {
		fmt.Printf("%s Disconnected from Claude Code %s\n", green("✓"), dim("("+pretty(path)+")"))
		fmt.Println(dim("  Your history stays in the ledger."))
		return nil
	}
	if _, err := openStore(); err != nil {
		return err
	}
	fmt.Printf("%s Connected to Claude Code %s\n\n", green("✓"), dim("("+pretty(path)+")"))
	fmt.Println("  Every file Claude edits, and every file its shell commands change,")
	fmt.Println("  now lands on your timeline — with the prompt that asked for it.")
	if *withMCP {
		fmt.Println("  Actions through MCP tools are recorded too (recorded, not reversible).")
	}
	fmt.Println()
	fmt.Println("  Ask Claude Code to change something, then run " + bold("trail log"))
	return nil
}

func claudeSettingsPath(project bool) (string, error) {
	if project {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root, _ := ledger.FindRoot(cwd)
		return filepath.Join(root, ".claude", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func trailHookHandler(timeout int) (map[string]any, error) {
	exe, err := executable()
	if err != nil {
		return nil, err
	}
	h := map[string]any{"type": "command", "timeout": timeout}
	if strings.ContainsAny(exe, " \t") {
		h["command"] = exe
		h["args"] = []any{"hook", "claude"}
	} else {
		// Forward slashes work in sh, Git Bash and PowerShell alike.
		h["command"] = filepath.ToSlash(exe) + " hook claude"
	}
	return h, nil
}

func isTrailHandler(h map[string]any) bool {
	cmd, _ := h["command"].(string)
	if !strings.Contains(strings.ToLower(cmd), "trail") {
		return false
	}
	if strings.Contains(cmd, "hook claude") {
		return true
	}
	if a, ok := h["args"].([]any); ok && len(a) == 2 && a[0] == "hook" && a[1] == "claude" {
		return true
	}
	return false
}

func writeClaudeHooks(path string, remove, withMCP bool) error {
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &settings); err != nil {
				return fmt.Errorf("%s isn't valid JSON (%v) — fix it first; trail won't overwrite it", pretty(path), err)
			}
		}
		backup := path + ".before-trail"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			os.WriteFile(backup, raw, 0o600)
		}
	case errors.Is(err, os.ErrNotExist):
		if remove {
			return nil
		}
	default:
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	matcher := "Edit|MultiEdit|Write|NotebookEdit|Bash|PowerShell"
	if withMCP {
		matcher = "^(Edit|MultiEdit|Write|NotebookEdit|Bash|PowerShell)$|^mcp__"
	}
	events := []struct {
		name    string
		matcher string
		timeout int
	}{
		{"UserPromptSubmit", "", 10},
		{"PreToolUse", matcher, 60},
		{"PostToolUse", matcher, 60},
		{"PostToolUseFailure", matcher, 60},
	}
	for _, ev := range events {
		groups, _ := hooks[ev.name].([]any)
		var kept []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hs, _ := gm["hooks"].([]any)
			var rest []any
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok && isTrailHandler(hm) {
					continue
				}
				rest = append(rest, h)
			}
			if len(rest) == 0 && len(hs) > 0 {
				continue
			}
			gm["hooks"] = rest
			kept = append(kept, gm)
		}
		if !remove {
			h, err := trailHookHandler(ev.timeout)
			if err != nil {
				return err
			}
			group := map[string]any{"hooks": []any{h}}
			if ev.matcher != "" {
				group["matcher"] = ev.matcher
			}
			kept = append(kept, group)
		}
		if len(kept) == 0 {
			delete(hooks, ev.name)
		} else {
			hooks[ev.name] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// claudeConnected returns the settings file where trail's hooks are installed, if any.
func claudeConnected() string {
	var paths []string
	if p, err := claudeSettingsPath(false); err == nil {
		paths = append(paths, p)
	}
	if p, err := claudeSettingsPath(true); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s struct {
			Hooks map[string][]struct {
				Hooks []map[string]any `json:"hooks"`
			} `json:"hooks"`
		}
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		for _, g := range s.Hooks["PreToolUse"] {
			for _, h := range g.Hooks {
				if isTrailHandler(h) {
					return p
				}
			}
		}
	}
	return ""
}
