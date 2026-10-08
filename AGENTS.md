# AGENTS.md

Non-obvious things about this repository. Everything here cost somebody an hour.

## Name

**Bright Lantern**, command `brightlantern`, formerly **spireweb** (#81, #83).
The emoji are 🔆🏮; it is named for *Ctenoscopelus*, the bright lanternfish.
Bundle identifier, when M14 needs one: `org.billmill.brightlantern`, from
brightlantern.billmill.org. It needs to be a domain owned, not one that
resolves, and must never change: Gatekeeper, SMAppService and privacy
permissions all attach to it.

"spireweb" survives on purpose in three places, and nowhere else:

- `index.LegacyPath`, `~/Library/Caches/spireweb/index.db`, which is where
  v0.0.2 and earlier kept the index and is what gets moved from.
- `internal/index/testdata/README.md`, which says how v0.0.1 -- whose binary
  was `./cmd/spireweb` -- built the fixture.
- Measurements of the corpus taken before the rename, below.

Settings and the model in `~/.config/spireweb` and `~/.local/share/spireweb`
are not migrated: there was one install, and it was moved by hand.

## Build

```bash
mise run setup    # once: embedding model + sqlite-lembed (~1 min)
mise run check    # vet, lint, typecheck, gofmt, test -- run before committing
mise run dev      # server + tsc, both watching
```

`GOFLAGS=-tags=sqlite_fts5` lives in `mise.toml`'s `[env]`. mattn/go-sqlite3
omits FTS5 without it and the schema fails at runtime with "no such module:
fts5". Because it is in the environment, plain `go test` works too.

`pnpm` uses mise's npm backend; the default aqua backend fails pnpm's GitHub
attestation check. Its version is pinned equal to `package.json`'s
`packageManager`, or pnpm re-downloads that version on every invocation.

`mise run test` depends on `ts`: the web package embeds
`internal/web/static`, and `app.js` is generated, not committed.

CI runs on `macos-26`, the release runner's image (#91), and builds and tests
the app as well. Its virtual GPU does run llama.cpp's Metal backend, but a
fresh VM pays for every model load: with the model installed, `internal/index`
took 162s against 34s without. So CI installs the model only after `check`, and
the semantic tests skip there; `mise run check` runs them on a real GPU.

`setup` deletes its build tree when it finishes: building the fork leaves
473MB behind to produce a 3.2MB dylib, and a rebuild from nothing is a minute.
`BRIGHTLANTERN_KEEP_SRC=1` keeps it for working on the extension itself.

## sqlite3.h

`sqlite-vec`'s cgo bindings compile with `-DSQLITE_CORE` and `#include
"sqlite3.h"`, taking struct layouts from whatever header the preprocessor
finds -- which is not the library being linked, that being go-sqlite3's
amalgamation and newer than either the macOS SDK's header or debian's. The
header was an unpinned input that varied per machine, and that class of
mismatch corrupts structs rather than failing to compile.

`mise run sqlite-header` copies the header go-sqlite3 ships for its own
amalgamation into `third_party/sqlite/`, and `CGO_CFLAGS` points there. It is
derived from `go.mod`, so bumping go-sqlite3 restages it. `build`, `test`, and
`lint` depend on it, and a test asserts the staged header's version equals
`sqlite_version()`. Nothing needs `libsqlite3-dev`, including CI.

## sqlite-lembed

Must be the **landrix fork** (`mise run setup` verifies). Upstream
v0.0.1-alpha.8 kills the process rather than returning errors: SIGSEGV on input
over 512 tokens, SIGABRT on invalid UTF-8.

**A connection whose model failed to register will segfault, not error.**
`lembed_model_from_file` returns null when llama.cpp cannot load the model, the
vtab rejects it with a bare "SQL logic error", and a later `lembed()` call
dereferences the null model. A crash inside cgo cannot be recovered. So a
registration failure must always fall back to lexical-only; never continue on
that connection.

The usual cause of that failure is no reachable GPU, not a bad file. On macOS
llama.cpp needs a Metal device, which a sandbox lacks. GitHub's macos-26
runners have one, virtual and slow -- see "Build".
The extension still loads and `lembed_version()` still answers, so the only
honest test of "is semantic search working" is opening a connection.

**The first connection compiles llama.cpp's Metal shaders: about 15 seconds**,
and it is not paid once. macOS caches the result under
`/var/folders/.../C/com.apple.metal` and evicts it on its own schedule; there
were three generations a week apart on one machine. It is caused by
`-DGGML_METAL_EMBED_LIBRARY=ON`, which is also what makes the dylib relocatable
and therefore shippable. `noteSlowModelLoad` says so after a two second grace
rather than before every load, because the warm case is overwhelmingly common.
`serve` pays it behind the page instead: it answers on the lexical driver and
`warm` swaps semantic search in once the model loads. Removing the cost means
building with the flag off and shipping a precompiled `.metallib`, which needs
the Metal compiler -- full Xcode plus its separately downloaded Metal
toolchain, not Command Line Tools -- and only removes half of it: the
pipeline-state cache is keyed by GPU driver and recompiles regardless (#56).

## Connections

- **Writer: one connection** (`SetMaxOpenConns(1)`). lembed's `llama_context`
  is not safe for concurrent use; two goroutines embedding through the *same*
  connection segfault. Uses `_txlock=immediate`, because indexing reads before
  it writes and lock upgrades fail rather than wait.
- **Concurrent embedding on *different* connections is fine.** Each has its
  own model and context. This matters because the background indexer embeds
  chunks while handlers embed queries; there is a test that runs seven
  connections at once, and no process-wide lock is needed.
- **Each context costs ~30MB.** `serve` is 151MB idle and ~270MB once the
  reader pool is open, almost all of it the five loaded models. Raising
  `ReaderConns` costs 30MB a connection, which is the reason it is 4 and not
  something larger.
- **vec0 is on every connection, lexical ones included.** It is compiled in
  and needs no GPU; only lembed needs the model. The lexical driver is the
  fallback when the model will not load, and it writes to indexes that have
  `chunks_vec`: deleting a chunk deletes its vector, and without vec0 that
  aborted the run with "no such module: vec0" and wedged it on the same file
  every run after (#82). It also lets `stats` and `doctor` count vectors
  without loading the model.
- **Readers: a pool**, `_query_only=true` — *not* `mode=ro`, which cannot
  create the `-shm`/`-wal` files WAL needs.
- **The model is registered per connection**, via the driver's `ConnectHook`.
  It lives in `temp.lembed_models`, which is connection-scoped, and
  `database/sql` recycles connections whenever it likes. Registering once after
  opening a pool leaves later connections modelless, and the symptom is silent:
  the ranker errors, gets skipped, and search degrades to keyword-only.

## Two session formats

`internal/session` parses pi's files and Claude Code's, told apart by the first
line: pi opens with a `{"type":"session"}` header, Claude Code has no header at
all. Detection is **per file**, not per directory, so one `--dir` may hold
either. Everything downstream consumes `*Session` and never sees bytes, which
is what keeps the difference to that one package.

Claude Code's shape differs in ways that are not a remapping:

- **Tool results have no role.** They are `user` records carrying `tool_result`
  blocks, and one record may carry several -- that is what parallel tool calls
  look like coming back. `Message.ToolCallID` is singular, so one record fans
  out into N messages, or `ToolResults()` finds one call's output and the rest
  render as missing.
- **Every fanned-out message carries the same `Raw`.** `archiveMessages` uses
  `COUNT(*)` as its resume point, so a message with no `Raw` leaves a gap that
  makes the count disagree with the message list on every later build.
- **`Raw` is the whole record**, unlike pi's, where it is the message.
  `toolUseResult` sits on the envelope and holds the `structuredPatch` an edit
  produced, so keeping only the message would make the archive lossy in exactly
  the way it exists to prevent.
- `message.content` is a bare string on older records, an array on newer ones.
- One assistant turn spans several records, so `n_msgs` is not comparable
  between agents.

`render.ParseDiff` takes either: pi's rendered `details.diff`, whose line
numbers are parsed back out of the text, or Claude Code's `structuredPatch`,
whose numbers come from walking the hunk. Tool names are normalized before the
`summaryArg` lookup -- Claude Code capitalizes its built-ins, and MCP tools
arrive as `mcp__<server>__<tool>`.

## SDK sessions are excluded

Claude Code writes a session file for **every SDK invocation**, not just for
what a person types. On the corpus that was 1600 of 1617 files and 283 of
284MB:

| entrypoint | files | what it is |
| --- | --- | --- |
| `sdk-cli` in `spireweb-titles-*` | 1215 | this tool's own title prompts |
| any `sdk-ts` | 252 | pi tunnelling through claude-bridge |
| `cli` | **17** | someone typing |

The bridge files duplicate the pi corpus, with the worse copy. The title files
were a **feedback loop** while titles could come from `claude -p`, which wrote
a session into the directory being indexed, which got indexed and titled, which
wrote another. That backend is gone (#91); the filter stays for the bridge.

`session.SkipReason` filters on `entrypoint`, which is present on every
message-bearing record across every version seen. **Whole file, not a prefix**:
94 bridge sessions open with one or two `cli` records before the bridge takes
over, the deepest at record 429. Matched as bytes rather than parsed, because
it runs over every candidate on a cold build.

It is checked in `Build` *after* the mtime test, so an unchanged indexed session
still costs a stat. The consequence is that a session indexed before it became
excluded survives until `--full`; excluded files are recorded as such, and
exclusion is the one thing the sweep deletes for.

## Directories and config

`BuildOptions.Dirs` is a slice. It cannot be one `Build` per directory:
`build.go` treats every indexed session it did not see as having no file, so
two builds would have each root demote the other's sessions to archive-only.
`Discover` takes them all and returns one deduplicated list.

With no `--dir`, `cmd` probes `$CLAUDE_CONFIG_DIR/projects`,
`~/.config/claude/projects`, `~/.claude/projects`, `~/.pi/agent/sessions` and
keeps those holding at least one `.jsonl`. Existence is not the test:
`~/.claude` survives as a home for settings after `CLAUDE_CONFIG_DIR` has moved
everything else. The candidates are deduplicated because they genuinely
overlap -- `CLAUDE_CONFIG_DIR` is usually `~/.config/claude`.

`index.Build` still falls back to pi's directory alone. Only `cmd` detects,
because a test indexing a fixture must not depend on the machine running it.

Precedence is flag, then `~/.config/brightlantern/config.toml`, then detection, and
**"was the flag given"** is the question rather than "does it differ from its
default" -- otherwise `--addr` with the default value could not override a file.
`resolve` is handed the set of flags the FlagSet saw.

A first run writes down what it detected, because detection's answer moves:
install pi to try it once and the corpus doubles. It records `titles = "apple"`:
on-device, so there is nobody to ask before using it.

XDG, not `~/Library/Application Support`, matching `BRIGHTLANTERN_DATA_DIR` and
`embed.DefaultPaths`.

## Where the index lives

`$XDG_DATA_HOME/brightlantern/index.db`, else `~/.local/share/brightlantern/index.db`,
beside the model. **Not a cache directory**, which is where v0.0.2 and earlier
kept it: macOS may empty `~/Library/Caches` under disk pressure, and cleanup
tools empty it on sight. That was harmless while the index was derived; the
archive and the titles are not.

An index at the old path is **moved**, on the first run that resolves the
default -- not rebuilt beside it, which would re-embed everything and look as
though it had all been lost. Only the default moves: `--db` and the settings
file's `index` are where someone wants it. `index.Relocate` never fails; it
leaves the index where it is and says why:

- **In use**: a `serve` from before the upgrade has it open, and renaming a
  database under an open connection splits it -- that process keeps writing
  to the moved file while anything opening the old path gets an empty one. The
  test is an exclusive lock, because in WAL mode every open connection holds a
  shared one for its whole life. **Not the `-wal` file**: Apple's SQLite keeps
  it after a clean close, so a database nobody has open can have one.
- **The rename failed**, e.g. across volumes. Copying instead would make a
  second 500MB index that silently diverges from the first.

`-wal` and `-shm` move with it, and a failure part way puts back what moved.
The cmd tests sandbox `XDG_DATA_HOME` and `XDG_CACHE_HOME` as well as `HOME`;
without that, a test resolving the default would move the real index.

## Storage

- `chunks.id` is `AUTOINCREMENT`. A plain `INTEGER PRIMARY KEY` reuses rowids
  freed by `DELETE`, and `chunks_vec` is keyed by chunk id, so a recycled id
  collides with a surviving vector.
- `chunks_fts` (external content) and `chunks_vec` (virtual table) do not
  participate in foreign-key cascades. Deleting a chunk must delete from both
  by hand, or search returns hits pointing at rows that are gone.
- Never `DELETE FROM sessions` to update one: `chunks.session_id` cascades and
  destroys the chunks the reindex meant to reuse. Upsert.
- `sessions.title` is excluded from the upsert's UPDATE list, so a reindex does
  not clobber a generated title. Sessions are append-only and get reindexed on
  every new message.
- Identity is `sessions.id`, a UUID from pi's header. `path` and `host` are
  machine-local metadata, so indexes from different machines can be merged.
- Chunks must stay under `session.MaxTokens`. Character limits cannot predict
  this: 800 chars of prose is 162 tokens, 800 chars of dense JSON is 802.
- `Chunk` splits only on rune boundaries. Invalid UTF-8 crashed the extension.

### The archive

`messages` holds every message of every session as its agent wrote it, and is
the one table that is not derived from something else: chunks, vectors, and titles can
all be rebuilt from it, and it cannot be rebuilt from them. `brightlantern doctor`
checks it separately for that reason.

`content` is JSON **text, verbatim and uncompressed**, and both halves are
load-bearing. Verbatim because the corpus already contains a `bashExecution`
role nothing in this repo handles, so re-marshalling `session.Message` would
silently drop fields. Uncompressed because tool calls and their arguments live
in there, and `json_each` over them is the point of having an archive at all;
compressing `toolResult` would save ~150MB and cost that.

The key is `(session_id, idx)` -- a UUID from pi and a position in an
append-only file. Both survive a merge, which no autoincrement does.

`session.ParseWithRaw` is what fills it. Plain `Parse` does not keep raw bytes,
because they are a second copy of the file and the server caches eight parsed
sessions at a time.

It costs, measured on the corpus:

| | before | after |
| --- | --- | --- |
| lexical index | ~30MB | **424MB** |
| cold build | ~5s | ~9.5s |
| one changed session | ~55ms | ~140ms |

Writes are incremental the same way chunk reuse is: the stored count is the
starting point, so a session that gained a message inserts one row. An index
built before the table existed is backfilled by promoting the run to a full
pass, exactly as `needsVectors` does, because mtime and size will never change
again on an old session.

`index --full` is cheaper than it sounds and costs nothing in money. The
session upsert's `DO UPDATE` list omits `title`, `title_msgs` and `title_key`,
and `TitleCandidates` keys on `title_msgs <> n_msgs` -- so a reindex of an
unchanged file re-titles nothing. `reusableChunks` matches on chunk *content*,
not on mtime, so unchanged chunks keep their row ids and their vectors and
nothing is re-embedded. `--full` only bypasses the mtime skip.

### The archive outlives the files

**A session whose file is gone is kept**, by `Build`'s sweep and by the
watcher alike, and so is one from a directory no longer listed. Only
exclusions -- SDK files, empty sessions -- are deleted. Before this the sweep
deleted anything it did not see, and `messages` cascaded with it, which made
the archive exactly as durable as the session directory and deleted every
merged session on the next build. It also meant one `brightlantern index --dir
somewhere-else` against the real index would wipe the rest of it.

**A file that is a prefix of its archive does not truncate it.**
`archiveMessages` compares the file's last message to the row at that index:
equal means the archive holds more (a longer copy merged in, or a truncated
file) and the extra rows stay, with `n_msgs` set to the archive's count;
different means a rewrite, and the extras go. Without the first half, `merge`'s
"longer wins" would be undone the next time the shorter local file changed.

Everything that read a session file falls back to the archive:
`index.LoadSession` and the web handlers read the file if it exists and
`ArchivedSession` if not. **The file wins when both exist**: it is live, and
the archive is only as fresh as the last index run. The titles pass uses
`LoadSession` too; without it a merged session with no title fails, records
nothing, and is retried on every run.

`session.DecodeArchived` is the inverse of `ParseWithRaw` and has to preserve
positions exactly, because `chunks.msg_idx`, the tool URLs and the scroll
anchors all address messages by index. Claude Code's fan-out means N rows share
one record, so a row is decoded only if an earlier row's fan-out has not
already produced its index; a missing row is padded with an empty `Message`
rather than closed up.

A full pass reindexes every session it did not see **from the archive**, which
is how the `needsVectors` and `needsTitleChunks` promotions reach sessions with
no file. An incremental pass leaves them alone: nothing about them changes
without a merge, and merge reindexes what it brings in. A session rebuilt from
the archive is marked `FromArchive`, which `upsertSession` reads to skip
archiving it back into itself and to keep its `host`.

### Merge

`brightlantern merge OTHER.db`: union by session id, longer copy wins. The other
index is `ATTACH`ed with `mode=ro` through a `file:` URI, which works against
a WAL database with no `-shm` and refuses writes. Only `sessions` and
`messages` cross over; chunks and vectors are rebuilt here from the archive,
because chunk ids are `AUTOINCREMENT` and `chunks_vec` is keyed by them.

- **Divergence is compared in full**, every overlapping row, not at one index
  like the prefix check -- it exists to catch the append-only assumption
  failing and cannot lean on it. A diverged session is reported and neither
  copy taken. 1264 sessions compare in about a second.
- A title travels with `title_key` and `title_msgs` or not at all. The winning
  row's title is taken when it has one; a losing copy's title still fills a
  session that has none here.
- A path claimed by a different session id here is reported and skipped:
  `sessions.path` is unique and neither row can go without its archive.
- No titles pass afterwards. Carrying titles is about not paying twice.

`tool_calls` is a view, dropped and recreated on every open because a view
holds no data. It unions pi's `toolCall` blocks with Claude Code's `tool_use`
blocks. Its `CASE` guard is load-bearing: `json_each` yields a string block as
SQL text, which `json_extract` rejects, and SQLite does not promise to test
`b.type` first. It has no block index, because Claude Code's parser drops
block types it does not know, so a raw position would not match the tool URLs.

After changing anything about indexing, both of these must return 0:

```sql
SELECT COUNT(*) FROM chunks_vec v
  WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.id = v.rowid);
