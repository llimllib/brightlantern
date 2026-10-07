// Carrying out agent.swift's plan (#74).
//
// Run once per launch, off the main thread, while the window waits for the
// daemon as it always has. A problem comes back to be shown in the window;
// everything else goes to say(), in log.swift.

import Foundation
import ServiceManagement

let agentPlistName = "org.billmill.brightlantern.plist"
private let registeredHashKey = "registeredDaemonHash"

func ensureAgent() async -> AgentProblem? {
    guard let daemon = Bundle.main.url(forAuxiliaryExecutable: "brightlantern") else {
        // Run from somewhere other than a bundle `mise run app` built.
        say("agent: no brightlantern in \(Bundle.main.bundlePath); not registering")
        return nil
    }
    let service = SMAppService.agent(plistName: agentPlistName)
    let hash = fileHash(daemon)
    let facts = AgentFacts(
        hasConfig: FileManager.default.fileExists(atPath: configPath()),
        legacyInstalled: FileManager.default.fileExists(atPath: legacyPlistPath()),
        status: agentStatus(service.status),
        daemonHash: hash,
        registeredHash: UserDefaults.standard.string(forKey: registeredHashKey))

    for step in agentPlan(facts) {
        say("agent: \(step)")
        switch step {
        case .writeConfig:
            var env = ProcessInfo.processInfo.environment
            if env["CLAUDE_CONFIG_DIR"] == nil, let dir = loginShellVariable("CLAUDE_CONFIG_DIR") {
                env["CLAUDE_CONFIG_DIR"] = dir
            }
            let (ok, output) = run(daemon, ["init"], environment: env)
            if !ok {
                return .firstRun(output)
            }
        case .removeLegacy:
            // Not fatal: register says so if the label is still taken.
            let (ok, output) = run(daemon, ["service", "uninstall"])
            if !ok {
                say("agent: removing the M13 agent: \(output)")
            }
        case .register, .reregister:
            if step == .reregister {
                do { try await service.unregister() } catch {
                    say("agent: unregister: \(error.localizedDescription)")
                }
            }
            do {
                try await registerRetrying(service)
            } catch {
                // Turned off in Login Items between the status check and
                // here, or never allowed: the same answer either way.
                if service.status == .requiresApproval {
                    return .needsApproval
                }
                // The domain and code, because the description is generic --
                // "Operation not permitted" covers several SMAppService
                // refusals -- and the code is what tells them apart.
                let e = error as NSError
                let detail = "\(e.localizedDescription) (\(e.domain) \(e.code); status \(service.status.rawValue))"
                say("agent: register: \(detail) \(e.userInfo)")
                return .failed(detail)
            }
            UserDefaults.standard.set(hash, forKey: registeredHashKey)
            if service.status == .requiresApproval {
                return .needsApproval
            }
        case .needsApproval:
            return .needsApproval
        }
    }
    return nil
}

// registerRetrying retries a refused registration a few times, a second
// apart. Seen once, straight after removing an M13 agent: `register()` threw
// "Operation not permitted" while the bootout of the label they share was
// still finishing, and the next launch registered without complaint.
private func registerRetrying(_ service: SMAppService) async throws {
    var attempt = 1
    while true {
        do {
            try service.register()
            return
        } catch {
            // The user's switch, not a race: retrying would not change it.
            if attempt == 4 || service.status == .requiresApproval {
                throw error
            }
            say("agent: register attempt \(attempt): \(error.localizedDescription); retrying")
            attempt += 1
            try? await Task.sleep(for: .seconds(1))
        }
    }
}

// agentRequiresApproval asks whether the agent is switched off in System
// Settings right now. A local XPC call, cheap enough for every failed probe.
func agentRequiresApproval() -> Bool {
    SMAppService.agent(plistName: agentPlistName).status == .requiresApproval
}

private func agentStatus(_ s: SMAppService.Status) -> AgentStatus {
    switch s {
    case .notRegistered: return .notRegistered
    case .enabled: return .enabled
    case .requiresApproval: return .requiresApproval
    case .notFound: return .notFound
    @unknown default: return .notFound
    }
}

// run starts the bundled daemon and waits, returning stderr and stdout
// together: init's error and its notes are both on stderr.
private func run(_ exe: URL, _ args: [String], environment: [String: String]? = nil) -> (Bool, String) {
    let p = Process()
    p.executableURL = exe
    p.arguments = args
    if let environment { p.environment = environment }
    let out = Pipe()
    p.standardOutput = out
    p.standardError = out
    p.standardInput = FileHandle.nullDevice
    do { try p.run() } catch {
        return (false, error.localizedDescription)
    }
    // Before waiting: a child that fills the pipe would otherwise never exit.
    let data = out.fileHandleForReading.readDataToEndOfFile()
    p.waitUntilExit()
    let text = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    return (p.terminationStatus == 0, text)
}

// loginShellVariable asks the user's shell for a variable, because an app
// opened from Finder has launchd's environment and not the one a terminal
// would give it. Interactive as well as login, because .zshrc is where most
// people export things. Behind a marker, since rc files can print, and a
// timeout, since one can also wait for input.
private func loginShellVariable(_ name: String) -> String? {
    var shell = "/bin/zsh"
    if let pw = getpwuid(getuid()), let s = pw.pointee.pw_shell, s.pointee != 0 {
        shell = String(cString: s)
    }
    let marker = "__brightlantern_value__"
    let p = Process()
    p.executableURL = URL(fileURLWithPath: shell)
    p.arguments = ["-l", "-i", "-c", "printf '\\n\(marker)%s' \"$\(name)\""]
    let out = Pipe()
    p.standardOutput = out
    p.standardError = FileHandle.nullDevice
    p.standardInput = FileHandle.nullDevice
    do { try p.run() } catch { return nil }
    let timer = DispatchWorkItem { if p.isRunning { p.terminate() } }
    DispatchQueue.global().asyncAfter(deadline: .now() + 5, execute: timer)
    let data = out.fileHandleForReading.readDataToEndOfFile()
    p.waitUntilExit()
    timer.cancel()
    return shellVariable(String(decoding: data, as: UTF8.self), marker: marker)
}
