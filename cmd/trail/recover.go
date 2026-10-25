package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hacker-git-code/trail/ledger"
)

// Bringing files back: `trail deleted` lists what's gone, `trail history`
// shows every saved version of one file, `trail restore` puts files back.

type lastSeen struct {
	entry  *ledger.Entry
	change ledger.Change
}

// latestByPath maps every path Trail has seen to its most recent change.
func latestByPath(entries []*ledger.Entry) map[string]lastSeen {
	m := map[string]lastSeen{}
	for _, e := range entries {
		for _, c := range e.Body.Changes {
			m[c.Path] = lastSeen{e, c}
		}
	}
	return m
}

func missing(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

// deletedFiles lists files whose last recorded change removed them and that
// are still gone, newest first.
func deletedFiles(m map[string]lastSeen, under string) []lastSeen {
	var out []lastSeen
	for p, ls := range m {
		if ls.change.After != "" || ls.change.Before == "" || !missing(p) {
			continue
		}
		if under != "" && !within(p, under) {
			continue
		}
		out = append(out, ls)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].entry.Body.Seq != out[j].entry.Body.Seq {
			return out[i].entry.Body.Seq > out[j].entry.Body.Seq
		}
		return out[i].change.Path < out[j].change.Path
	})
	return out
}

func cmdDeleted(args []string) error {
	fs := flag.NewFlagSet("deleted", flag.ContinueOnError)
	here := fs.Bool("here", false, "")
	n := fs.Int("n", 50, "")
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
	under := ""
	if *here {
		under, _ = os.Getwd()
	}
	gone := deletedFiles(latestByPath(entries), under)
	if len(gone) == 0 {
		fmt.Println("No deleted files on record.")
		fmt.Println()
		fmt.Println(dim("Trail can bring back any file it saw before it was deleted: in folders you watch"))
		fmt.Println(dim("(trail watch add <folder>) and in projects your connected tools work in."))
		return nil
	}
	fmt.Println(bold(fmt.Sprintf("%d deleted file(s) Trail can bring back", len(gone))))
	fmt.Println()
	for i, ls := range gone {
		if *n > 0 && i >= *n {
			fmt.Println(dim(fmt.Sprintf("  … and %d more (-n 0 to see all)", len(gone)-i)))
			break
		}
		t := ls.entry.When().Local()
		fmt.Printf("  %s  %s  %s  %s\n", dim(t.Format("2 Jan 15:04")), red("D"), bold(pretty(ls.change.Path)),
			dim("by "+ls.entry.Body.Actor.Name+" · "+ls.entry.Short()))
	}
	fmt.Println()
	fmt.Println("  Bring one back:    " + bold("trail restore <file>"))
	fmt.Println("  A whole folder:    " + bold("trail restore <folder>"))
	return nil
}

