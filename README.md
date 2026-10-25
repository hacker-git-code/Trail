# trail

**Undo for everything AI does on your computer.**

AI agents now edit your files, run commands, and act for you. Each one keeps
its own half-memory of what it did, or none at all. Trail is one record under
all of them: every change, who made it, why, the exact diff, and a way back.
The record is signed and hash-chained, so history can't be quietly rewritten.

```
$ trail log
Sun 25 Oct 2026
  14:52  ◆ a8d7047a  claude-code   gmail · send email  can't be undone
                              fix  Can't be unsent. Follow up with the recipient.
  14:52  ● e9070879  claude-code   edited ideas.md  +1
                              why  tidy my notes
  14:51  ● 34352d8b  unknown app   4 files changed in Documents  4 files +3 −3

$ trail deleted
  25 Oct 14:51  D  ~/Documents/taxes/2025.txt  by unknown app · 34352d8b
  25 Oct 14:51  D  ~/Documents/taxes/2026.txt  by unknown app · 34352d8b

$ trail restore ~/Documents/taxes
↩ Restored 2 files in taxes
```

One small Go binary. No dependencies, no account, nothing leaves your machine.
Website: [traillayer.framer.website](https://traillayer.framer.website)

## What's new in 0.2

Built from the replies to the launch post:

- **Deleted files come back, inside git or not.** `trail watch add ~/Documents`
  keeps a copy of a folder and records every change to it, from any app or AI,
  connected or not. `trail deleted` lists what's gone; `trail restore` brings
  back a file or a whole folder.
- **No per-tool setup needed.** The watcher covers every AI on the machine.
  Connected tools (Claude Code, wrapped CLIs) still add who and why.
- **A clear label for what can't be undone.** A file diff can't unsend an
  email. Those actions now say *can't be undone* and come with a recovery
  step: "Follow up with the recipient", "Delete it in github", "Look in drive's trash".
- **Roll one file back.** `trail history <file>` shows every saved version;
  `trail restore <file> --before <id>` puts back how it was before a change.

## Install

**Windows** (PowerShell):
```powershell
irm https://raw.githubusercontent.com/hacker-git-code/trail/main/install.ps1 | iex
```

**macOS / Linux:**
```sh
curl -fsSL https://raw.githubusercontent.com/hacker-git-code/trail/main/install.sh | sh
```

No Go, no PATH editing: it downloads the latest release, checks it runs, and puts
it on your PATH. Run it again any time to update; it replaces your existing copy
in place.

**Already installed?** `trail update` gets the latest version (from 0.2.1 on).
Your history in `~/.trail` is never touched, and a running watcher is restarted
on the new version.

Or download a binary from [releases](https://github.com/hacker-git-code/trail/releases/latest),
or build it: `go build -o trail ./cmd/trail`

## Set it up

```sh
trail watch add ~/Documents ~/Desktop ~/code   # cover the folders that matter, from any app
trail watch autostart on                       # keep watching in the background, from every login
trail install claude                           # Claude Code: adds who and why to every change
trail wrap add codex aider gemini              # any other CLI (paste the one line it prints)
```

## Use it

| | |
|---|---|
| `trail log` | the timeline, newest first. `--here`, `--agent codex`, `-n 50`, `--json` |
| `trail show <id>` | who, when, why, the command, and the exact diff |
| `trail undo` | reverse the latest change. Run it again to keep walking back |
| `trail undo <id>` | reverse one change. Undoing an undo is a redo |
| `trail deleted` | every deleted file Trail can bring back |
| `trail restore <file\|folder>` | bring back a deleted file, or everything missing in a folder |
| `trail history <file>` | every saved version of a file |
| `trail restore <file> --before <id>` | put a file back to how it was before a change |
| `trail verify` | prove nothing was edited, removed or reordered |
| `trail head` | the current head — post it somewhere public as an anchor |
| `trail status` | what's connected and watched |
| `trail update` | get the latest version (`--check` to only look) |

| Watching | |
|---|---|
| `trail watch add <folder>` | keep a copy of a folder; prints how much space the first copy takes |
| `trail watch` | run the watcher in this terminal |
| `trail watch autostart on\|off` | run it in the background from every login (Windows, macOS, Linux) |
| `trail watch list` / `stop` / `rm <folder>` | see, stop, or stop watching a folder |

| Connecting | |
|---|---|
| `trail install claude` | Claude Code. `--project` for one repo, `--with-mcp` for MCP actions too |
| `trail wrap add <cli>` | any CLI; one entry per run |
| `trail run -- <cmd>` | one command |
| `trail begin` / `trail end <token>` | bracket work from your own tool or script |
| `trail note "text" --recover "how to fix"` | record an action outside your files |

Undo and restore never lose work. If a file changed after the entry you're
undoing, Trail says by whom and stops (`--force` overrides). Whatever a restore
or undo overwrites is saved first, so it can itself be undone.

## How it works

- **Watched folders:** Trail saves a first copy of every file, then rescans
  every few seconds (unchanged files are only checked, not re-read). Changes
  are grouped into one entry per burst. Because the old version was saved
  *before* anything happened, a deleted or overwritten file can always come back.
- **Connected tools:** before an agent acts, Trail saves the file it names (or
  the project, for a shell command); after, it records the change with the
  prompt that asked for it. The watcher skips changes a connected tool already
  recorded, so nothing shows up twice.
- Every file version is stored once in `~/.trail/objects/`, by its SHA-256, compressed.
- Every entry includes the hash of the one before it and is signed with this
  device's Ed25519 key. Change anything in the past and `trail verify` shows where.
- Undo and restore are new entries, never edits. The history only grows.

The format is specified in [SPEC.md](SPEC.md). Anything that follows it can
write to, verify, or display a Trail ledger.

## Honest limits (v0.2)

- **Tamper-evident, not tamper-proof.** Nobody can quietly rewrite history,
  but someone with full control of this machine *and* its key could rewrite
  all of it. Posting `trail head` somewhere you don't control closes that gap.
  The key is a file in `~/.trail/key` for now.
- **The watcher knows what changed, not who.** Its entries say "unknown app".
  Connected tools add the who and why.
- **Changes between two scans** (a file written and deleted within a few
  seconds) can be missed. Files that existed at the last scan are always safe.
- **Watched folders cost disk space** for the first copy (compressed, stored once).
  Files over 10 MB, `.git`, `node_modules`, virtualenvs and caches are skipped;
  add your own patterns to a `.trailignore`.
- **Sent data can't be unsent.** External actions are recorded with a recovery
  step, not reversed.

## Next

- Which app made each change, for watched folders
- A guard that pauses an agent before it reads secrets or deletes many files
- Key in the OS keychain / Secure Enclave / TPM
- **The timeline app** — scroll back, see what changed, tap to undo

## License

MIT
