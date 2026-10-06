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
brightlantern
```

That serves on <http://127.0.0.1:8080>, building the index behind the page if
there is not one yet. `brightlantern help` lists the other commands.

macOS on Apple Silicon. The cask carries the embedding model and the search
extension alongside the binary, so nothing else is downloaded.

`brightlantern` finds your sessions itself, looking in `$CLAUDE_CONFIG_DIR/projects`,
`~/.config/claude/projects`, `~/.claude/projects`, and `~/.pi/agent/sessions`,
and indexes everything it finds. What it found is written to
`~/.config/brightlantern/config.toml` on the first run, so that installing another
agent later does not silently change what is indexed. Edit that file, or pass
`--dir` (repeatable), to choose differently.

The first search after a while takes about fifteen seconds while macOS compiles
the embedding model's Metal shaders. It says so when it happens.

## Titles

Sessions are listed under a generated title, falling back to their opening
message. Two backends, set with `titles` in the config file or `--titles-via`:

- `api` needs `ANTHROPIC_API_KEY`. The default.
- `claude` shells out to the Claude Code CLI, which bills whatever subscription
  it is signed in to and shares a rate limit with your interactive sessions. A
  run over a large corpus asks first. `--titles N` caps it, for trying it out.

`titles = "off"` lists every session under its opening message, which is what
Bright Lantern did for its first five milestones.

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
mise run dev      # http://localhost:8080
mise run check    # vet, lint, typecheck, gofmt, test
```

See [AGENTS.md](AGENTS.md) for the things that cost somebody an hour.

## Status

Under construction; see the [milestones](https://github.com/llimllib/brightlantern/milestones).
Browsing, search, live indexing, titles, both session formats, and the archive
work.
