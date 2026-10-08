# trail

**Every change made on your behalf, on a timeline you can undo.**

AI agents now edit your files, run commands, and act for you. Each one keeps
its own half-memory of what it did, or none at all. Trail is one record under
all of them: every change, who made it, why, the exact diff, and a way back.
The record is signed and hash-chained, so history can't be quietly rewritten.

```
$ trail log
Thu 8 Oct 2026
  21:59  ● f9e72cb9  claude-code   Create CHANGELOG.md with one line  +1
                              why  change the Pro price to 19 and note it in the changelog
  21:59  ● 7580c97d  claude-code   edited pricing.md  +1 −1
  21:41  ● 3affae0d  codex         ran codex  3 files +48 −12

$ trail undo
↺ Undid f9e72cb9 · Create CHANGELOG.md with one line
  1 file put back.  Changed your mind? trail undo d365ed47
```

One small Go binary. No dependencies, no daemon, no account, nothing leaves
your machine.

## Install

```sh
git clone https://github.com/hacker-git-code/trail && cd trail
go build -o trail ./cmd/trail        # then move `trail` somewhere on your PATH
```

Or use a prebuilt binary: `trail-windows-amd64.exe`, `trail-macos-arm64` (Apple
silicon), `trail-macos-amd64`, `trail-linux-amd64`. Rename it to `trail` and put
it on your PATH.

## Connect your tools

**Claude Code** — every edit, and every file a shell command changes, with the prompt that asked for it:

```sh
trail install claude              # all projects (~/.claude/settings.json)
trail install claude --project    # just this repo (.claude/settings.json)
trail install claude --with-mcp   # also record actions through MCP tools (recorded, not reversible)
```

**Any other CLI** — Codex, Aider, Gemini CLI, OpenCode, your own scripts:

```sh
trail wrap add codex aider gemini
# then add the one line it prints to your shell config (bash, zsh, fish or PowerShell)
```

After that, typing `codex` runs Codex exactly as before, and the changes it
made land on the timeline as one entry per run.

**One-off** — `trail run -- <any command>`

**Your own tool** — bracket any piece of work:

```sh
T=$(trail begin --agent my-bot --why "nightly cleanup")
...do the work...
trail end $T --summary "removed stale caches"
trail note --agent my-bot "emailed the weekly report"   # actions outside the file system
```

## Use it

| | |
|---|---|
| `trail log` | the timeline, newest first. `--here` for this project, `--agent codex`, `-n 50`, `--json` |
| `trail show <id>` | who, when, why, the command, and the exact diff |
| `trail undo` | reverse the latest change. Run it again to keep walking back |
| `trail undo <id>` | reverse a specific change. Undoing an undo is a redo |
| `trail verify` | prove nothing was edited, removed or reordered |
| `trail head` | the current head — post it somewhere public as an anchor |
| `trail status` | what's connected |

Undo never overwrites work done after the change. If a file was edited
since, Trail tells you by whom and stops. `--force` overrides, and is still
safe: the overwritten version is saved and the undo can itself be undone.

## How it works

- **Before** an agent acts, Trail saves the current state: the one file an
  edit tool names, or the whole project folder for a shell command (fast
  after the first time — unchanged files aren't re-read).
- **After**, it saves the new state and appends one entry to
  `~/.trail/ledger.jsonl`: who, what, why, and the before/after of every file.
- Every file version is stored once in `~/.trail/objects/`, by its SHA-256.
- Every entry includes the hash of the one before it and is signed with this
  device's Ed25519 key. Change anything in the past and `trail verify` shows
  exactly where.
- Undo is a new entry, never an edit. The history only grows.

The format is fully specified in [SPEC.md](SPEC.md). Anything that follows it
can write to, verify, or display a Trail ledger.

## Honest limits (v0.1)

- **Tamper-evident, not tamper-proof.** Nobody can quietly rewrite history,
  but someone with full access to this machine *and* its key could rewrite all
  of it consistently. `trail head` + posting it somewhere you don't control
  closes that gap. The key lives in `~/.trail/key` for now; moving it into the
  OS keychain / secure enclave is next.
- **It sees what it's connected to.** Shell commands are tracked inside the
  project folder (the git repo, or the current folder). Changes outside it
  aren't. Commands run from your home folder are recorded but not snapshotted.
- **Parallel shell commands** in the same project can be attributed together.
- **Wrapped interactive CLIs** are recorded as one entry per session, not per step.
- Files over 10 MB, and `.git`, `node_modules`, virtualenvs and build caches,
  aren't tracked. Add your own patterns to a `.trailignore`.
- External actions (an email sent, an API called) are recorded, not reversed.

## Next

- `trail watch` — record any app, not just ones with hooks
- An MCP server so agents can read the timeline and explain themselves
- Key in the OS keychain / Secure Enclave / TPM
- **The timeline app** — scroll back, see what changed, tap to undo

## License

MIT
