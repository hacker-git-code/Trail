# trail — notes for Claude Code

Trail records every change made on a person's behalf as a signed, hash-chained
timeline with undo. SPEC.md is the foundation; the code implements it.

## Principles (don't break these)

1. **The format is the product.** Any change to the entry format, hashing,
   signing or chain rules needs a SPEC.md update and a `v` bump. Readers must
   keep verifying old entries.
2. **Append only.** Never rewrite, truncate or reorder `ledger.jsonl`. Undo is a
   new entry.
3. **Never break the host tool.** `trail hook …` always exits 0, logs failures to
   `~/.trail/errors.log`, and prints nothing to stdout except a guard decision
   (block / ask first) when a secret is about to leave. `trail run` must run the
   wrapped command even if Trail itself is broken.
4. **Never lose user work.** Undo refuses when a file changed since, unless
   `--force`, and saves whatever it overwrites first.
5. **Zero dependencies.** Standard library only. One static binary per OS.
6. **Taste.** Output is calm and human: plain verbs, relative paths, one idea
   per line, no jargon in user-facing text.

## Layout

- `ledger/` — the engine (importable): store, entries + verify, objects, snapshots, diff, undo, restore
- `cmd/trail/` — the CLI: views, Claude Code hooks + installer, `run`/`begin`/`end`/`note`/`wrap`,
  `watch.go` (folder watcher + autostart), `recover.go` (deleted/history/restore, recovery hints),
  `update.go` (self-update from the latest GitHub release), `guard.go` (guard + sent log),
  `leaks.go` (leaked secrets + how to cancel them), `net.go` (which apps talk to which sites, blocking)
- **Never store a secret's value** in the ledger, logs or pending files: kind, masked preview and hash only.
- `install.ps1` / `install.sh` — one-line installers; they download release assets by name, so keep
  asset names `trail-<windows|macos|linux>-<amd64|arm64>[.exe]`
- The watcher must never record a change twice: it skips changes another entry already recorded.
- `SPEC.md` — the ledger format

## Checks before committing

```sh
gofmt -l . && go vet ./... && go test ./...
GOOS=windows go build -o /dev/null ./cmd/trail && GOOS=darwin go build -o /dev/null ./cmd/trail
```