SELECT COUNT(*) FROM chunks c
  WHERE NOT EXISTS (SELECT 1 FROM chunks_vec v WHERE v.rowid = c.id);
```

### Migrations

The layout changes through `migrations` in `internal/index/migrations.go`: a
numbered, forward-only list, each entry applied once in its own transaction and
counted under `meta.migrations`. **Never bump `SchemaVersion`**, which used to
be the mechanism and meant "delete the database and rebuild it" -- right while
everything was derived, and wrong since the archive. A change SQLite's `ALTER
TABLE` cannot express is still a migration: new table, copy, drop, rename.

`SchemaVersion` is frozen at 1 for a reason that is not in this repository:
**v0.0.1 and v0.0.2 delete any index whose `schema_version` is not 1**, and
released binaries cannot be fixed. Counting migrations under a key they never
read means reinstalling an old version against a migrated index gets SQL
errors, or derived data left stale for `--full` -- both recoverable. Downgrade
is unsupported, just no longer destructive.

Order inside `init` is load-bearing:

- **Every refusal comes before any write.** A newer index (more migrations than
  this build knows) is refused and left byte-identical; the test compares the
  file. `views` used to be dropped and recreated before the version was even
  read.
- `addColumns` runs before migrations: it predates them and is idempotent.
- `schema` runs **after** migrations, so a `CREATE INDEX` there can name a
  column a migration adds.

`OpenOrReset` survives only for a `schema_version` other than 1, which nothing
released has written. Even then an index holding messages or titles is **moved
aside**, never deleted: `index.db.schema-N.<timestamp>`.

`merge` requires the other index to have the same migration count, because it
reads the other through a read-only `ATTACH` and names columns. Behind is
refused because no migration exists yet to be behind by; migrating a temporary
copy before attaching it is the shape that fits when one does.

`internal/index/testdata/v0.0.1.db` is a database the released v0.0.1 wrote,
and must open intact with no reset. It is the one test of a migration this code
did not also generate. Never regenerate it with a newer build; its README says
how it was made, and a successor fixture should be added per release that
changes the layout.

## Live indexing

`serve` opens a second, writing handle and runs a catch-up build followed by
an fsnotify watcher. The catch-up finishes before the watcher starts: both
write, through one connection, and overlapping them is the same-connection
case above.

Events are coalesced after a 2s lull, because pi writes once per message.

**`serve` binds its port before anything else.** One writer per index holds
across processes only because a second `serve` cannot get that far: it used to
open a writer, run a catch-up build and start titling before discovering the
port was taken, and with a LaunchAgent running one invisibly that is the
ordinary case. On a taken port it asks `/api/instance` who is there, to say
"already running" rather than "address in use". `index` asks the same and
refuses when a server is *writing the same database* -- comparing paths, so a
daemon is no reason to refuse `--db` elsewhere -- because two titles passes pay
twice for the same sessions. `--force` overrides. `merge` does not check: it
does not title, and lock contention is all it risks.

A new project directory gets a watch **and a sweep of what is already in it**.
Creating the directory and writing the first session into it are two operations
milliseconds apart, so the session that caused the directory to appear is
exactly the one the watch would miss.

**`Build` and `Watcher.reindex` are two paths over the same decision.** The
watcher does not call `Build`; it parses and upserts the changed files itself.
So any rule about *which* sessions get indexed has to be written twice, and the
watcher is the path that matters more -- it is the one running while an agent
writes. The SDK filter shipped in `Build` alone and leaked 16 rows into a live
index within a day, because the titles pass writes the very files it excludes
*while the server is up*. They share `upsertSession`, so anything about how a
session is indexed is safe; anything about whether it is indexed is not.

The header polls `/status`, which **replaces itself**, so the server picks the
next interval (2s busy, 10s idle) rather than the page choosing once at load.
The page seeds the poll with the session count it rendered with, which is how
"3 new sessions" works without the server tracking per-client state.

The engine changes exactly once: lexical when the server starts answering,
lexical plus semantic once `warm` has loaded the model into its own reader
pool. Browsing stays on the lexical pool throughout. Nothing about the choice
may depend on index *contents*: a server started against an empty index would
otherwise stay keyword-only for its whole life. The semantic ranker is attached
whenever the model loads, and returns nothing until vectors exist.

`warm` loads the reader before opening the writer, not alongside it. The first
connection compiles the shaders and every later one hits the cache, so two at
once would compile twice; search goes first because someone at the page is
waiting for it. Until the writer exists `/status` reports `starting`, because
an empty response removes the header's poller and the page would never notice
the indexer arrive.

## Run at login

`brightlantern service install` writes
`~/Library/LaunchAgents/org.billmill.brightlantern.plist` and bootstraps it
into `gui/$UID`. The label is the bundle identifier, so M14's `SMAppService`
registers the same job (#74), and it must never change.

- **`serve --wait`, not `serve`.** `KeepAlive` restarts a `serve` that exits,
  and one exits whenever someone typed `brightlantern` first -- every ten
  seconds, a log line each time, for as long as theirs runs. `--wait` logs once
  and retries the port; it holds nothing while waiting, because the index and
  the model open only after the port is bound. Tested under launchd: one run,
  never exited, took over within five seconds.
- **The plist names the unresolved executable**, the opposite of
  `embed.DefaultPaths`. Homebrew's symlink is the stable name; its target is a
  versioned Caskroom directory that `brew upgrade` deletes. A `go run` binary
  is refused, being gone the moment it exits.
- **`install` refuses without a settings file.** A first run under launchd
  would detect session directories in launchd's environment and write that
  answer down for good.
- **No `EnvironmentVariables`.** The plist gets pasted into issues, and
  titles, being on-device, need nothing from the environment.
- Logs go to `~/Library/Logs/brightlantern/brightlantern.log`, both streams in
  one file: every `note()` is stderr and the startup line is stdout, and they
  only make sense interleaved. Nothing rotates it; `serve` is quiet.
- `mise run dev` serves on 5269 with `--no-watch`, beside the agent rather
  than against it.

Nothing tests `launchctl` itself. The plist is checked with `plutil -lint`;
the rest was exercised under launchd by hand, with a throwaway label, port and
index, which is the way to test a change here without touching the real agent.

## The app bundle

`mise run app` builds `build/Bright Lantern.app` with the daemon **inside**
it, so `SMAppService` registers an agent that is part of the bundle and an
upgrade replaces both at once (#86, reasoning in
`docs/plans/2026-10-06-smappservice-design.md`). `brightlantern`,
`brightlantern-apple` and `lembed0.dylib` sit beside `brightlantern-app` in
`Contents/MacOS`; the model is in `Contents/Resources`.

**The model cannot go in `MacOS`.** Everything there is signed as nested
code, and a data file there stops the bundle signing at all: "code object is
not signed at all", naming the file. So each executable and the dylib are
signed on their own first, then the bundle, and `embed.DefaultPaths` has a
candidate pairing `Contents/MacOS`'s dylib with `Contents/Resources`'s model.
`Beside` needs nothing: the helper is an executable and stays beside the
binary.

`BRIGHTLANTERN_DATA_DIR` is first in precedence and `mise.toml` sets it, so the
bundled daemon run from a mise shell uses the data directory, not the bundle.
Run it with `env -u BRIGHTLANTERN_DATA_DIR` to see what launchd will;
`brightlantern info` prints the extension, model and helper it resolved.

## Registering from the app

Opening the app is the whole install: it registers
`Contents/Library/LaunchAgents/org.billmill.brightlantern.plist` with
`SMAppService`, and the daemon runs at login with nobody having opened a
terminal (#74, `docs/plans/2026-10-07-register-agent-design.md`). The decision
is `agentPlan` in `agent.swift`, free of ServiceManagement so `app-test`
covers it; `register.swift` carries it out. Registration itself has no test:
it needs a login session, which a sandbox cannot reach -- every status
answers `notFound` there.

- **Config first.** With no `config.toml`, the app runs the bundled
  `brightlantern init` before registering, the rule `service install`
  enforces by refusing. An app opened from Finder has launchd's environment,
  so it passes `CLAUDE_CONFIG_DIR` from `$SHELL -l -i`, the one thing
  detection reads that launchd loses.
- **Never `launchctl bootout` the app's agent.** After two manual bootouts
  of the label -- a `service install` over it, then the app clearing that
  install -- launchd held the job as a `partial import` without the bundle's
  location, and every spawn failed with exit 78 and "Could not find and/or
  execute program specified by service ... Contents/MacOS/brightlantern":
  `BundleProgram` is relative and resolves only through the bundle smd
  submitted. Unregistering and registering again, even with a new BTM
  record, did not repair it; logging out and back in did, launchd
  rebuilding the job from the BTM records. `managedByApp` spots such a job by
  `managed_by = com.apple.xpc.ServiceManagement` -- `launchctl print` has no
  `program =` line for one -- and `service install` and `uninstall` both
  leave it alone. `service status` names it as the app's (#89).
- **When the agent will not start, ask launchd why.** `launchctl print`
  says *that* a spawn failed; `log show --predicate 'process == "launchd"
  AND eventMessage CONTAINS "brightlantern"'` says why. `needs LWCR update`
  in `properties` looked like the cause and was not. `sudo sfltool dumpbtm`
  shows the BTM records, writing every home as `/Users/<uid>`.
