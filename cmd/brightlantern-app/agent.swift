// What the app does about the LaunchAgent at launch (#74).
//
// The app registers the daemon inside its own bundle to run at login, so that
// nobody has to open a terminal for Bright Lantern to keep its index. This is
// the decision; register.swift carries it out. Free of AppKit and
// ServiceManagement, like address.swift, so `mise run app-test` can cover
// every branch -- registration itself can only be tried in a login session.
// docs/plans/2026-10-07-register-agent-design.md has the reasoning.

import CryptoKit
import Foundation

// AgentStatus is SMAppService.Status, mirrored so this file needs nothing
// from ServiceManagement.
enum AgentStatus: Equatable {
    case notRegistered
    case enabled
    case requiresApproval
    case notFound
}

enum AgentStep: Equatable {
    // Write config.toml with the bundled `brightlantern init`. The daemon
    // must not be the first run: under launchd it would detect session
    // directories in launchd's environment and write that down for good.
    case writeConfig
    // An M13 `brightlantern service install` agent holds the same label.
    // Replaced rather than asked about: same job, and nobody who cannot use
    // a terminal could answer the question.
    case removeLegacy
    case register
    // The daemon changed since it was registered. SMAppService.h: such an
    // agent "must be re-registered or it may not launch", and it recommends
    // unregistering first.
    case reregister
    // Someone turned it off in Login Items. Registering over that would
    // overrule them; the window says so and offers to open Login Items.
    case needsApproval
}

// AgentProblem is what the window says instead of "starting" when the agent
// cannot run. The probe carries on regardless, so a daemon started some other
// way still loads.
enum AgentProblem: Equatable {
    // `brightlantern init` failed, almost always because there are no
    // sessions to index. Its message, which names where it looked.
    case firstRun(String)
    case needsApproval
    case failed(String)
}

struct AgentFacts {
    var hasConfig: Bool
    var legacyInstalled: Bool
    var status: AgentStatus
    // daemonHash is the bundled daemon's now; registeredHash is what it was
    // at the last registration, nil before there was one.
    var daemonHash: String?
    var registeredHash: String?
}

// agentPlan lists the steps in order. Running them stops at the first that
// fails: an agent with no config to read is the thing writeConfig prevents.
func agentPlan(_ f: AgentFacts) -> [AgentStep] {
    var steps: [AgentStep] = []
    if !f.hasConfig {
        steps.append(.writeConfig)
    }
    if f.legacyInstalled {
        steps.append(.removeLegacy)
    }
    switch f.status {
    case .notRegistered, .notFound:
        steps.append(.register)
    case .enabled:
        // The status is SMAppService's, but the job launchd has loaded under
        // the shared label may be the M13 one, and removing that leaves
        // nothing running until a login.
        if f.legacyInstalled {
            steps.append(.reregister)
        // A hash that cannot be read is no evidence of a change, and
        // re-registering restarts the daemon.
        } else if let now = f.daemonHash, now != f.registeredHash {
            steps.append(.reregister)
        }
    case .requiresApproval:
        steps.append(.needsApproval)
    }
    return steps
}

// fileHash identifies the daemon executable, which is what has to change for
// re-registration to matter. Not CFBundleVersion: `mise run app` rebuilds the
// daemon without changing any version, and that is the case seen most.
func fileHash(_ url: URL) -> String? {
    guard let data = try? Data(contentsOf: url, options: .mappedIfSafe) else { return nil }
    return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

// legacyPlistPath is service.go's servicePaths plist.
func legacyPlistPath(home: String = NSHomeDirectory()) -> String {
    home + "/Library/LaunchAgents/org.billmill.brightlantern.plist"
}

// shellVariable pulls one variable out of a login shell's output, which may
// carry anything the user's rc files print before the marker.
func shellVariable(_ output: String, marker: String) -> String? {
    guard let r = output.range(of: marker, options: .backwards) else { return nil }
    let value = output[r.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
    return value.isEmpty ? nil : value
}
