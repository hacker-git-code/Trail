package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hacker-git-code/trail/ledger"
)

// Once a secret has left the computer, nobody can pull it back. What makes it
// harmless is cancelling it, so the copy that leaked stops working.
// `trail leaks` lists every secret Trail saw leave, and how to cancel each one.

var fixFor = map[string]string{
	"Anthropic API key":          "Delete it and make a new one: https://console.anthropic.com/settings/keys",
	"OpenAI API key":             "Delete it and make a new one: https://platform.openai.com/api-keys",
	"GitHub token":               "Revoke it: https://github.com/settings/tokens",
	"AWS access key":             "Deactivate it and create a new one: https://console.aws.amazon.com/iam/home#/security_credentials",
	"Stripe key":                 "Roll it: https://dashboard.stripe.com/apikeys",
	"Razorpay key":               "Regenerate it: Razorpay Dashboard → Account & Settings → API Keys",
	"Google API key":             "Regenerate or delete it: https://console.cloud.google.com/apis/credentials",
	"Slack token":                "Revoke it in your app's settings: https://api.slack.com/apps",
	"SendGrid key":               "Delete it and make a new one: https://app.sendgrid.com/settings/api_keys",
	"Private key":                "Make a new key pair, then remove the old public key wherever it's trusted (GitHub → Settings → SSH keys, servers' authorized_keys).",
	"JWT":                        "Sign out that session so the token stops working; if it was long-lived, rotate the secret that signs it.",
	"Database URL with password": "Change that database user's password, then update the connection string.",
	"Password or secret":         "Change it wherever it's used.",
}

type leak struct {
	secret  ledger.Secret
	first   time.Time
	last    time.Time
	dests   []string
	sources []string
	ids     []string
}

func collectLeaks(entries []*ledger.Entry) []*leak {
	fixed := map[string]bool{}
	for _, e := range entries {
		if e.Body.Action.Type == "resolved" {
			fixed[e.Body.Action.Ref] = true
		}
	}
	byHash := map[string]*leak{}
	var order []string
	add := func(e *ledger.Entry, sec ledger.Secret, source string) {
		if fixed[sec.Hash] {
			return
		}
		l := byHash[sec.Hash]
		if l == nil {
			l = &leak{secret: sec, first: e.When()}
			byHash[sec.Hash] = l
			order = append(order, sec.Hash)
		}
		l.last = e.When()
		if e.Body.Dest != "" && !contains(l.dests, e.Body.Dest) {
			l.dests = append(l.dests, e.Body.Dest)
		}
		if source != "" && !contains(l.sources, source) {
			l.sources = append(l.sources, source)
		}
		if !contains(l.ids, e.Short()) {
			l.ids = append(l.ids, e.Short())
		}
	}
	for _, e := range entries {
		switch e.Body.Action.Type {
		case "sent":
			for _, it := range e.Body.Sent {
				for _, sec := range it.Secrets {
					add(e, sec, it.Source)
				}
			}
		case "external":
			for _, sec := range e.Body.Secrets {
				add(e, sec, "")
			}
		}
	}
	out := make([]*leak, 0, len(order))
	for _, h := range order {
		out = append(out, byHash[h])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].last.After(out[j].last) })
	return out
}

func cmdLeaks(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	entries, err := s.Entries()
	if err != nil {
		return err
	}
	leaks := collectLeaks(entries)
	if len(args) > 0 && (args[0] == "fixed" || args[0] == "done") {
		if len(args) < 2 {
			return errors.New("usage: trail leaks fixed <number>   (the number from `trail leaks`)")
		}
		for _, a := range args[1:] {
			n, err := strconv.Atoi(a)
			if err != nil || n < 1 || n > len(leaks) {
				return fmt.Errorf("there's no leak number %s — run `trail leaks` to see the list", a)
			}
			l := leaks[n-1]
			if _, err := s.Append(ledger.Body{
				Actor:  ledger.Actor{Kind: "human", Name: username()},
				Action: ledger.Action{Type: "resolved", Ref: l.secret.Hash, Summary: "cancelled the leaked " + l.secret.Kind + " " + l.secret.Masked},
			}); err != nil {
				return err
			}
			fmt.Printf("%s Marked the %s %s as cancelled. The copy that leaked no longer matters.\n", green("✓"), l.secret.Kind, dim(l.secret.Masked))
		}
		return nil
	}
	if len(leaks) == 0 {
		fmt.Println(green("✓") + " No secrets have left your computer through anything Trail watches.")
		if !s.Config.GuardOn() {
			fmt.Println(dim("  The guard is off — turn it on to stop leaks before they happen: trail guard on"))
		}
		return nil
	}
	fmt.Printf("%s %s left your computer.\n", red("!"), bold(plural(len(leaks), "secret")))
	fmt.Println(dim("  Sent data can't be pulled back, but a cancelled key is useless to whoever has it."))
	fmt.Println()
	for i, l := range leaks {
		fmt.Printf("  %s  %s  %s\n", bold(strconv.Itoa(i+1)), bold(l.secret.Kind), dim(l.secret.Masked))
		where := "sent"
		if len(l.dests) > 0 {
			where = "sent to " + strings.Join(l.dests, ", ")
		}
		if len(l.sources) > 0 {
			src := l.sources[0]
			if filepath.IsAbs(src) {
				src = pretty(src)
			}
			if len(l.sources) > 1 {
				src += fmt.Sprintf(" (+%d more)", len(l.sources)-1)
			}
			where += " · from " + src
		}
		fmt.Printf("     %s %s · %s\n", dim("where"), where, dim(l.last.Local().Format("2 Jan 15:04")+" · "+strings.Join(l.ids, " ")))
		fix := fixFor[l.secret.Kind]
		if fix == "" {
			fix = fixFor["Password or secret"]
		}
		fmt.Printf("     %s   %s\n", purple("fix"), fix)
		fmt.Println()
	}
	fmt.Println("  Then put the new key where the old one was (like your .env).")
	fmt.Println("  Done with one? " + bold("trail leaks fixed <number>"))
	if !s.Config.GuardOn() {
		fmt.Println(dim("  Stop the next one before it happens: trail guard on"))
	}
	return nil
}