- **Signed with a Developer ID, even in development.** SMAppService records
  the agent's executable in a launch constraint (`LWCR`), by cdhash when the
  signature is ad-hoc, and every rebuild changes the cdhash. Whether that
  breaks a rebuilt daemon was never cleanly tested; a Developer ID gives the
  constraint a team instead. `--timestamp=none`, because the timestamp
  server is network and only notarization needs it.
- **Re-registered when the daemon's SHA-256 changes**, not
  `CFBundleVersion`: `mise run app` rebuilds the daemon at the same version,
  and `SMAppService.h` says a changed executable "may not launch" until
  re-registered. The hash is in the app's UserDefaults.
- **`requiresApproval` is never registered over.** It means someone turned it
  off in System Settings; the window says so and links there. On macOS 27
  the switch is under **Background App Activity**, not a Login Items list,
  so that is what user-facing text names. The app rechecks the status on
  every failed probe, since the switch can flip with the window open.
- **An M13 agent is replaced**, through the bundled `service uninstall`,
  and always followed by a registration: when the M13 job was the one
  loaded, booting it out leaves nothing running. `register()` straight after
  it was refused once with "Operation not permitted", so it retries. The
  other way round, `service install` refuses while the app's agent is
  loaded.
- **`say()`, not `NSLog`.** NSLog from the app never reached the unified
  log. `log show --predicate 'subsystem == "org.billmill.brightlantern"'`
  reads what it did.
