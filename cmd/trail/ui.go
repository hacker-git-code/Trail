package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hacker-git-code/trail/ledger"
)

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

var useColor = os.Getenv("NO_COLOR") == "" && isTTY(os.Stdout)

func paint(code, s string) string {
	if !useColor || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func dim(s string) string    { return paint("2", s) }
func bold(s string) string   { return paint("1", s) }
func red(s string) string    { return paint("31", s) }
func green(s string) string  { return paint("32", s) }
func yellow(s string) string { return paint("33", s) }
func cyan(s string) string   { return paint("36", s) }
func purple(s string) string { return paint("35", s) }

// pretty shows a path the way a person thinks of it: relative to here, or ~/….
func pretty(p string) string {
	if cwd, err := os.Getwd(); err == nil && filepath.Dir(cwd) != cwd {
		if rel, err := filepath.Rel(cwd, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

func marker(e *ledger.Entry) string {
	switch e.Body.Action.Type {
	case "undo":
		return yellow("↺")
	case "restore":
		return green("↩")
	case "observed":
		return cyan("●")
	case "external":
		return purple("◆")
	case "guard":
		return red("⊘")
	case "sent":
		if len(e.Body.Secrets) > 0 {
			return red("↗")
		}
		return purple("↗")
	case "network":
		return dim("⇢")
	case "resolved":
		return green("✓")
	case "init":
		return dim("○")
	}
	if e.Body.Action.Failed {
		return red("●")
	}
	return green("●")
}

func stats(changes []ledger.Change) string {
	if len(changes) == 0 {
		return ""
	}
	add, rem := 0, 0
	for _, c := range changes {
		add += c.Added
		rem += c.Removed
	}
	var parts []string
	if len(changes) > 1 {
		parts = append(parts, dim(fmt.Sprintf("%d files", len(changes))))
	}
	if add > 0 {
		parts = append(parts, green(fmt.Sprintf("+%d", add)))
	}
	if rem > 0 {
		parts = append(parts, red(fmt.Sprintf("−%d", rem)))
	}
	return strings.Join(parts, " ")
}

func opLetter(op string) string {
	switch op {
	case "create":
		return green("A")
	case "delete":
		return red("D")
	}
	return yellow("M")
}
