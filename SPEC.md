# Trail Ledger Format — v1

Trail is a record of every change made on your behalf — by an AI agent, a
script, or you — that can be verified and reversed. This document is the
foundation. The `trail` CLI is one implementation of it; anything that follows
these rules can write to, read, verify, or render a Trail ledger.

The format has one job: **make it impossible to quietly rewrite history.**
It does not promise that nobody can break into the machine. It promises that if
anyone edits, removes, or reorders a past record, verification will show it.

---

## 1. Layout on disk

A ledger lives in one directory (default `~/.trail`, override with `TRAIL_HOME`):

```
ledger.jsonl     the ledger: one entry per line, append-only
objects/ab/cd…   content store: every file version, addressed by its hash
key              this device's Ed25519 private key seed (hex, mode 0600)
key.pub          the matching public key (hex)
config.json      local settings (not part of the ledger)
pending/ index/ sessions/   scratch state for writers (not part of the ledger)
```

Only `ledger.jsonl` and `objects/` are the ledger. Everything else is local
working state and can be rebuilt or discarded.

## 2. Entries

Each line of `ledger.jsonl` is one entry:

```json
{"id":"<64 hex>","sig":"<base64>","body":{ ... }}
```

- `body` is a JSON object (section 3).
- `id` is the lowercase hex **SHA-256 of the exact bytes of `body`** as they
  appear on the line. Readers MUST hash the raw bytes, never a re-encoding.
- `sig` is the standard base64 **Ed25519 signature over the 32-byte SHA-256
  digest** (not over the hex string).

Lines are separated by `\n`. A writer appends a whole line in a single write.

## 3. Body

| field     | type     | meaning |
|-----------|----------|---------|
| `v`       | int      | format version, `1` |
| `seq`     | int      | position in the ledger, starting at `0`, no gaps |
| `prev`    | string   | `id` of the previous entry; `""` for `seq` 0 |
| `time`    | string   | RFC 3339 UTC timestamp, set by the writer at append time |
| `key`     | string   | first 16 hex chars of SHA-256(public key) |
| `actor`   | object   | who did it (section 3.1) |
| `action`  | object   | what was done (section 3.2) |
| `why`     | string?  | the intent, in the actor's or user's words |
| `root`    | string?  | the folder the action was scoped to |
| `changes` | array?   | file changes (section 4) |
| `note`    | string?  | anything a reader should know (e.g. "not snapshotted") |
| `recovery`| string?  | for actions Trail can't reverse (an email sent): how a person can |
| `dest`    | string?  | where data went (an AI provider, a server, a site) |
| `sent`    | array?   | what left the computer (section 4.1) |
| `secrets` | array?   | credentials found in what was sent or blocked (section 4.1) |

### 3.1 actor

```json
{"kind":"agent","name":"claude-code","session":"…","host":"…"}
```

`kind` is `agent`, `human`, `system`, or `unknown` (a change seen by a folder
watcher, where the program that made it isn't known). `name` is the tool or person.

### 3.2 action

```json
{"type":"tool","tool":"Edit","summary":"edited pricing.md","command":"…","ref":"…","failed":false,"exit":0}
```

| `type`     | meaning |
|------------|---------|
| `init`     | the genesis entry. MUST be `seq` 0. Carries `pubkey` (base64 Ed25519 public key). |
| `tool`     | an agent used a tool (edit a file, run a shell command, …) |
| `run`      | a wrapped command ran from start to finish |
| `undo`     | a previous entry was reversed. `ref` is that entry's `id`. |
| `observed` | a folder watcher saw files change. Who changed them is not known. |
| `sent`     | data left the computer: files and outputs an AI agent sent to its model. `sent` lists them. |
| `guard`    | something was stopped (or paused for the person to decide) before it could leak a secret, or a site/app was blocked. |
| `network`  | apps connected to sites for the first time. Each `sent` item's `source` reads `app → host`. |
| `resolved` | a leaked secret was cancelled. `ref` is that secret's `hash`. |
| `restore`  | files were put back to saved versions (a deleted file brought back, a file rolled back). |
| `external` | something happened outside the file system (email sent, API called). Recorded, not reversible; `recovery` says how a person can reverse it. |

Unknown types MUST be preserved and displayed, not rejected.

## 4. Changes and objects

```json
{"path":"/abs/path/file.md","op":"modify","before":"<sha256>","after":"<sha256>",
 "before_mode":420,"after_mode":420,"added":12,"removed":3}
```

- `op` is `create`, `modify`, or `delete`.
- `before` / `after` are SHA-256 hex hashes of the full file content.
  An empty or missing hash means "the file did not exist".
- `*_mode` are Unix permission bits.
- `added` / `removed` are line counts, for display only. `binary` marks binary files.

Every hash referenced by a change MUST exist in `objects/`, stored at
`objects/<first 2 hex>/<remaining 62 hex>`, zlib-compressed. The hash is
of the **uncompressed** content, so readers can check every object they open.

### 4.1 Sent data and secrets

```json
"sent":    [{"source":"/abs/path/.env","bytes":512,"hash":"<16 hex>","secrets":[ ... ]}],
"secrets": [{"kind":"Stripe key","masked":"sk_liv…ab12","hash":"<16 hex>"}]
```

- A `sent` item's `source` is an absolute path for a file, or a description
  ("output of `printenv`"). `hash` is the first 16 hex of SHA-256 of what was
  sent; the content itself is not stored.
- A secret's value is **never** written to the ledger. `masked` shows at most
  its first 6 and last 4 characters; `hash` (first 16 hex of SHA-256 of the
  value) lets a reader recognise the same secret again.

## 5. The chain

An entry is valid when all of these hold:

1. `id == hex(SHA-256(body bytes))` — the entry has not been edited.
2. `sig` verifies against the public key from the genesis entry.
3. `seq` equals its line position (0, 1, 2, …) — nothing removed or inserted.
4. `prev` equals the `id` of the line before it — nothing reordered.

Because each `id` covers `prev`, changing any past entry changes its id and
breaks every entry after it. To make even a full re-signing of history
detectable, publish the current head (`trail head`) somewhere outside the
device; any later rewrite before that point will no longer match.

## 6. Undo

**Nothing is ever deleted from the ledger.** Undo is a new entry:

- type `undo`, `ref` = the entry being reversed
- its `changes` move each file from its current state back to the `before`
  state of the referenced entry

A writer MUST refuse to undo when a file's current content no longer matches
the referenced entry's `after` hash (someone changed it since), unless the
user explicitly forces it. Undoing an `undo` entry is a redo.

## 7. Rules for writers

- Append only. Never rewrite or truncate `ledger.jsonl`.
- Take an exclusive lock (`lock` file, created with O_EXCL) around reading the
  head and appending.
- Store every object a change references **before** appending the entry.
- When integrated into another tool (an agent hook), never block or break the
  host tool. If recording fails, log it and let the host continue.

## 8. Versioning

Any change to sections 2–6 requires bumping `v`. Readers MUST verify entries
of versions they understand and report, not discard, versions they don't.

Adding an optional body field or a new `action.type` / `actor.kind` value is
not a breaking change and keeps `v` at 1. Readers MUST ignore body fields they
don't know (the `id` covers the raw bytes, so verification is unaffected) and
display unknown types as-is. `recovery`, `observed`, `restore` and `unknown`
were added this way in trail 0.2; `dest`, `sent`, `secrets` and the types
`sent`, `guard`, `network` and `resolved` in trail 0.3.
