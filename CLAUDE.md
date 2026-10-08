# trail — notes for Claude Code

Trail records every change made on a person's behalf as a signed, hash-chained
timeline with undo. SPEC.md is the foundation; the code implements it.

## Principles (don't break these)

1. **The format is the product.** Any change to the entry format, hashing,
   signing or chain rules needs a SPEC.md update and a `v` bump. Readers must
   keep verifying old entries.
2. **Append only.** Never rewrite, truncate or reorder `ledger.jsonl`. Undo is a
   new entry.
3. **Never break the host tool.** `trail hook …` always exits 0, never prints to
   stdout, and logs failures to `~/.trail/errors.log`. `trail run` must run the
   wrapped command even if Trail itself is broken.
4. **Never lose user work.** Undo refuses when a file changed since, unless
   `--force`, and saves whatever it overwrites first.
5. **Zero dependencies.** Standard library only. One static binary per OS.
6. **Taste.** Output is calm and human: plain verbs, relative paths, one idea
   per line, no jargon in user-facing text.

## Layout

- `ledger/` — the engine (importable): store, entries + verify, objects, snapshots, diff, undo
- `cmd/trail/` — the CLI: views, Claude Code hooks + installer, `run`/`begin`/`end`/`note`/`wrap`
- `SPEC.md` — the ledger format

## Checks before committing

```sh
gofmt -l . && go vet ./... && go test ./...
GOOS=windows go build -o /dev/null ./cmd/trail && GOOS=darwin go build -o /dev/null ./cmd/trail
```
