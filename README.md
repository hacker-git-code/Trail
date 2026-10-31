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

## What's new in 0.3: keep your data in

AI agents send everything they read to their model's servers, and other apps
talk to sites you never see. 0.3 shows you what leaves your computer, stops
secrets from leaving, and tells you how to cancel any that already did.

- **See what AI agents send out.** Every file and command output Claude Code
  reads goes to Anthropic. `trail sent` lists all of it, turn by turn, with any
  keys or passwords inside flagged.
- **The guard stops leaks before they happen** (on by default). An agent can't
  read `.env` files, SSH keys or other credential files. It has to ask before
  reading a file that holds a key, printing your environment variables or
  uploading data. A web request or MCP call carrying a secret is blocked, and so
  is a prompt where you pasted a live API key by mistake.
  `trail guard allow <file>` when you mean it.
- **Cancel what leaked.** Sent data can't be pulled back, but a cancelled key is
  useless. `trail leaks` lists every secret that left, where it went, and the
  exact page to cancel it on (Anthropic, OpenAI, GitHub, AWS, Stripe, Google…).
- **See which app talks to which site** — any app, not just AI. `trail net on`
  records it, flags AI services and new destinations, and
  `trail net block <site.com>` / `<app.exe>` cuts one off. (Traffic is encrypted:
  Trail sees who connects where, not what's inside. For connected AI agents,
  `trail sent` has the content.)

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
trail install claude                           # Claude Code: who, why, what it sends — and the guard
trail net on                                   # record which apps talk to which sites
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

| Your data | |
|---|---|
| `trail sent` | everything AI agents sent off your computer, and where it went |
| `trail leaks` | secrets that left, where, and how to cancel each one. `trail leaks fixed <n>` once done |
| `trail guard` | what was stopped. `on` / `off`, `allow <file>`, `disallow <file>` |
| `trail net` | which apps talk to which sites. `on` / `off`, `now` for a live look |
| `trail net block <site.com\|app.exe>` | cut one off (sites: every OS · apps: Windows). `unblock`, `blocked` |

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

## Honest limits (v0.3)

- **Encrypted traffic stays encrypted.** For other apps, Trail records which app
  connected to which site, not what was sent. Only agents connected through
  Trail (Claude Code today) have their content recorded and guarded.
- **Secret detection is pattern-based.** It knows the shapes of common keys
  (Anthropic, OpenAI, GitHub, AWS, Stripe, Razorpay, Google, Slack, SendGrid,
  private keys, JWTs, database URLs) and flags likely passwords. It can miss a
  secret with no recognisable shape.
- **Sent data can't be unsent.** Trail tells you how to cancel what leaked; it
  can't delete a copy from someone else's servers.
- **Blocking needs admin rights.** Run the terminal as administrator (Windows)
  or use sudo. Blocking an app is Windows-only for now.
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

## Next

- Which app made each change, for watched folders
- The guard and sent log for Codex, Cursor and other agents
- Pausing an agent before it deletes many files
- Key in the OS keychain / Secure Enclave / TPM
- **The timeline app** — scroll back, see what changed, tap to undo

## License

MIT