- **`serve --log`, not `StandardOutPath`.** The plist is signed and the same
  for everyone, and launchd does not expand `~`. `--log` expands it and
  `dup2`s the file over fds 1 and 2, so panics and llama.cpp land there too.

Two bundles with this identifier -- `build/` and `/Applications` -- register
the same label, and whichever launched last wins. Worth knowing before
testing one beside the other.

## Titles

`sessions.title` is written by `internal/titles`, a pass that runs *after* a
build and never during one: both write, and the writer is one connection.
When Apple Intelligence is unavailable the pass is skipped with a note and
every row falls back to its opening message, which is what the list did for
five milestones.

**One backend: Apple's on-device model** (#91), through `brightlantern-apple`,
a Swift helper in `cmd/brightlantern-apple` built by `mise run apple` with
plain `swiftc` -- FoundationModels is in the Command Line Tools SDK, so no
Xcode. Measured on 200 sessions as a LaunchAgent: 199 titled, 1 declined, no
rate limiting, 1.66s a title at `AppleConcurrency` 2 -- four is no faster, the
model is local. Quality is moderately below Haiku and was accepted for that
(#84).

There were also `api` and `claude`. Neither worked under launchd, which has no
`ANTHROPIC_API_KEY` and no PATH that finds `claude` (#70), and launchd is how
the app runs the daemon; making them work meant a key file, a resolved binary
path and a picker. They were removed instead, and nothing leaves the machine.
A config still saying `api` or `claude` is read as `apple`, with a note.

The helper is a separate process, not cgo: a crash in cgo cannot be recovered
(see sqlite-lembed), and a crashed helper costs one title. It takes the
instructions and prompt as JSON on stdin, so the system prompt has one
definition. `blacktop/go-foundationmodels` does not build from `go get`: it
links a `libFMShim.a` that `go generate` makes and the read-only module cache
cannot hold. The helper ships beside the binary and `embed.Beside` finds it
the way it finds the dylib, and CI and the release runner are pinned to
`macos-26` for the SDK.

**The helper must compile against the macOS 26 SDK**, which is what the runners
have, and a development machine may have a newer one. `GenerationOptions(
samplingMode:)` is SDK 27's spelling; SDK 26 has only `sampling:`. It compiled
locally and failed on every runner, which is why the v0.0.3 release failed.
CI builds the helper on every push now. Command Line Tools keeps older SDKs
beside the current one, so `swiftc -sdk
/Library/Developer/CommandLineTools/SDKs/MacOSX26.sdk ...` checks it locally.

**A refusal is an answer, not a failure.** The on-device model has guardrails,
and they are not subtle: the one session of 200 it declined was about speeding
up `rmtree`. Exit 4 from the helper -- `guardrailViolation`, `refusal`, too
long, unsupported language -- becomes `ErrDeclined`, and the pass marks the
session checked like one with no prose. A failure records nothing and retries,
which for a refusal would mean asking again after every change the watcher
sees. `rateLimited` and everything else stay failures.

The instructions are repeated **after** the transcript, which is fenced on both
sides, even though the helper passes them separately as well. The slice is cut
at 10k characters, and without the closing half a transcript ending
mid-sentence drew *"Your message cuts off mid-sentence. Could you complete the
question?"* as a title, back when everything went as one message.

Two columns decide whether a session is paid for again, and they guard
different costs:

- `title_msgs` guards the **parse**. It is `n_msgs` as of the last time the
  pass looked. Without it, every run would parse all 1128 files to discover
  that nothing moved.
- `title_key` guards the **API call**. It hashes a bounded prefix of the
  conversation -- and that prefix is quantized to `keySteps`, so the hash moves
  when a session roughly doubles rather than on every message. Sessions are
  append-only and get reindexed per message; hashing the prose directly would
  re-summarize a live session continuously, which is the whole failure this is
  built to avoid.

A failure writes **neither**, so the row keeps its fallback and the next run
retries it. `MarkTitleChecked` writes only `title_msgs`, which is how a session
with no prose at all settles without ever getting a title.

Both columns are added by `addColumns()` (`ALTER TABLE ADD COLUMN`), not by
`schema`, because they predate migrations -- see "Migrations".

`--titles N` caps a run, which is useful for watching a few titles appear
before committing a cold corpus to half an hour of the on-device model.

## Query syntax

Double quotes mean a phrase, and a phrase **excludes**: a session without it
does not appear, however any ranker scores it. Demotion was the alternative and
is worse in the case that matters -- if a phrase only sorted matches upward,
a page of plausible results would look the same whether or not the corpus
contained the phrase, so "did I ever discuss exactly this?" would have no
observable answer. Empty is the answer, which is why `emptyNote` names the
phrase and says to drop the quotes.

Unquoted words stay ranking hints. One FTS5 expression does both:

    P AND (P OR w1 OR w2)

The AND-ed part constrains; the OR-ed group exists only so the bare words reach
BM25, which scores every phrase in the expression it is handed. Drop the second
group and a mixed query ranks as though the unquoted words were never typed.

**The lexical ranker is not enough on its own.** The semantic ranker has no
notion of a phrase -- it embeds the text and returns neighbours -- so it will
happily contribute sessions containing none of the words. `sessionsMatching`
filters the fused results, and without it quoting visibly fails to do the one
thing it promises. There is a test with a deliberately ignorant ranker for
exactly this.

Four things that are true and worth not rediscovering:

- Exact means exact **modulo stemming**: the tokenizer is `porter`, so
  `"deploying pipelines"` matches "deploy pipeline". Byte-exact needs a second
  unstemmed index over 46k chunks.
- A phrase spanning a chunk boundary is invisible, chunks being 800 characters.
- Single-character tokens are dropped from bare words and **kept inside
  phrases**. `"is a weird choice to"` matches nothing if the `a` is dropped.
- An unterminated quote is a phrase in progress, not an error. Search runs on
  every keystroke, so `"deploy pip` is a state the parser has to hold an
  opinion about.

## Rendering

Transcripts are parsed from the `.jsonl` on demand, and from the archive only
when there is no file (see above). Prose is 0.3–2.6% of a
large session's bytes -- a 7MB session is well under 100KB of conversation --
so tool output is fetched lazily and nothing needs pagination.

goldmark runs with raw HTML **disabled**. Session content is arbitrary text
that routinely contains HTML and JavaScript.

A `toolResult` carries a per-tool `details` object, and an edit's holds the
diff pi drew in the terminal -- markers, file line numbers, context, `...`
gaps -- under `details.diff`. Render that rather than computing one: the call's
arguments hold only the old and new text, so a diff derived from them could
never say *where* in the file the edit landed, and the line numbers are most of
what makes a diff readable. When a diff is present the arguments are hidden,
because they are the same edit as escaped JSON. A failed edit has `details:
{}`, and there the arguments are the useful part: the text that was not found.

## Keyboard

The cursor is real DOM focus on a row's `<a>`, not a class we track. Focus
gives scroll-into-view, Enter-to-activate, and screen reader support for
free. Consequences: rows must be anchors with an `href`, and `.row:focus`
rather than `:focus-visible` does the styling, because a programmatic
`focus()` does not always count as keyboard-initiated.

`app.ts` reaches the page through selectors, which nothing type-checks.
`markup_test.go` pins that contract; it cannot tell whether `j` *works*, which
is what `e2e/smoke.spec.ts` is for. Both are worth having: the Go test says
which selector broke, in the suite that runs on every commit.

## Tests

`mise run check` is ~35s. Nearly all of it is `internal/index` and
`internal/indexer`: the watcher tests wait out a 2s settle timer, and each
semantic test loads the model. The other packages total a few seconds.

Semantic tests skip when the model is not installed **and** when it is but
the backend cannot run it -- the second because only opening a connection is
an honest test (see the GPU note above). CI relies on the first: it installs
the model after `check`, so they skip there.

A watcher test asserting a file was **not** indexed has to wait out a second
settle first. "Not indexed yet" and "never indexed" look identical, so an
assertion made immediately after some other file appears passes whether or not
the code works -- one did, once, before the sleep was added.

### Browser tests

`mise run e2e` drives Chromium through Playwright. Not part of `mise run
check`: it is ~7s against check's ~20s but needs a browser that is not a
repository dependency, and a failure there is a different kind of signal.

`mise run e2e-install` fetches that browser, once. `@playwright/test` is pinned
to `~1.58`, whose browser revision is **chromium-1208**, because that build was
already in the shared cache -- a minor bump is a 130MB download, so it is worth
knowing that is what changed.

Playwright starts the server itself, through `e2e/serve.sh`, which compiles
`app.js` (a stale one means testing the previous keyboard handling), builds the
binary, and indexes `e2e/sessions` into a temp database. The fixtures are
committed for a reason: the tests assert which
session is newest and how many rows a filter leaves, and neither survives
contact with a real corpus. `e2e-charlie` is 41 messages with one occurrence of
"quicksand" at the end, so the scroll-to-match test starts below the fold.

These tests are only worth their weight if they fail when the behaviour breaks,
which is worth re-checking after editing them: disabling `scrollToMatch()` and
renaming the `j` case both produce failures.

## Releasing

`mise run release`, from a `v*` tag, through goreleaser. **darwin/arm64 only**:
go-sqlite3, the sqlite-vec bindings and FTS5 all need cgo, so the
`CGO_ENABLED=0` cross-compilation other repositories here use is not available.
Restricting to one architecture removes the problem instead -- a macOS runner is
already arm64.

The entry point is `mise run release` and not bare `goreleaser`, because
`CGO_CFLAGS` comes from `mise.toml`'s `[env]`. A preflight hook fails the build
without it rather than compiling against whichever `sqlite3.h` the machine has.
`before` hooks also run `sqlite-header` and `ts`; a missing `app.js` is embedded
silently and ships the previous release's keyboard handling.

The archive carries `lembed0.dylib` and the model beside the binary, and
`embed.DefaultPaths` resolves the running executable and looks in its own
directory. That is what makes both an unpacked tarball and the cask work:
Homebrew puts only `brightlantern` on PATH and leaves the rest in the Caskroom, so
resolving *through* the symlink is what finds them.

**Homebrew quarantines cask artifacts by default** -- that is what
`--no-quarantine` overrides -- and nothing shipped is signed by a Developer ID.
Both files are refused, and differently: the binary dies with `Killed: 9` behind
an "Apple could not verify" dialog, while the dylib fails `dlopen` and SQLite
retries with the suffix appended, so the error names `lembed0.dylib.dylib` and
says "no such file" about a file that is right there. The result is a working
brightlantern with semantic search silently gone. The `postflight_steps` + `xattr -dr`
in `.goreleaser.yaml` clears both; notarization would be the real fix.

Pushing the cask needs `HOMEBREW_TAP_TOKEN`, a PAT secret on this repo.
`GITHUB_TOKEN` cannot push to the tap.

## Planning

Milestones are GitHub milestones, numbered `M1`..`Mn`, each with a one-line
description of what is in scope. They are worked roughly in order, and the
number is how a body of work gets referred to in conversation.

Issues carry the reasoning at length, the way commit bodies do. An issue is
usually where a design argument gets written *first* -- it then ends up in a
commit body, and often in this file. Recording the option that was rejected
and why is most of the value, because that is the question asked again six
months later.

Titles are either a problem statement -- "The watcher can miss the first
session in a brand-new project directory" -- or an imperative -- "Ship the
extension and model through Homebrew". Both say what is wrong or what should
exist, not which file to edit.

Measurements belong in the issue, taken rather than guessed. #26 estimated
~81MB of message JSON; #37 measured 265MB and said so, which is what made the
cost of the archive arguable instead of assumed.

An issue too big to do at once gets broken down, and the parent keeps the
list -- #26 is a sketch plus a breakdown into #37--#41, and is not itself work.

Milestones do the grouping. No labels are in use, and nothing needs them at
this size.

## Commits

A subject line says what changed, not how much of it: "Archive every message in
the database", not "Record three things this milestone cost an hour" -- which is
an actual commit here, and names neither the file it touched nor any of the
three things.

The long-standing style is a capitalized sentence in the imperative. The eight
commits of the Claude Code milestone add a `scope:` prefix -- `storage:`,
`render:`, `infra:` -- which is the more recent answer and worth following where
a change belongs to one area.

Bodies carry the reasoning, at length, and are most of the value: the same
argument usually ends up in a comment or in this file, and the commit is where
it gets written first.

## Changing GitHub Actions

Run `aver` after editing a workflow; it reports outdated action versions.
`actionlint` (via docker) catches syntax errors.

## Corpus

**pi**: 1190 files, 258MB, median 95KB, p90 567KB, max 7.2MB; 48k chunks.
Useful for judging whether an approach scales; build timings are in "The
archive" above.

**Claude Code**: 1617 files, 284MB -- but only **17 files and 1MB** of it is
conversation this repository does not already have. The rest is excluded as SDK
output, so it is worthless for judging scale and actively misleading for judging
quality. Almost everything done through Claude Code on this machine arrives via
claude-bridge and is therefore a duplicate of a pi session. Anything that needs
a real Claude Code corpus needs someone else's.
