package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/hacker-git-code/trail/ledger"
)

// ---- trail run: the universal wrapper -------------------------------------

func cmdRun(args []string) {
	agent, why, rootFlag := "", "", ""
	var cmdArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--":
			cmdArgs = args[i+1:]
		case a == "--agent":
			agent = next()
			continue
		case a == "--why":
			why = next()
			continue
		case a == "--root":
			rootFlag = next()
			continue
		case strings.HasPrefix(a, "--agent="):
			agent = strings.TrimPrefix(a, "--agent=")
			continue
		case strings.HasPrefix(a, "--why="):
			why = strings.TrimPrefix(a, "--why=")
			continue
		default:
			cmdArgs = args[i:]
		}
		break
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: trail run [--agent NAME] [--why TEXT] [--root DIR] -- <command> [args...]")
		os.Exit(2)
	}
	if agent == "" {
		agent = strings.TrimSuffix(filepath.Base(cmdArgs[0]), filepath.Ext(cmdArgs[0]))
	}

	// If Trail itself is broken, the wrapped command must still run.
	s, err := openStore()
	var p *ledger.Pending
	if err != nil {
		fmt.Fprintln(os.Stderr, "trail: not recording this run:", err)
	} else {
		cwd, _ := os.Getwd()
		root, scoped := ledger.FindRoot(cwd)
		if rootFlag != "" {
			root, _ = filepath.Abs(rootFlag)
			scoped = true
		}
		p = s.BeginTree(root, scoped)
	}

	c := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl-C reaches the child straight from the terminal; trail stays alive to
	// record what happened. SIGTERM sent to trail alone is passed on.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	code := 0
	if err := c.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "trail:", err)
		os.Exit(127)
	}
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGTERM && c.Process != nil {
				c.Process.Signal(sig)
			}
		}
	}()
	if err := c.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if code < 0 {
				code = 1
			}
		} else {
			code = 1
		}
	}
	signal.Stop(sigs)

	if s != nil && p != nil {
		changes := s.Finish(p)
		if len(changes) > 0 || p.Kind == "none" {
			e, err := s.Append(ledger.Body{
				Actor:   ledger.Actor{Kind: "agent", Name: agent},
				Action:  ledger.Action{Type: "run", Summary: "ran " + oneLine(strings.Join(cmdArgs, " "), 72), Command: oneLine(strings.Join(cmdArgs, " "), 2000), Failed: code != 0, Exit: code},
				Why:     why,
				Root:    p.Root,
				Changes: changes,
				Note:    p.Note,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, "trail: could not record this run:", err)
			} else if isTTY(os.Stderr) && len(changes) > 0 {
				files := "file"
				if len(changes) != 1 {
					files = "files"
				}
				fmt.Fprintf(os.Stderr, "\n%s %d %s changed · recorded %s · %s\n",
					dim("trail"), len(changes), files, e.Short(), dim("undo: trail undo "+e.Short()))
			}
		}
	}
	os.Exit(code)
}

// ---- trail begin / end: bracket anything ----------------------------------