// resolve turns what a person typed into the paths Trail knows: one file, every
// file under a folder, or a file found by its name alone.
func resolve(arg string, m map[string]lastSeen) (paths []string, isFolder bool, err error) {
	abs, err := filepath.Abs(expandHome(arg))
	if err != nil {
		return nil, false, err
	}
	if _, ok := m[abs]; ok {
		return []string{abs}, false, nil
	}
	for p := range m {
		if within(p, abs) && p != abs {
			paths = append(paths, p)
		}
	}
	if len(paths) > 0 {
		sort.Strings(paths)
		return paths, true, nil
	}
	if !strings.ContainsAny(arg, `/\`) {
		for p := range m {
			if filepath.Base(p) == arg {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		if len(paths) == 1 {
			return paths, false, nil
		}
		if len(paths) > 1 {
			var b strings.Builder
			fmt.Fprintf(&b, "%d files are called %s — use the full path:\n", len(paths), arg)
			for _, p := range paths {
				b.WriteString("  " + pretty(p) + "\n")
			}
			return nil, false, errors.New(strings.TrimRight(b.String(), "\n"))
		}
	}
	return nil, false, fmt.Errorf("Trail has no saved version of %s", arg)
}

func cmdHistory(args []string) error {
	pos, err := parseFlags(flag.NewFlagSet("history", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: trail history <file>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	paths, isFolder, err := resolve(pos[0], latestByPath(entries))
	if err != nil {
		return err
	}
	if isFolder {
		return fmt.Errorf("%s is a folder — `trail log` shows its history, `trail deleted` what's missing in it", pos[0])
	}
	path := paths[0]
	fmt.Println(bold(pretty(path)))
	fmt.Println()
	count := 0
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		for _, c := range e.Body.Changes {
			if c.Path != path {
				continue
			}
			count++
			t := e.When().Local()
			fmt.Printf("  %s  %s %s  %s  %s  %s\n", dim(t.Format("2 Jan 15:04")), opLetter(c.Op), dim(e.Short()),
				cyan(fmt.Sprintf("%-12s", oneLine(e.Body.Actor.Name, 12))), oneLine(e.Body.Action.Summary, 50), stats([]ledger.Change{c}))
		}
	}
	state := "exists now"
	if missing(path) {
		state = red("deleted now")
	}
	fmt.Println()
	fmt.Printf("  %d saved change(s) · %s\n", count, state)
	fmt.Println(dim("  Go back to how it was before a change:  trail restore " + filepath.Base(path) + " --before <id>"))
	fmt.Println(dim("  Or to the version a change left:        trail restore " + filepath.Base(path) + " --from <id>"))
	return nil
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	from := fs.String("from", "", "")
	before := fs.String("before", "", "")
	dry := fs.Bool("dry-run", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: trail restore <file|folder> [--before <id> | --from <id>] [--dry-run]")
	}
	if *before != "" && *from != "" {
		return errors.New("use --before or --from, not both")
	}
	pick, useBefore := *from, false
	if *before != "" {
		pick, useBefore = *before, true
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	m := latestByPath(entries)
	paths, isFolder, err := resolve(pos[0], m)
	if err != nil {
		return err
	}

	var targets []ledger.Target
	var summary, root string
	switch {
	case isFolder:
		if pick != "" {
			return errors.New("--before and --from work on one file; for a folder, `trail restore` brings back everything missing in it")
		}
		folder, _ := filepath.Abs(expandHome(pos[0]))
		for _, ls := range deletedFiles(m, folder) {
			targets = append(targets, ledger.Target{Path: ls.change.Path, Hash: ls.change.Before, Mode: ls.change.BeforeMode})
		}
		if len(targets) == 0 {
			fmt.Printf("Nothing in %s is missing.\n", pretty(folder))
			return nil
		}
		summary = fmt.Sprintf("restored %d files in %s", len(targets), filepath.Base(folder))
		root = folder
	default:
		path := paths[0]
		var t ledger.Target
		if pick != "" {
			e, err := ledger.FindIn(entries, pick)
			if err != nil {
				return err
			}
			found := false
			for _, c := range e.Body.Changes {
				if c.Path != path {
					continue
				}
				found = true
				t = versionOf(c)
				if useBefore {
					if c.Before == "" {
						return fmt.Errorf("%s didn't exist before %s — to remove it, run `trail undo %s`", filepath.Base(path), e.Short(), e.Short())
					}
					t = ledger.Target{Path: c.Path, Hash: c.Before, Mode: c.BeforeMode}
				}
			}
			if !found {
				return fmt.Errorf("%s didn't change %s — see `trail history %s`", e.Short(), filepath.Base(path), filepath.Base(path))
			}
		} else {
			if !missing(path) {
				fmt.Printf("%s exists. To go back to an earlier version of it:\n\n", pretty(path))
				fmt.Println("  " + bold("trail history "+filepath.Base(path)) + "     pick a change")
				fmt.Println("  " + bold("trail restore "+filepath.Base(path)+" --before <id>"))
				return nil
			}
			t = versionOf(m[path].change)
		}
		targets = []ledger.Target{t}
		summary = "restored " + filepath.Base(path)
		root = m[path].entry.Body.Root
	}

	if *dry {
		fmt.Println("Restoring would bring back:")
		for _, t := range targets {
			fmt.Println("  " + pretty(t.Path))
		}
		return nil
	}
	e, plan, err := s.Restore(targets, ledger.Actor{Kind: "human", Name: username()}, summary, root)
	if errors.Is(err, ledger.ErrNothingToRestore) {
		fmt.Println("Already in that state — nothing to restore.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", green("↩"), bold(strings.ToUpper(summary[:1])+summary[1:]))
	for i, c := range plan {
		if i == 10 {
			fmt.Println(dim(fmt.Sprintf("  … and %d more", len(plan)-10)))
			break
		}
		fmt.Println("  " + pretty(c.Path))
	}
	fmt.Println(dim("  Changed your mind? trail undo " + e.Short()))
	return nil
}

// versionOf is the version a change leaves behind, or for a deletion the
// version it removed.
func versionOf(c ledger.Change) ledger.Target {
	if c.After != "" {
		return ledger.Target{Path: c.Path, Hash: c.After, Mode: c.AfterMode}
	}
	return ledger.Target{Path: c.Path, Hash: c.Before, Mode: c.BeforeMode}
}

// ---- actions outside your files ----------------------------------------------

// recoveryHint says how to reverse an action Trail can only record, judged by
// the words in the tool's name (mcp__gmail__send_email → send, email).
// readOnly is true for lookups, which change nothing and aren't recorded.
func recoveryHint(tool string) (hint string, readOnly bool) {
	server, action := "the app", tool
	if parts := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2); len(parts) == 2 {
		server, action = parts[0], parts[1]
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(action), func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' '
	}) {
		words[w] = true
	}
	has := func(list ...string) bool {
		for _, w := range list {
			if words[w] {
				return true
			}
		}
		return false
	}
	switch {
	case has("get", "list", "read", "search", "fetch", "query", "find", "view", "describe", "lookup", "show", "download"):
		return "", true
	case has("send", "email", "mail", "message", "reply", "dm", "chat", "notify", "sms", "forward"):
		return "Can't be unsent. Follow up with the recipient, or recall it if " + server + " supports that.", false
	case has("pay", "payment", "charge", "transfer", "refund", "invoice", "order", "purchase", "buy", "checkout"):
		return "Money may have moved. Contact " + server + " or the other party to reverse it.", false
	case has("delete", "remove", "archive", "trash", "close", "cancel", "revoke", "drop"):
		return "Look in " + server + "'s trash, archive or history to bring it back.", false
	case has("update", "edit", "set", "modify", "change", "move", "rename", "merge", "assign", "patch"):
		return "Change it back in " + server + " — the input Trail recorded shows exactly what was sent.", false
	case has("create", "add", "post", "publish", "upload", "insert", "new", "comment", "write", "push", "share", "invite"):
		return "Delete or edit it in " + server + ".", false
	}
	return "Reverse it by hand in " + server + " — the input Trail recorded shows exactly what was sent.", false
}
