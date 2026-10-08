# Bright Lantern 🔆🏮

Search and read your coding agent sessions, from
[pi](https://github.com/badlogic/pi-mono) and from
[Claude Code](https://claude.com/claude-code). Named for *Ctenoscopelus*, the
bright lanternfish, which carries its own light through deep water.

Formerly spireweb.

Local semantic search via [sqlite-vec](https://github.com/asg017/sqlite-vec) and
[sqlite-lembed](https://github.com/landrix/sqlite-lembed/), combined with FTS5
keyword search by reciprocal rank fusion.

## Install

```bash
brew install llimllib/tap/brightlantern
```

Then open **Bright Lantern** from Applications. The first launch finds your
sessions, indexes them behind the page, and registers a background agent that
keeps the index current at login with no window open. The window is a view
onto that agent, which also serves <http://127.0.0.1:5268> to any browser.
`brightlantern` is on PATH too; `brightlantern help` lists its commands.

macOS on Apple Silicon. The app carries the embedding model and the search
extension, so nothing else is downloaded.

Bright Lantern finds your sessions itself, looking in `$CLAUDE_CONFIG_DIR/projects`,
`~/.config/claude/projects`, `~/.claude/projects`, and `~/.pi/agent/sessions`,
and indexes everything it finds. What it found is written to
`~/.config/brightlantern/config.toml` on the first run, so that installing another
agent later does not silently change what is indexed. Edit that file, or pass
`--dir` (repeatable), to choose differently.

After a while away, semantic search takes about fifteen seconds to arrive while
macOS compiles the embedding model's Metal shaders. The page and keyword search
work in the meantime.

## Run at login

The app does this itself; its switch in System Settings is under Background
App Activity. Without the app -- built from source, say -- the CLI can install
the agent instead:

```bash
brightlantern                    # once, from a terminal: finds your sessions
brightlantern service install    # then keep it running
```

That installs a LaunchAgent, so the index stays current and search stays warm
with no terminal open. Logs are in `~/Library/Logs/brightlantern/`.
`brightlantern service status` says what it is doing, and `service uninstall`
removes it. After rebuilding, `service restart` picks up the new binary.

Typing `brightlantern` while the agent runs says it is already running rather
than starting a second copy, and `brightlantern index` declines to write the
same index unless given `--force`.

## Titles

Sessions are listed under a title written by Apple's on-device model: no key,
no bill, nothing leaves the machine. It needs Apple Intelligence turned on;
without it, each session is listed under its opening message instead.
`titles = "off"` in the config file does that on purpose, which is what Bright
Lantern did for its first five milestones.

## The archive

The index keeps every message of every session as its agent wrote it, so a
session stays readable and searchable after its file is gone. `brightlantern info`
says where it is.

`brightlantern merge other.db` folds another machine's index into this one. Sessions
are matched by id and the longer copy wins; titles come with them, so they are
not paid for twice. A session whose two copies disagree is reported and left
alone.

It is a SQLite database, and `tool_calls` is a view over every tool call either
agent made:

```sql
-- every session where you ran a command, newest first
SELECT s.project, t.at, json_extract(t.arguments, '$.command')
FROM tool_calls t JOIN sessions s ON s.id = t.session_id
WHERE lower(t.name) = 'bash'
  AND json_extract(t.arguments, '$.command') LIKE 'gh pr create%'
ORDER BY t.at DESC;

-- which files were edited most
SELECT COALESCE(json_extract(arguments, '$.path'),
                json_extract(arguments, '$.file_path')) AS file, COUNT(*)
FROM tool_calls WHERE lower(name) = 'edit'
GROUP BY file ORDER BY 2 DESC LIMIT 20;
```

The session id opens in the browser at `/sessions/<id>`.

## Working on it

```bash
mise run setup    # embedding model + sqlite-lembed, once
mise run index    # build the search index
mise run dev      # http://localhost:5269, reading the index the agent keeps
mise run check    # vet, lint, typecheck, gofmt, test
mise run app      # build/Bright Lantern.app, a window onto the server
```

The app is a window onto the server, not the server itself. Quitting it
leaves the LaunchAgent running, which is the point: the index stays current
and search stays warm whether or not a window is open. To stop the service,
use `brightlantern service uninstall`. `open "build/Bright Lantern.app" --args
--url http://127.0.0.1:5269/` points it at `mise run dev`.

See [AGENTS.md](AGENTS.md) for the things that cost somebody an hour.

## Status

Under construction; see the [milestones](https://github.com/llimllib/brightlantern/milestones).
Browsing, search, live indexing, titles, both session formats, and the archive
work.