func newToken() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func cmdBegin(args []string) error {
	fs := flag.NewFlagSet("begin", flag.ContinueOnError)
	agent := fs.String("agent", "", "")
	why := fs.String("why", "", "")
	root := fs.String("root", "", "")
	files := fs.String("files", "", "")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	if *agent == "" {
		return errors.New("usage: trail begin --agent NAME [--why TEXT] [--root DIR | --files a,b]   (prints a token for `trail end`)")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	var p *ledger.Pending
	if *files != "" {
		var paths []string
		for _, f := range strings.Split(*files, ",") {
			if f = strings.TrimSpace(f); f != "" {
				abs, _ := filepath.Abs(f)
				paths = append(paths, abs)
			}
		}
		p = s.BeginFiles(paths)
	} else {
		dir := *root
		scoped := true
		if dir == "" {
			cwd, _ := os.Getwd()
			dir, scoped = ledger.FindRoot(cwd)
		}
		dir, _ = filepath.Abs(dir)
		p = s.BeginTree(dir, scoped)
	}
	p.Agent, p.Why = *agent, *why
	token := newToken()
	if err := s.SavePending("begin:"+token, p); err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

func cmdEnd(args []string) error {
	fs := flag.NewFlagSet("end", flag.ContinueOnError)
	why := fs.String("why", "", "")
	summary := fs.String("summary", "", "")
	exit := fs.Int("exit", 0, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: trail end <token> [--summary TEXT] [--why TEXT] [--exit CODE]")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	p, err := s.TakePending("begin:" + pos[0])
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("no open action with token %s (already ended, or never begun)", pos[0])
	}
	changes := s.Finish(p)
	if len(changes) == 0 && p.Kind != "none" {
		return nil
	}
	w := p.Why
	if *why != "" {
		w = *why
	}
	sum := *summary
	if sum == "" {
		sum = p.Agent + " session"
	}
	e, err := s.Append(ledger.Body{
		Actor:   ledger.Actor{Kind: "agent", Name: p.Agent},
		Action:  ledger.Action{Type: "run", Summary: oneLine(sum, 120), Failed: *exit != 0, Exit: *exit},
		Why:     w,
		Root:    p.Root,
		Changes: changes,
		Note:    p.Note,
	})
	if err != nil {
		return err
	}
	fmt.Println(e.ID)
	return nil
}

// ---- trail note: actions outside the file system --------------------------

func cmdNote(args []string) error {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	agent := fs.String("agent", "", "")
	why := fs.String("why", "", "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	text := strings.Join(pos, " ")
	if text == "" {
		return errors.New(`usage: trail note [--agent NAME] [--why TEXT] "what happened"`)
	}
	actor := ledger.Actor{Kind: "agent", Name: *agent}
	if *agent == "" {
		actor = ledger.Actor{Kind: "human", Name: username()}
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	e, err := s.Append(ledger.Body{Actor: actor, Action: ledger.Action{Type: "external", Summary: oneLine(text, 200)}, Why: *why})
	if err != nil {
		return err
	}
	fmt.Println(e.ID)
	return nil
}

// ---- trail wrap / shell-init: every CLI ------------------------------------

var cliName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func cmdWrap(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	sub, names := args[0], args[1:]
	switch sub {
	case "list":
		if len(s.Config.Wrap) == 0 {
			fmt.Println("No CLIs wrapped yet. Try: " + bold("trail wrap add codex"))
			return nil
		}
		for _, n := range s.Config.Wrap {
			fmt.Println(n)
		}
		return nil
	case "add", "rm", "remove":
	default:
		return errors.New("usage: trail wrap add|rm|list <cli>...")
	}
	if len(names) == 0 {
		return fmt.Errorf("usage: trail wrap %s <cli>...", sub)
	}
	set := map[string]bool{}
	for _, n := range s.Config.Wrap {
		set[n] = true
	}
	for _, n := range names {
		if !cliName.MatchString(n) || n == "trail" {
			return fmt.Errorf("%q can't be wrapped", n)
		}
		set[n] = sub == "add"
	}
	s.Config.Wrap = s.Config.Wrap[:0]
	for n, on := range set {
		if on {
			s.Config.Wrap = append(s.Config.Wrap, n)
		}
	}
	sortStrings(s.Config.Wrap)
	if err := s.SaveConfig(); err != nil {
		return err
	}
	if sub != "add" {
		fmt.Printf("%s Unwrapped %s. Open a new terminal for it to take effect.\n", green("✓"), strings.Join(names, ", "))
		return nil
	}
	fmt.Printf("%s Wrapped %s. Every run will land on your timeline.\n\n", green("✓"), strings.Join(names, ", "))
	exe, err := executable()
	if err != nil {
		exe = "trail"
	}
	shell := detectShell()
	fmt.Println("  One-time setup — add this line to " + shellRC(shell) + ", then open a new terminal:")
	fmt.Println()
	fmt.Println("    " + bold(shellInitLine(shell, exe)))
	return nil
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func detectShell() string {
	if os.Getenv("PSModulePath") != "" && os.Getenv("SHELL") == "" {
		return "powershell"
	}
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return "zsh"
	case "fish":
		return "fish"
	}
	return "bash"
}

func shellRC(shell string) string {
	return map[string]string{"zsh": "~/.zshrc", "fish": "~/.config/fish/config.fish", "powershell": "your $PROFILE", "bash": "~/.bashrc"}[shell]
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func shellInitLine(shell, exe string) string {
	switch shell {
	case "fish":
		return shQuote(exe) + " shell-init fish | source"
	case "powershell":
		return "& " + psQuote(exe) + " shell-init powershell | Out-String | Invoke-Expression"
	}
	return `eval "$(` + shQuote(exe) + ` shell-init ` + shell + `)"`
}

func cmdShellInit(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: trail shell-init bash|zsh|fish|powershell")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# trail: every run of these CLIs is recorded on your timeline\n")
	for _, n := range s.Config.Wrap {
		switch args[0] {
		case "bash", "zsh", "sh":
			fmt.Fprintf(&b, "%s() { %s run --agent %s -- %s \"$@\"; }\n", n, shQuote(exe), n, n)
		case "fish":
			fmt.Fprintf(&b, "function %s; %s run --agent %s -- %s $argv; end\n", n, shQuote(exe), n, n)
		case "powershell", "pwsh":
			fmt.Fprintf(&b, "function %s { & %s run --agent %s -- %s @args }\n", n, psQuote(exe), n, n)
		default:
			return fmt.Errorf("unsupported shell %q (bash, zsh, fish, powershell)", args[0])
		}
	}
	fmt.Print(b.String())
	return nil
}
