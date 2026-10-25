package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hacker-git-code/trail/ledger"
)

func undoneBy(entries []*ledger.Entry) map[string]*ledger.Entry {
	latest := map[string]*ledger.Entry{} // entry id → the most recent undo of it
	for _, e := range entries {
		if e.Body.Action.Type == "undo" {
			latest[e.Body.Action.Ref] = e
		}
	}
	// An entry is undone if its latest undo was not itself undone (a redo).
	memo := map[string]bool{}
	var isUndone func(id string, depth int) bool
	isUndone = func(id string, depth int) bool {
		if v, ok := memo[id]; ok {
			return v
		}
		u := latest[id]
		v := u != nil && depth < 10000 && !isUndone(u.ID, depth+1)
		memo[id] = v
		return v
	}
	out := map[string]*ledger.Entry{}
	for id, u := range latest {
		if isUndone(id, 0) {
			out[id] = u
		}
	}
	return out
}

func touches(e *ledger.Entry, root string) bool {
	if e.Body.Root == root {
		return true
	}
	for _, c := range e.Body.Changes {
		if strings.HasPrefix(c.Path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("n", 20, "")
	agent := fs.String("agent", "", "")
	here := fs.Bool("here", false, "")
	asJSON := fs.Bool("json", false, "")
	all := fs.Bool("all", false, "")
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
	undone := undoneBy(entries)
	root := ""
	if *here {
		cwd, _ := os.Getwd()
		root, _ = ledger.FindRoot(cwd)
	}
	var shown []*ledger.Entry
	for i := len(entries) - 1; i >= 0 && (len(shown) < *n || *n <= 0); i-- {
		e := entries[i]
		if e.Body.Action.Type == "init" && !*all {
			continue
		}
		if *agent != "" && !strings.HasPrefix(e.Body.Actor.Name, *agent) {
			continue
		}
		if root != "" && !touches(e, root) {
			continue
		}
		shown = append(shown, e)
	}
	if *asJSON {
		for i := len(shown) - 1; i >= 0; i-- {
			fmt.Println(string(shown[i].Line()))
		}
		return nil
	}
	if len(shown) == 0 {
		fmt.Println("Nothing here yet. Every change an AI makes for you will show up on this timeline.")
		fmt.Println()
		fmt.Println("  Connect Claude Code    " + bold("trail install claude"))
		fmt.Println("  Wrap any other CLI     " + bold("trail wrap add codex"))
		return nil
	}
	lastDay := ""
	for _, e := range shown {
		t := e.When().Local()
		if day := t.Format("Mon 2 Jan 2006"); day != lastDay {
			if lastDay != "" {
				fmt.Println()
			}
			fmt.Println(bold(day))
			lastDay = day
		}
		summary := oneLine(e.Body.Action.Summary, 60)
		if u, ok := undone[e.ID]; ok {
			summary = dim(summary + "  (undone by " + u.Short() + ")")
		}
		if e.Body.Action.Type == "external" {
			summary += purple("  can't be undone")
		}
		fmt.Printf("  %s  %s %s  %s  %s  %s\n",
			dim(t.Format("15:04")), marker(e), dim(e.Short()),
			cyan(fmt.Sprintf("%-12s", oneLine(e.Body.Actor.Name, 12))), summary, stats(e.Body.Changes))
		pad := strings.Repeat(" ", 30)
		if e.Body.Why != "" {
			fmt.Println(pad + dim("why  "+oneLine(e.Body.Why, 80)))
		}
		if e.Body.Note != "" {
			fmt.Println(pad + yellow("!    "+oneLine(e.Body.Note, 80)))
		}
		if e.Body.Recovery != "" {
			fmt.Println(pad + purple("fix  ") + oneLine(e.Body.Recovery, 80))
		}
	}
	return nil
}

func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	noDiff := fs.Bool("no-diff", false, "")
	full := fs.Bool("full", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: trail show <id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	e, err := ledger.FindIn(entries, pos[0])
	if err != nil {
		return err
	}
	b := e.Body
	row := func(label, value string) {
		if value != "" {
			fmt.Printf("  %s  %s\n", dim(fmt.Sprintf("%-5s", label)), value)
		}
	}
	fmt.Printf("%s %s\n", marker(e), bold(oneLine(b.Action.Summary, 100)))
	fmt.Println(dim("  " + e.ID))
	fmt.Println()
	row("when", e.When().Local().Format("Mon 2 Jan 2006, 15:04:05"))
	who := b.Actor.Name
	if b.Actor.Session != "" {
		who += dim("  session " + oneLine(b.Actor.Session, 8))
	}
	if b.Actor.Host != "" {
		who += dim("  on " + b.Actor.Host)
	}
	row("who", who)
	row("why", b.Why)
	if b.Action.Tool != "" {
		row("tool", b.Action.Tool)
	}
	row("ran", b.Action.Command)
	if b.Action.Failed {
		row("exit", red(fmt.Sprintf("failed (%d)", b.Action.Exit)))
	}
	if b.Root != "" {
		row("in", pretty(b.Root))
	}
	if b.Action.Ref != "" {
		row("ref", b.Action.Ref[:8])
	}
	if u, ok := undoneBy(entries)[e.ID]; ok {
		row("undo", yellow("undone by "+u.Short()))
	}
	row("note", b.Note)
	if b.Action.Type == "external" {
		row("undo", purple("can't be undone by Trail — it happened outside your files"))
		row("fix", b.Recovery)
	}
	if len(b.Changes) == 0 {
		return nil
	}
	fmt.Println()
	for _, c := range b.Changes {
		fmt.Printf("  %s %s  %s\n", opLetter(c.Op), bold(pretty(c.Path)), stats([]ledger.Change{c}))
		if *noDiff {
			continue
		}
		printDiff(s, c, *full)
	}
	if b.Action.Type != "external" {
		fmt.Println()
		fmt.Println(dim("  to reverse this:  trail undo " + e.Short()))
	}
	return nil
}

func printDiff(s *ledger.Store, c ledger.Change, full bool) {
	var a, b []byte
	if c.Before != "" {
		a, _ = s.GetBlob(c.Before)
	}
	if c.After != "" {
		b, _ = s.GetBlob(c.After)
	}
	indent := "      "
	switch {
	case c.Binary || ledger.IsBinary(a) || ledger.IsBinary(b):
		fmt.Println(indent + dim("binary file"))
		return
	case len(a)+len(b) > 2<<20:
		fmt.Println(indent + dim("too large to show as a diff"))
		return
	}
	dl, ok := ledger.DiffLines(ledger.SplitLines(string(a)), ledger.SplitLines(string(b)))
	if !ok {
		fmt.Println(indent + dim("changed too much to show line by line"))
		return
	}
	printed, limit := 0, 200
	for _, h := range ledger.Hunks(dl, 3) {
		fmt.Println(indent + cyan(fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.AStart, h.ALen, h.BStart, h.BLen)))
		for _, l := range h.Lines {
			if !full && printed >= limit {
				fmt.Println(indent + dim("… more — use --full"))
				return
			}
			line := string(l.Op) + " " + l.Text
			switch l.Op {
			case ledger.Insert:
				line = green(line)
			case ledger.Delete:
				line = red(line)
			default:
				line = dim(line)
			}
			fmt.Println(indent + line)
			printed++
		}
	}
}

func cmdUndo(args []string) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	dry := fs.Bool("dry-run", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
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
	undone := undoneBy(entries)
	var target *ledger.Entry
	if len(pos) > 0 {
		if target, err = ledger.FindIn(entries, pos[0]); err != nil {
			return err
		}
		if len(target.Body.Changes) == 0 && target.Body.Action.Type != "init" {
			// An email sent, an issue created: a file diff can't reverse it,
			// so say plainly what happened and how a person can fix it.
			fmt.Printf("%s %s can't be undone by Trail — it happened outside your files.\n\n", purple("◆"), bold(oneLine(target.Body.Action.Summary, 70)))
			fmt.Printf("  %s  %s, %s\n", dim("what"), target.Body.Actor.Name, target.When().Local().Format("Mon 2 Jan 15:04"))
			if target.Body.Action.Command != "" {
				fmt.Printf("  %s  %s\n", dim("sent"), oneLine(target.Body.Action.Command, 200))
			}
			fix := target.Body.Recovery
			if fix == "" {
				fix = "Reverse it by hand where it happened."
			}
			fmt.Printf("  %s   %s\n", dim("fix"), fix)
			return errQuiet
		}
		if u, ok := undone[target.ID]; ok && !*force {
			return fmt.Errorf("%s was already undone by %s — run `trail undo %s` to bring it back", target.Short(), u.Short(), u.Short())
		}
	} else {
		// The latest change that has not been undone, so repeated `trail undo`
		// keeps walking back in time.
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if len(e.Body.Changes) > 0 && e.Body.Action.Type != "undo" && undone[e.ID] == nil {
				target = e
				break
			}
		}
		if target == nil {
			fmt.Println("Nothing to undo.")
			return nil
		}
	}
	actor := ledger.Actor{Kind: "human", Name: username()}
	e, plan, conflicts, err := s.Undo(target, actor, *force, *dry)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 && !*force {
		fmt.Printf("%s Can't undo %s cleanly — %d file(s) changed after it:\n\n", red("✗"), bold(target.Short()), len(conflicts))
		for _, c := range conflicts {
			fmt.Printf("  %s  %s%s\n", bold(pretty(c.Path)), c.Reason, laterChange(entries, target, c.Path))
		}
		fmt.Println()
		fmt.Println("Undo the later change first, or run with --force to overwrite it.")
		fmt.Println(dim("(--force is safe to try: the overwritten version is saved, and the undo can itself be undone.)"))
		return errQuiet
	}
	if *dry {
		fmt.Printf("Undoing %s would:\n", bold(target.Short()))
		for _, c := range plan {
			verb := map[string]string{"create": "bring back", "delete": "remove", "modify": "restore"}[c.Op]
			fmt.Printf("  %s %-11s %s\n", opLetter(c.Op), verb, pretty(c.Path))
		}
		return nil
	}
	fmt.Printf("%s Undid %s · %s\n", yellow("↺"), bold(target.Short()), oneLine(target.Body.Action.Summary, 60))
	files := "file"
	if len(plan) != 1 {
		files = "files"
	}
	fmt.Printf("  %d %s put back.  %s\n", len(plan), files, dim("Changed your mind? trail undo "+e.Short()))
	return nil
}

