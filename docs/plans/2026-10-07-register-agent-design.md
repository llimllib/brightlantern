# M14: The app registers the agent (#74)

The experience this serves: someone installs the app, from Homebrew or by
dragging it to /Applications, and opens it. The daemon starts running at login
without their running a command, at most behind a system notification or an
approval prompt. Nobody is asked to know what a terminal is.

`docs/plans/2026-10-06-smappservice-design.md` decided the mechanism:
`SMAppService.agent`, with a plist inside the bundle naming the daemon inside
the bundle (#86) through `BundleProgram`. This is what the app does with it.

The spike found ad-hoc signing enough to register. `mise run app` signs
with the Developer ID anyway, because the registration pins an ad-hoc daemon
by cdhash, which every rebuild changes; see AGENTS.md, "Registering from the
app", which also records how booting the agent out by hand broke it.

## At launch

`ensureAgent()` runs before the window's probe, off the main thread:

1. **Config.** With no `config.toml`, run `Contents/MacOS/brightlantern
   init`, which is `resolve`'s first-run detection without indexing: find
   the session directories, write the file, exit. It exits non-zero when it
   finds nothing, and then the window says so and nothing is registered.
2. **An M13 agent.** If `statusForLegacyPlist` reports
   `~/Library/LaunchAgents/org.billmill.brightlantern.plist`, run the
   bundled `brightlantern service uninstall` first. Same label, same job; two
   would be #67 again.
3. **Register**, by `SMAppService.agent(plistName:).status`:
   - `notRegistered`, `notFound`: `register()`
   - `enabled`: re-register (unregister, then register) if the daemon's hash
     differs from the one recorded; otherwise nothing
   - `requiresApproval`: never register over it -- someone turned it off in
     Login Items. The startup page says so, with a button that calls
     `openSystemSettingsLoginItems()`. #75's last row.
4. Record the hash.

The decision is a pure function, free of AppKit and ServiceManagement, so
`mise run app-test` covers every branch. Registration itself can only be
tested in a login session.

### Why the app can detect at all

`service install` refuses without a settings file, because a first run under
launchd detects directories in launchd's environment and writes that answer
down for good. An app opened from Finder has that same environment, so
"run it from a terminal first" is not available to it.

What launchd's environment loses is `CLAUDE_CONFIG_DIR`. Detection probes
`~/.config/claude`, `~/.claude` and `~/.pi` by path, so the variable matters
only when it points somewhere else. The app reads it from the login shell --
`$SHELL -lc` -- and passes it to `init`. Detection stays in Go, written once.

No questions are asked. A first run already writes `titles = "apple"`, which
needs no key and works under launchd; #76's picker goes in front of `init`.

## What counts as an upgrade

`SMAppService.h`: an agent whose executable changes "must be re-registered
or it may not launch". `CFBundleVersion` would miss the common case, which is
`mise run app` rebuilding the daemon at the same version. So the app records
the SHA-256 of `Contents/MacOS/brightlantern`, about 20ms, and re-registers
when it moves. Unregistering stops the running job, so the new daemon
replaces the old one rather than waiting for a login.

## The plist

`cmd/brightlantern-app/org.billmill.brightlantern.plist`, copied to
`Contents/Library/LaunchAgents/`:

- `BundleProgram` `Contents/MacOS/brightlantern`, arguments `serve --wait
  --log ~/Library/Logs/brightlantern/brightlantern.log`
- `RunAtLoad`, `KeepAlive`, as M13's
- `AssociatedBundleIdentifiers`, so Login Items names Bright Lantern

**No `StandardOutPath`.** The plist is static and signed, and launchd does not
expand `~`, so it cannot name a log in the user's home. `serve --log` opens
the file itself, expanding `~`, and points both stdout and stderr at it --
descriptors, not `os.Stdout`, so a panic lands there too. The same file
`service install` uses, so `service status` and the app's stuck page stay
right.

## Rejected

- **Waiting for a terminal first run**, as `service install` does. It makes
  the app need a command line, which is the one thing it must not.
- **Detecting in Swift.** A second copy of `session.Candidates` to keep in
  step, and no better informed than the Go one.
- **Asking about an M13 agent** before replacing it. Nobody who cannot use a
  terminal can answer it.
