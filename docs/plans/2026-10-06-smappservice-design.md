# M14: Registering the LaunchAgent from the app (#74)

Not yet a design: a decision and the measurements it needs. What follows is
what could be established from the SDK headers and from codesign, and what
can only be established by registering something in a real login session.

## What the headers say

`ServiceManagement/SMAppService.h`, in the Command Line Tools SDK:

- **`BundleProgram` is optional.** Agent plists in
  `Contents/Library/LaunchAgents` "may use" it, "in addition to the standard
  launchd.plist keys". #74 assumed the daemon must ship inside the bundle;
  by the header, `ProgramArguments` pointing outside it is allowed.
- **Changing the executable needs re-registration.** "If an app updates
  either the plist or the executable for a LaunchAgent … the SMAppService
  must be re-registered or it may not launch. It is recommended to also call
  unregister before re-registering if the executable has been changed." So
  the app has to remember what it registered and redo it after an upgrade.
  #74 does not mention this, and getting it wrong fails silently.
- **The two-routes conflict has an API.** `statusForLegacyPlist(at:)`
  reports on a plist in `~/Library/LaunchAgents`, which is where
  `brightlantern service install` writes. #74 names the risk -- two routes,
  one label, two daemons -- and this is how the app can see it coming.
- "Apps that use SMAppService APIs must be code signed", and `register()`
  returns `kSMErrorInvalidSignature` otherwise. Whether ad-hoc counts is not
  said.
- `openSystemSettingsLoginItems()` exists for `.requiresApproval`.

## What codesign says

If the daemon moves inside the bundle, everything in `Contents/MacOS` is
nested code and must be signed before the bundle:

| in `Contents/…` | bundle signs and verifies? |
| --- | --- |
| an unsigned script in `MacOS/` | no: "code object is not signed at all" |
| a data file -- a stand-in for the `.gguf` -- in `MacOS/` | no, same |
| the same data file in `Resources/` | yes |

So `brightlantern`, `brightlantern-apple` and `lembed0.dylib` would each be
signed on their own, and the model would have to live in `Resources/`.
`embed.DefaultPaths` looks only beside the executable, so it would need a
second place to look.

## The decision

**A. The plist points outside the bundle.** `ProgramArguments` naming
Homebrew's `/opt/homebrew/bin/brightlantern`, exactly as `service install`
writes it. The app carries only itself and a plist. Small, but it ties the
app to a Homebrew install, and whether `brew upgrade` replacing the binary
under a registered agent breaks it -- the header suggests it might -- is
unknown.

**B. The daemon ships inside the bundle.** `BundleProgram` naming
`Contents/MacOS/brightlantern`, with the helper and dylib signed beside it
and the model in `Resources/`. The app is self-contained and survives being
moved, an upgrade replaces everything at once, and re-registering on a
changed `CFBundleVersion` covers it. The CLI becomes a symlink into the app,
via the cask's `binary` stanza. Bigger: it reworks packaging, and that
touches the release (#78).

B is what a Mac app usually is, and it is the single artifact M15 wants to
sign and notarize. A is less work now, and it may break on `brew upgrade` in
a way nobody notices until the index goes stale. **Undecided: it is the
choice this ticket is waiting on.**

Either way, the app needs to:

- record the version it registered, and unregister then re-register when it
  changes
- check `statusForLegacyPlist` first, and not register while an M13
  LaunchAgent is installed -- say so, or offer to replace it
- treat `.requiresApproval` as #75's third state, with a button that calls
  `openSystemSettingsLoginItems()`

## Measured in a login session

The sandbox these notes were first written from cannot reach the background
task service: every status query, including for agents installed and
running, answered `notFound`. So the questions went to a throwaway app under
`org.billmill.brightlantern-spike`, not the real label -- one copy ad-hoc
signed and one with the Developer ID, each carrying an agent that runs
`/bin/sh` outside the bundle and one whose `BundleProgram` is a script
inside it, both only appending the date to a file. On macOS 27:

| | ad-hoc | Developer ID |
| --- | --- | --- |
| `register()` | ok | ok |
| status after | `enabled` | `enabled` |
| external agent ran | yes, within 2s | yes, within 2s |
| bundled agent ran | yes, within 2s | yes, within 2s |
| status after `unregister()` | `notRegistered` | `notRegistered` |

- **Ad-hoc is enough to register.** #74 can be built and tested before M15.
- **Both options run.** Neither is ruled out on technical grounds.
- **No approval was asked for.** Status went straight to `enabled`. The app
  must still handle `.requiresApproval` -- someone can switch it off in
  Login Items -- but it is not the first-run path.

Not measured: what status reads before the first registration (the spike
registered first), and what Login Items displays. And the one that matters
most for option A -- whether a registered agent keeps launching after its
executable is replaced -- needs a test that swaps the program, which this one
did not.

`build/smappservice-spike/run.sh` is the spike; its header has the steps.

Still open after that, and needing a real cask install rather than a bundle
in `build/`: whether registration survives the cask's `/Applications`
symlink into the Caskroom (#74's own known unknown), and what `brew upgrade`
does to a registered agent.
