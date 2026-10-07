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

if failures > 0 {
    print("\(failures) failed")
    exit(1)
}
print("ok")
