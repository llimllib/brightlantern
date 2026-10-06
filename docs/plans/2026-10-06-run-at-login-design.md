# M13: Run at login

Bright Lantern as a resident LaunchAgent: always indexed, always warm, no
terminal. Ships on its own, without an app.

Worked in this order, one commit each. Later steps depend on earlier ones:
the daemon is only safe once a second instance cannot write (#67), and the
LaunchAgent is only useful once titles work in launchd's environment (#84).

## 1. Listen before anything else (#67)

`runServe` binds the socket first, before `bootstrapIndex`, the model, or the
writer. A second `serve` then fails in milliseconds having touched nothing,
and says so: "brightlantern is already running on 127.0.0.1:5268", not
"bind: address already in use".

`brightlantern index` asks the configured address for `/status` before
opening the writer. If a brightlantern answers, it refuses: the daemon already
keeps the index current, and two titles passes would pay twice for the same
untitled sessions. `--force` runs it anyway, which is how `--full` and
`--titles N` stay reachable without stopping the daemon.

## 2. A fixed, written-down address (#68)

The default moves from `127.0.0.1:8080` to **`127.0.0.1:5268`**: "LANT" on a
phone keypad, unassigned by IANA, below macOS's ephemeral range (49152+), and
clear of 3000, 5000, 5173 and 8000-8080. A port in the ephemeral range would
occasionally be taken by some process's outbound connection, and the daemon
would fail to start for no visible reason.

A first run writes `addr` into `config.toml`, as it already writes `titles`.
Every client -- the app in M14, a probe, `index`'s check above -- reads the
same file. A file with no `addr` falls back to the default.

## 3. Page first, model behind it (#56)

`serve` loads the model before it listens, so a cold Metal cache holds the
whole process for ~15s. Under `KeepAlive` that is a restarted daemon refusing
connections. Instead the server starts on the lexical driver and swaps in the
semantic ranker once a semantic reader pool has opened in the background.
Browsing and keyword search are available immediately.

Shipping a precompiled `.metallib` stays out of scope: it needs full Xcode and
the separate Metal toolchain, which this machine does not have.

## 4. Titles on the device (#84, replaces most of #70)

A new backend, `titles = "apple"`, using Apple's on-device Foundation Models.
No key, no bill, no network. Measured on 25 sessions from the real index
(details on #84): no failures, median 2.6s per title, works under launchd, and
quality moderately below Haiku -- accepted in exchange for running entirely on
the device.

Named for the provider rather than "local", so another local model can be
added later without the name lying.

- A Swift executable, `brightlantern-apple`, built with `swiftc` from Command
  Line Tools: no Xcode. It reads a slice on stdin and writes a title on stdout,
  with the same system prompt the other backends use.
- A separate process rather than cgo. A crash inside cgo cannot be recovered
  (see sqlite-lembed in AGENTS.md); a crashed helper costs one title. It also
  keeps the Go side buildable and testable on Linux CI.
  `blacktop/go-foundationmodels` was considered and does not build from
  `go get`: it links a `libFMShim.a` produced by `go generate`, which cannot
  run in the read-only module cache.
- It ships beside the binary and is found the way `embed.DefaultPaths` finds
  the dylib, by resolving the running executable through Homebrew's symlink.
- The default for new settings files. `claude` and `api` remain for terminal
  use. Under launchd, `api` has no key and `claude` has no PATH; both report
  "titles disabled" in the log rather than being worked around.

## 5. The LaunchAgent (#69)

`brightlantern service install | uninstall | status`. The plist:

- label `org.billmill.brightlantern`, the same as the bundle identifier, so
  M14's `SMAppService` registers the same job (#74)
- `RunAtLoad` and `KeepAlive`
- the absolute path of the running executable in `ProgramArguments`
- stdout and stderr to `~/Library/Logs/brightlantern/`, where Console.app looks

`install` refuses unless `config.toml` exists. A first run under launchd would
detect session directories in launchd's environment and write that answer down
permanently, so the first run has to be from a terminal. It also notes if
`titles` is something other than `apple`, because the daemon cannot use the
others.
