// Tests for address.swift, run by `mise run app-test`.
//
// A plain executable rather than XCTest: XCTest needs Xcode, and this
// repository builds its Swift with Command Line Tools alone.

import Foundation

var failures = 0

func check<T: Equatable>(_ got: T, _ want: T, _ name: String, line: Int = #line) {
    if got != want {
        print("FAIL line \(line): \(name)\n  got:  \(got)\n  want: \(want)")
        failures += 1
    }
}

// configAddr

// What internal/config's SaveTo actually writes, comments and all.
let written = """
    # brightlantern settings. Delete a line to have brightlantern work it out again.

    dirs = [
      "/Users/someone/.pi/agent/sessions",
    ]

    titles = "apple"

    # Address the web interface listens on, and where everything else --
    # a second brightlantern, the app -- looks for it.
    addr = "127.0.0.1:5270"

    """
check(configAddr(written), .found("127.0.0.1:5270"), "the file config.go writes")
check(configAddr("titles = \"apple\"\n"), .absent, "no addr line")
check(configAddr(""), .absent, "empty file")
check(configAddr("addr = ''\n"), .absent, "empty value")
check(configAddr("addr = 'localhost:9000'\n"), .found("localhost:9000"), "literal string")
check(configAddr("  addr=\"127.0.0.1:1\"   # mine\n"), .found("127.0.0.1:1"), "indent, no spaces, trailing comment")
check(configAddr("# addr = \"127.0.0.1:1\"\n"), .absent, "commented out")
check(configAddr("address = \"x:1\"\n"), .absent, "a different key with addr as a prefix")
check(configAddr("[server]\naddr = \"127.0.0.1:1\"\n"), .absent, "addr inside a table is not top-level")
check(configAddr("addr = 5268\n"), .unreadable("addr = 5268"), "not a string")
check(configAddr("addr = \"127.0.0.1:1\n"), .unreadable("addr = \"127.0.0.1:1"), "unterminated")
check(configAddr("addr = \"a\\tb\"\n"), .unreadable("addr = \"a\\tb\""), "escape")
check(configAddr("addr = \"127.0.0.1:1\" x\n"), .unreadable("addr = \"127.0.0.1:1\" x"), "junk after the value")

// dialable, matching cmd/brightlantern's

check(dialable("127.0.0.1:5268"), "127.0.0.1:5268", "loopback unchanged")
check(dialable(":5268"), "127.0.0.1:5268", "no host")
check(dialable("0.0.0.0:5268"), "127.0.0.1:5268", "unspecified v4")
check(dialable("[::]:5268"), "127.0.0.1:5268", "unspecified v6")
check(dialable("[::1]:5268"), "[::1]:5268", "loopback v6 keeps its brackets")
check(dialable("localhost:5268"), "localhost:5268", "a name")

// resolveURL

var notes: [String] = []
func resolve(_ args: [String], _ config: String?) -> String {
    resolveURL(arguments: ["brightlantern-app"] + args, config: config) { notes.append($0) }.absoluteString
}

notes = []
check(resolve([], nil), "http://127.0.0.1:5268/", "no file: the default")
check(notes, [], "no file is not worth a note")

check(resolve([], "addr = \"127.0.0.1:5270\"\n"), "http://127.0.0.1:5270/", "the file")
check(resolve([], "addr = \":5270\"\n"), "http://127.0.0.1:5270/", "the file, dialable")
check(resolve(["--url", "http://127.0.0.1:5269/"], "addr = \"127.0.0.1:5270\"\n"),
      "http://127.0.0.1:5269/", "the argument beats the file")

notes = []
check(resolve(["--url", "not a url"], "addr = \"127.0.0.1:5270\"\n"),
      "http://127.0.0.1:5270/", "a bad argument falls through to the file")
check(notes.count, 1, "and says so")

notes = []
check(resolve(["--url", "file:///etc/passwd"], nil), "http://127.0.0.1:5268/", "not http")
check(notes.count, 1, "and says so")

notes = []
check(resolve(["--url"], nil), "http://127.0.0.1:5268/", "--url with nothing after it")
check(notes.count, 1, "and says so")

notes = []
check(resolve([], "addr = 5270\n"), "http://127.0.0.1:5268/", "an unreadable file: the default")
check(notes.count, 1, "and says so")

// Finder and Xcode both pass arguments of their own; none of them is --url.
check(resolve(["-NSDocumentRevisionsDebugMode", "YES"], nil), "http://127.0.0.1:5268/", "foreign arguments")

check(configPath(environment: ["XDG_CONFIG_HOME": "/x"]), "/x/brightlantern/config.toml", "XDG_CONFIG_HOME")
check(configPath(environment: [:]), NSHomeDirectory() + "/.config/brightlantern/config.toml", "home")

// startup

check(startup(answered: true, elapsed: 0), .ready, "an answer is ready at once")
check(startup(answered: true, elapsed: 120), .ready, "and however late")
check(startup(answered: false, elapsed: 0), .quiet, "say nothing at first")
check(startup(answered: false, elapsed: startupGrace - 0.1), .quiet, "through the grace")
check(startup(answered: false, elapsed: startupGrace), .starting, "then say starting")
check(startup(answered: false, elapsed: startupStuckAfter - 0.1), .starting, "until it has been too long")
check(startup(answered: false, elapsed: startupStuckAfter), .stuck, "then say where to look")

