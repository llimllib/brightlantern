# M14: A WKWebView shell (#71, #72)

The app: a window, a `WKWebView`, and a URL. It is a **client**, not a host --
the LaunchAgent from M13 is already running and already indexing, and the app's
job is to show it without browser chrome.

Why not Electron or Tauri is argued at length on #71 and not repeated here.
The short version: there is no renderer-side application to host, the thing
that genuinely needs hosting is a cgo binary neither of them helps with, and
`WKWebView` ships with macOS.

This covers #71 and #72 together. #72 is four lines of the `Info.plist` that
#71 must author anyway, and without it the webview loads nothing -- so a strict
#71 would ship an app that provably does not work.

## 1. The bundle, built with swiftc (#71)

`cmd/brightlantern-app/main.swift`, beside `cmd/brightlantern-apple`, built by
`mise run app` into `build/Bright Lantern.app`:

```
build/Bright Lantern.app/Contents/
  Info.plist
  MacOS/brightlantern-app
```

No Xcode project. `swiftc` with a hand-written plist is how
`cmd/brightlantern-apple` already builds, it keeps `mise run app` a real task
rather than a wrapper around `xcodebuild`, and AppKit and WebKit are in the
Command Line Tools SDK -- Swift autolinks both from the `import`.

`-target arm64-apple-macos26.0`, matching the `apple` task rather than taking
swiftc's default of 27.0. The default is whatever the build machine runs,
which is an unpinned input of exactly the kind the `sqlite3.h` section of
AGENTS.md exists about.

The executable inside is `brightlantern-app`, not `Bright Lantern`.
`CFBundleName` carries the display name, so nothing user-facing is affected,
and it keeps a space out of every path the build task quotes.

`build/` is already gitignored, and already where `release-assets` stages
things. No new ignore.

## 2. Where the URL comes from (#71)

```
--url argument  →  config.toml addr  →  127.0.0.1:5268
```

`config.toml` is not a new decision: `internal/config/config.go` already
writes, above the `addr` key it emits, *"where everything else -- a second
brightlantern, the app -- looks for it"*, and §2 of the M13 design says every
client reads the same file. The comment predates the app.

**The argument is an override, not the mechanism.** Dock, Finder and Spotlight
all launch through `launchd` and cannot pass arguments; only `open -a "Bright
Lantern" --args …` and `mise run app` can. So an argument-only design would
work from a terminal and nowhere else. What it buys is the development loop:
point the app at `:5269` beside the real agent without editing settings.

Swift has no TOML parser in the stdlib and needs one scalar out of that file,
so the read is deliberately narrow -- `addr` at the start of a line, trailing
`#` comment stripped, basic and literal strings accepted, anything surprising
falling back to the default rather than guessing. A real parser for one string
is not worth a dependency in a second language.

`5268` is therefore written in both Go and Swift. That is the norm rather than
a smell: Docker's clients each carry their own
`unix:///var/run/docker.sock`, as Ollama's do `127.0.0.1:11434`, and both
treat an environment variable as the override. Nobody discovers a localhost
daemon. A generated header for one constant that has never changed costs more
than it saves; the Swift side names `config.DefaultAddr` in a comment instead.

**Rejected: a runtime state file.** Jupyter has the server write its live URL
into a runtime directory and clients read it, which is better when the server
may bind somewhere other than what is configured. Here that case is thin --
`serve` binds its port before anything else and refuses to continue if it is
taken, so there is at most one daemon per address -- and it adds a staleness
problem, since a daemon that crashes leaves the file behind claiming to be up.

## 3. The ATS exception (#72)

App Transport Security blocks plain HTTP, which is what the daemon speaks.
`NSAllowsLocalNetworking` under `NSAppTransportSecurity` is the narrow key:
it permits insecure loads to local-network hosts while leaving ATS in force
for the internet, unlike `NSAllowsArbitraryLoads`, which turns it off
wholesale.

**To verify empirically rather than assume:** the default address is the IP
literal `127.0.0.1`, not the name `localhost`, and Apple documents
`NSAllowsLocalNetworking` in terms of unqualified and `.local` names and
local-network address blocks. Whether it covers a loopback literal is the one
thing here worth testing before trusting, because the failure mode is a blank
window. If it does not, an `NSExceptionDomains` entry for `127.0.0.1` with
`NSExceptionAllowsInsecureHTTPLoads` goes in alongside it. The answer gets
written into the commit body either way.

## 4. The window remembers itself (#71)

`NSWindow.setFrameAutosaveName` -- one line, persisted in `UserDefaults`, and
the difference between an app and a demo. Set after the initial frame, since
the call restores a saved frame when one exists.

## 5. Failure is logged, not handled (#75 owns the UI)

#75 designs what the user sees when the daemon is down: probe `/status`, three
states, a grace period before narrating, a timeout naming the log path. It
needs #74 to know whether the agent is registered, so it is not folded in
here, and no placeholder is built that #75 would only delete.

What #71 does need is attribution. #75 observes that an ATS misconfiguration
and a dead daemon currently produce the same blank window -- and since #72 is
in scope here, "did the exception work" has to be answerable. So
`WKNavigationDelegate` is implemented for one purpose: log the `NSError` from
`didFailProvisionalNavigation`, whose code separates
`NSURLErrorAppTransportSecurityRequiresSecureConnection` from
`NSURLErrorCannotConnectToHost`. About ten lines, and it is what makes this
ticket verifiable rather than merely finished.

## 6. `mise run app`

Follows the `apple` task: a no-op off macOS, with `sources` and `outputs` so
it is skipped when nothing changed. **Not a dependency of `check`** -- CI is
Linux, and `check` must not grow a step that silently does nothing there.

**Rejected: declaring `swift` in `mise.toml`'s `[tools]`.** mise has
`core:swift`, and it installs swift.org toolchain releases -- which do not
carry Apple's frameworks. `WebKit`, `AppKit` and `FoundationModels` all come
from the Command Line Tools SDK, so Command Line Tools stays a hard
requirement and the declaration would be managing something other than the
real dependency. It would also download a toolchain on Linux CI, where both
Swift tasks are no-ops, and leave two compilers that can disagree about which
SDK they select. What actually affects the output is already pinned:
`-target arm64-apple-macos26.0`. A compiler version is not worth pinning for a
few hundred lines of AppKit glue.

What is worth having is a legible failure. A missing `swiftc` currently gives
`command not found`; the task should name `xcode-select --install` instead.
The existing `apple` task has the same gap and should get the same guard.

## 7. Out of scope, and whose job it is

`NSMenu` and cmd-Q (#73) -- the app will not be quittable from the keyboard
and that is known. In-session find, replacing the cmd-F the browser used to
give (#35). `SMAppService` registration (#74). The daemon-down UI (#75). An
icon, version injection into `CFBundleShortVersionString`, signing and
notarization: all M15/#78, which is where the Developer ID from #85 gets used.

## 8. What can be tested, and what cannot

Honestly, very little automatically. Nothing in this repository drives AppKit,
and a windowed app needs a GUI session no CI runner has.

- `plutil -lint` on the `Info.plist`, which is the precedent the LaunchAgent
  plist already set in AGENTS.md.
- That `mise run app` produces a bundle, and that the executable inside it
  loads -- a build smoke check, not a behavioural one.
- Everything else by hand, the way the LaunchAgent was: launch it, confirm the
  page renders, quit and relaunch to confirm the frame came back, and point it
  at a dead port to confirm the delegate names the right error.

The URL resolution is the one piece of real logic, and it is the piece that is
reachable without a window. Worth keeping it in a function that takes the
arguments and the file contents and returns an address, so it can be exercised
directly.