// laterChange names who touched a path after an entry, if Trail saw it.
func laterChange(entries []*ledger.Entry, after *ledger.Entry, path string) string {
	var hit *ledger.Entry
	for _, e := range entries {
		if e.Body.Seq <= after.Body.Seq {
			continue
		}
		for _, c := range e.Body.Changes {
			if c.Path == path {
				hit = e
			}
		}
	}
	if hit == nil {
		return dim(" — outside anything Trail recorded")
	}
	return dim(fmt.Sprintf(" — by %s at %s (%s)", hit.Body.Actor.Name, hit.When().Local().Format("15:04"), hit.Short()))
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	quick := fs.Bool("quick", false, "")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	r, err := s.Verify(!*quick)
	if err != nil {
		return err
	}
	if !r.OK() {
		fmt.Printf("%s The history does not check out (%d problem(s)):\n\n", red("✗"), len(r.Problems))
		for _, p := range r.Problems {
			fmt.Println("  " + p)
		}
		return errQuiet
	}
	head := "—"
	if r.Head != nil {
		head = r.Head.Short()
	}
	fmt.Printf("%s %d entries · chain intact · every signature valid", green("✓"), r.Entries)
	if !*quick {
		fmt.Print(" · every saved version present")
	}
	fmt.Printf(" · head %s\n", bold(head))
	return nil
}

