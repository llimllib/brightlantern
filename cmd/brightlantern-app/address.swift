// Where the app finds the daemon: --url, then config.toml's addr, then the
// default. Kept apart from main.swift, and free of AppKit, so that
// `mise run app-test` can exercise it without a window.
//
// The argument is an override and not the mechanism. Dock, Finder and
// Spotlight launch through launchd and cannot pass arguments, so an app that
// needed one would work from a terminal and nowhere else. It is there for
// pointing the app at `mise run dev`'s :5269 beside the real agent.

import Foundation

// defaultAddr is config.DefaultAddr in internal/config/config.go. Written
// twice rather than generated: one constant that has never changed is not
// worth a codegen step, and every localhost client does the same -- Docker's
// each carry their own /var/run/docker.sock.
let defaultAddr = "127.0.0.1:5268"

// ConfigAddr is what config.toml says about addr.
enum ConfigAddr: Equatable {
    case found(String)
    case absent
    // A line that looks like addr but is not a form this parser reads. Worth
    // saying so, because the alternative is quietly using the default while
    // someone's edit is sitting in the file.
    case unreadable(String)
}

// configAddr reads addr out of config.toml's text.
//
// Deliberately not a TOML parser. internal/config writes this file, and
// writes addr as one basic string on one line; this reads that, plus a
// literal string and a trailing comment for anyone editing by hand, and
// declines everything else rather than guessing at it.
func configAddr(_ text: String) -> ConfigAddr {
    for raw in text.split(separator: "\n", omittingEmptySubsequences: false) {
        let line = raw.trimmingCharacters(in: .whitespaces)
        // A table header ends the top level, and addr is a top-level key.
        if line.hasPrefix("[") {
            return .absent
        }
        guard line.hasPrefix("addr") else { continue }
        var rest = line.dropFirst("addr".count).drop(while: { $0 == " " || $0 == "\t" })
        // "address = ..." is some other key.
        guard rest.first == "=" else { continue }
        rest = rest.dropFirst().drop(while: { $0 == " " || $0 == "\t" })

        guard let quote = rest.first, quote == "\"" || quote == "'" else {
            return .unreadable(line)
        }
        let body = rest.dropFirst()
        guard let end = body.firstIndex(of: quote) else {
            return .unreadable(line)
        }
        let value = String(body[..<end])
        // An escape in a basic string means a value config.go would never
        // write for an address. Unescaping it properly is a TOML parser.
        if quote == "\"" && value.contains("\\") {
            return .unreadable(line)
        }
        let after = body[body.index(after: end)...].trimmingCharacters(in: .whitespaces)
        guard after.isEmpty || after.hasPrefix("#") else {
            return .unreadable(line)
        }
        return value.isEmpty ? .absent : .found(value)
    }
    return .absent
}

// dialable is cmd/brightlantern's dialable: ":5268" and "0.0.0.0:5268" are
// fine to listen on and mean nothing to connect to, and the server they
// describe answers on loopback either way.
func dialable(_ addr: String) -> String {
    guard let colon = addr.lastIndex(of: ":") else { return addr }
    var host = String(addr[..<colon])
    let port = addr[addr.index(after: colon)...]
    if host.hasPrefix("[") && host.hasSuffix("]") {
        host = String(host.dropFirst().dropLast())
    }
    if host.isEmpty || host == "0.0.0.0" || host == "::" {
        host = "127.0.0.1"
    }
    return host.contains(":") ? "[\(host)]:\(port)" : "\(host):\(port)"
}

// configPath is internal/config's Path().
func configPath(environment: [String: String] = ProcessInfo.processInfo.environment) -> String {
    if let d = environment["XDG_CONFIG_HOME"], !d.isEmpty {
        return d + "/brightlantern/config.toml"
    }
    return NSHomeDirectory() + "/.config/brightlantern/config.toml"
}

// resolveURL picks the URL to load, and says why when it falls back.
//
// config is the settings file's text, nil when there is no file, which is
// not an error: it is the state of every machine before brightlantern first
// runs, and the default is the right answer then.
func resolveURL(arguments: [String], config: String?, note: (String) -> Void) -> URL {
    if let i = arguments.firstIndex(of: "--url") {
        if i + 1 < arguments.count,
           let url = URL(string: arguments[i + 1]),
           url.scheme == "http" || url.scheme == "https",
           url.host != nil {
            return url
        }
        note("--url wants an http URL, like http://127.0.0.1:5269/; ignoring it")
    }

    var addr = defaultAddr
    if let config {
        switch configAddr(config) {
        case .found(let a):
            addr = a
        case .absent:
            break
        case .unreadable(let line):
            note("config.toml: cannot read \(line); using \(defaultAddr)")
        }
    }
    if let url = URL(string: "http://\(dialable(addr))/"), url.host != nil {
        return url
    }
    note("config.toml: addr \(addr) is not a host and port; using \(defaultAddr)")
    return URL(string: "http://\(defaultAddr)/")!
}