check(statusURL(for: URL(string: "http://127.0.0.1:5268/")!).absoluteString,
      "http://127.0.0.1:5268/status", "status beside the page")
check(statusURL(for: URL(string: "http://127.0.0.1:5269/sessions/abc")!).absoluteString,
      "http://127.0.0.1:5269/status", "status at the root, whatever --url named")

check(startupPage(.quiet, address: "a", log: "l"), nil, "quiet shows nothing new")
check(startupPage(.ready, address: "a", log: "l"), nil, "ready shows the daemon")
check(startupPage(.starting, address: "127.0.0.1:5268", log: "l")?.contains("127.0.0.1:5268"), true,
      "starting names the address")
check(startupPage(.stuck, address: "a", log: "/x/brightlantern.log")?.contains("/x/brightlantern.log"), true,
      "stuck names the log")
check(startupPage(.starting, address: "<script>", log: "l")?.contains("<script>"), false,
      "the address comes from a file someone edits, so it is escaped")
// The animation (#88): moving while starting, still once stuck, and never
// varying per probe, or each reload would restart it.
check(startupPage(.starting, address: "a", log: "l")?.contains(lantern), true, "starting shows the lantern")
check(startupPage(.stuck, address: "a", log: "l")?.contains("class=\"lantern\""), false, "stuck stays still")
check(startupPage(.starting, address: "a", log: "l")?.contains("prefers-reduced-motion"), true, "reduced motion is honoured")
check(startupPage(.starting, address: "a", log: "l"), startupPage(.starting, address: "a", log: "l"),
      "the same page every probe, so the animation runs on")

check(logPath(home: "/Users/x"), "/Users/x/Library/Logs/brightlantern/brightlantern.log", "service.go's log")

// agentPlan (#74)

func facts(config: Bool = true, legacy: Bool = false, _ status: AgentStatus,
           now: String? = "h1", registered: String? = "h1") -> AgentFacts {
    AgentFacts(hasConfig: config, legacyInstalled: legacy, status: status,
               daemonHash: now, registeredHash: registered)
}

check(agentPlan(facts(.notRegistered, registered: nil)), [.register], "first launch with a config")
check(agentPlan(facts(config: false, .notRegistered, registered: nil)), [.writeConfig, .register],
      "first launch: config before the agent, so the daemon never detects under launchd")
check(agentPlan(facts(.notFound)), [.register], "notFound registers like notRegistered")
check(agentPlan(facts(.enabled)), [], "registered and unchanged: nothing, every launch after the first")
check(agentPlan(facts(.enabled, now: "h2")), [.reregister], "a rebuilt or upgraded daemon is re-registered")
check(agentPlan(facts(.enabled, registered: nil)), [.reregister],
      "registered by some earlier build that recorded nothing")
check(agentPlan(facts(.enabled, now: nil)), [], "an unreadable daemon is no evidence of a change")
check(agentPlan(facts(.requiresApproval, now: "h2")), [.needsApproval],
      "turned off in Login Items: never registered over, even after an upgrade")
check(agentPlan(facts(legacy: true, .notRegistered)), [.removeLegacy, .register],
      "an M13 agent goes before registering the same label")
check(agentPlan(facts(config: false, legacy: true, .notRegistered)), [.writeConfig, .removeLegacy, .register],
      "all three, in order")
check(agentPlan(facts(legacy: true, .enabled)), [.removeLegacy, .reregister],
      "removing an M13 agent stops ours too, unchanged daemon or not")

check(problemWhileWaiting(nil, requiresApproval: true), .needsApproval, "switched off with the window open")
check(problemWhileWaiting(.needsApproval, requiresApproval: false), nil, "switched back on: wait for it again")
check(problemWhileWaiting(.firstRun("x"), requiresApproval: false), .firstRun("x"), "a launch problem stays")
check(problemWhileWaiting(.failed("x"), requiresApproval: true), .needsApproval, "the switch explains more than a failure")

check(leaveDaemonPage(showingDaemon: true, wasOff: false, isOff: true), true, "switched off under the page: leave it")
check(leaveDaemonPage(showingDaemon: true, wasOff: true, isOff: true), false,
      "still off, and a daemon answering anyway: no reload loop")
check(leaveDaemonPage(showingDaemon: false, wasOff: false, isOff: true), false, "already waiting: the probe handles it")
check(leaveDaemonPage(showingDaemon: true, wasOff: false, isOff: false), false, "on: nothing")

check(legacyPlistPath(home: "/Users/x"), "/Users/x/Library/LaunchAgents/org.billmill.brightlantern.plist",
      "service.go's plist")

let m = "__m__"
check(shellVariable("\n\(m)/Users/x/.cc", marker: m), "/Users/x/.cc", "the value after the marker")
check(shellVariable("Welcome!\nlast login\n\(m)/a b\n", marker: m), "/a b", "rc noise before it, newline after")
check(shellVariable("\n\(m)", marker: m), nil, "unset")
check(shellVariable("killed before printing", marker: m), nil, "no marker")

check(problemPage(.needsApproval).contains("href=\"\(loginItemsURL)\""), true,
      "the approval page links where the delegate intercepts")
check(problemPage(.firstRun("<no sessions>")).contains("<no sessions>"), false, "init's message is escaped")
check(problemPage(.failed("boom")).contains("boom"), true, "a failure says what failed")

if failures > 0 {
    print("\(failures) failed")
    exit(1)
}
print("ok")