func cmdHead(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	r, err := s.Verify(false)
	if err != nil {
		return err
	}
	if r.Head == nil {
		return errors.New("the ledger is empty")
	}
	fmt.Printf("%d %s\n", r.Head.Body.Seq, r.Head.ID)
	if isTTY(os.Stdout) {
		fmt.Fprintln(os.Stderr, dim("\nPost this line somewhere you don't control (a tweet, a git commit, an email to yourself)."))
		fmt.Fprintln(os.Stderr, dim("If anything before it is ever rewritten — even by someone holding your key — it won't match."))
	}
	return nil
}

func cmdStatus(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	fmt.Println(bold("trail") + " " + dim(version))
	head := ""
	if len(entries) > 0 {
		head = " · head " + entries[len(entries)-1].Short()
	}
	fmt.Printf("  %s  %s · %d entries%s\n", dim("ledger "), pretty(s.Dir), len(entries), head)
	fmt.Printf("  %s  ed25519 · %s\n", dim("key    "), s.Fingerprint())
	if where := claudeConnected(); where != "" {
		fmt.Printf("  %s  %s %s\n", dim("claude "), green("connected"), dim("("+pretty(where)+")"))
	} else {
		fmt.Printf("  %s  not connected — %s\n", dim("claude "), bold("trail install claude"))
	}
	if len(s.Config.Wrap) > 0 {
		fmt.Printf("  %s  %s\n", dim("wrapped"), strings.Join(s.Config.Wrap, ", "))
	} else {
		fmt.Printf("  %s  none — %s\n", dim("wrapped"), bold("trail wrap add <cli>"))
	}
	if len(s.Config.Watch) > 0 {
		state := yellow("not running") + dim(" — trail watch")
		if watcherAlive(s) {
			state = green("running")
		}
		names := make([]string, len(s.Config.Watch))
		for i, w := range s.Config.Watch {
			names[i] = pretty(w)
		}
		fmt.Printf("  %s  %s · %s\n", dim("watch  "), strings.Join(names, ", "), state)
	} else {
		fmt.Printf("  %s  none — %s\n", dim("watch  "), bold("trail watch add <folder>"))
	}
	return nil
}
