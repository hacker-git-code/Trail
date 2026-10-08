// Command trail records every change made on your behalf — by any AI agent,
// any CLI, or you — as a signed timeline you can inspect and undo.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/hacker-git-code/trail/ledger"
)

const version = "0.1.0"

const usage = `trail — every change made on your behalf, on a timeline you can undo.

See what happened
  trail log                 the timeline, newest first   (--here, --agent NAME, -n 50)
  trail show <id>           one entry: who, why, and the exact diff
  trail undo [id]           put files back the way they were (default: the latest change)
  trail verify              prove nothing in the history was edited, removed or reordered

Connect your tools
  trail install claude      record everything Claude Code does   (--project, --with-mcp)
  trail wrap add <cli>...   record every run of any CLI: codex, aider, gemini, …
  trail run -- <cmd>        record one command

Build on it
  trail begin / trail end   bracket any process from your own tool or script
  trail note <text>         record an action outside the file system (email sent, deploy, …)
  trail head                the current head, to publish as a tamper-proof anchor
  trail status              where things are and what is connected

The ledger lives in ~/.trail (or $TRAIL_HOME). Format: SPEC.md.
`

// errQuiet means the command already explained the failure.
var errQuiet = errors.New("")

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "log":
		err = cmdLog(args)
	case "show":
		err = cmdShow(args)
	case "undo":
		err = cmdUndo(args)
	case "verify":
		err = cmdVerify(args)
	case "head":
		err = cmdHead(args)
	case "status", "init":
		err = cmdStatus(args)
	case "run":
		cmdRun(args) // exits with the command's own exit code
	case "begin":
		err = cmdBegin(args)
	case "end":
		err = cmdEnd(args)
	case "note":
		err = cmdNote(args)
	case "hook":
		cmdHook(args) // never fails the host tool
	case "install":
		err = cmdInstall(args, false)
	case "uninstall":
		err = cmdInstall(args, true)
	case "wrap":
		err = cmdWrap(args)
	case "shell-init":
		err = cmdShellInit(args)
	case "version", "--version", "-v":
		fmt.Println("trail", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "trail: there is no %q command\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if err != errQuiet {
			fmt.Fprintln(os.Stderr, "trail:", err)
		}
		os.Exit(1)
	}
}

func openStore() (*ledger.Store, error) {
	dir, err := ledger.DefaultDir()
	if err != nil {
		return nil, err
	}
	return ledger.Open(dir)
}

// parseFlags lets flags appear anywhere: `trail show abc123 --full`.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if f := fs.Lookup(name); f != nil && !strings.Contains(a, "=") {
				if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); !isBool && i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, fmt.Errorf("%s: %v", fs.Name(), err)
	}
	return pos, nil
}

func username() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	return "you"
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if strings.Contains(exe, "go-build") {
		return "", errors.New("run this from a built binary (go build ./cmd/trail), not `go run`")
	}
	return exe, nil
}

// logError records failures from places that must stay silent (hooks).
func logError(err any) {
	dir, e := ledger.DefaultDir()
	if e != nil {
		return
	}
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "errors.log")
	if fi, e := os.Stat(p); e == nil && fi.Size() > 1<<20 {
		os.Remove(p)
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if e != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
