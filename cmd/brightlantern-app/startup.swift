// What the window says while the daemon is not answering (#75).
//
// The app does not start the daemon, and sometimes it is not there: starting
// at login, restarted by launchd after a crash -- which launchd throttles to
// once every ten seconds -- or not running at all. Without this the window
// shows WebKit's error page about a refused connection, which names neither
// Bright Lantern nor anything to do about it.
//
// Free of AppKit, like address.swift, so `mise run app-test` can run it.

import Foundation

enum Startup: Equatable {
    // Not answering yet, and too soon to say so: the daemon is usually up
    // within a moment, and a message that flashes on every launch teaches
    // people to ignore it.
    case quiet
    case starting
    // Long enough that waiting will not fix it. Probing carries on -- it is
    // a loopback request, and loading the moment the daemon appears is better
    // than making someone notice and press Reload -- but the window stops
    // saying "starting" and says where to look instead.
    case stuck
    case ready
}

// grace and stuckAfter, in seconds. Two seconds is noteSlowModelLoad's grace
// in cmd/brightlantern, for the same reason. Thirty is #75's: past launchd's
// ten-second throttle with room to spare.
let startupGrace: TimeInterval = 2
let startupStuckAfter: TimeInterval = 30

func startup(answered: Bool, elapsed: TimeInterval) -> Startup {
    if answered { return .ready }
    if elapsed < startupGrace { return .quiet }
    if elapsed < startupStuckAfter { return .starting }
    return .stuck
}

// statusURL is what the probe asks. /status is what the page's header polls,
// so it exists on every daemon that can serve a page; any HTTP response at
// all means something is listening, which is all the probe needs to know.
func statusURL(for page: URL) -> URL {
    URL(string: "/status", relativeTo: page)!.absoluteURL
}

// logPath is service.go's servicePaths log, for the one case where the app
// can say something useful about a daemon it does not own.
func logPath(home: String = NSHomeDirectory()) -> String {
    home + "/Library/Logs/brightlantern/brightlantern.log"
}

// startupPage renders a state as a page for the web view to show. HTML
// rather than native views because the window already holds a web view, and
// because it keeps these pages in the same place as everything else the
// window shows. nil for the states that show nothing new.
func startupPage(_ state: Startup, address: String, log: String) -> String? {
    let body: String
    switch state {
    case .quiet, .ready:
        return nil
    case .starting:
        body = """
            \(lantern)
            <h1>Waiting for Bright Lantern to start</h1>
            <p>Expecting it at \(escape(address)).</p>
            """
    case .stuck:
        body = """
            <h1>Bright Lantern is not answering</h1>
            <p>Nothing has answered at \(escape(address)) for thirty seconds.
            This window will load the moment something does.</p>
            <p>Its log is <code>\(escape(log))</code>.</p>
            """
    }
    return page(body)
}

// lantern is the starting page's animation (#88): a photophore, the
// lanternfish's light, breathing, with rings rippling out from it and motes
// drifting up past it, as in deep water. The wait it covers can be the agent
// being registered, launchd spawning the daemon and ~15s of Metal shaders,
// and a still line of text reads as possibly stuck.
//
// CSS only: the page loads with no base URL, so there is nowhere to fetch
// from. It runs across probes because startupPage only reloads when the HTML
// changes, so nothing here may vary per probe. It fades in, because the page
// itself appears only after a grace. Still under prefers-reduced-motion, and
// absent from the stuck page, where movement would say "working" about
// something that is not.
let lantern = """
    <div class="lantern" aria-hidden="true">
      <span class="ring"></span><span class="ring"></span><span class="ring"></span>
      <span class="glow"></span>
      <span class="mote"></span><span class="mote"></span><span class="mote"></span>
    </div>
    """

private let lanternStyle = """
    .lantern { position: relative; width: 120px; height: 120px; margin: 0 0 1.25em;
               animation: lantern-in 1.2s ease-out both; }
    .lantern span { position: absolute; border-radius: 50%; }
    .lantern .glow { inset: 40px;
      background: radial-gradient(circle, #fff7df 0%, #ffd681 35%, #f5a623 62%, rgba(245,166,35,0) 74%);
      box-shadow: 0 0 28px 10px rgba(245,166,35,0.4);
      animation: lantern-breathe 3.6s ease-in-out infinite; }
    .lantern .ring { inset: 40px; border: 1.5px solid rgba(245,166,35,0.6); opacity: 0;
      animation: lantern-ripple 3.6s ease-out infinite; }
    .lantern .ring:nth-child(2) { animation-delay: 1.2s; }
    .lantern .ring:nth-child(3) { animation-delay: 2.4s; }
    .lantern .mote { width: 3px; height: 3px; bottom: 14px; opacity: 0;
      background: rgba(255,214,140,0.9); animation: lantern-drift 5.4s linear infinite; }
    .lantern .mote:nth-of-type(5) { left: 30px; }
    .lantern .mote:nth-of-type(6) { left: 78px; animation-delay: 1.8s; }
    .lantern .mote:nth-of-type(7) { left: 92px; animation-delay: 3.6s; }
    @keyframes lantern-in { from { opacity: 0; } to { opacity: 1; } }
    @keyframes lantern-breathe {
      0%, 100% { transform: scale(0.9); opacity: 0.8; }
      50% { transform: scale(1.06); opacity: 1; } }
    @keyframes lantern-ripple {
      0% { transform: scale(1); opacity: 0.7; }
      100% { transform: scale(2.7); opacity: 0; } }
    @keyframes lantern-drift {
      0% { transform: translateY(0); opacity: 0; }
      15% { opacity: 0.9; }
      100% { transform: translateY(-96px); opacity: 0; } }
    @media (prefers-reduced-motion: reduce) {
      .lantern, .lantern span { animation: none; }
      .lantern .ring, .lantern .mote { display: none; } }
    """

// loginItemsURL is what the approval page's button navigates to. The web
// view's delegate cancels it and opens Login Items instead.
let loginItemsURL = "brightlantern:login-items"

// problemPage says why the agent is not running, instead of "starting" (#74).
func problemPage(_ problem: AgentProblem) -> String {
    let body: String
    switch problem {
    case .firstRun(let message):
        body = """
            <h1>Bright Lantern found no sessions</h1>
            <p>It looks for Claude Code and pi sessions on this Mac, and
            found none. Once there are some, open Bright Lantern again.</p>
            <pre>\(escape(message))</pre>
            """
    case .needsApproval:
        body = """
            <h1>Bright Lantern is turned off in System Settings</h1>
            <p>It needs to run in the background to keep its index of your
            sessions current. Turn it back on under Background App Activity,
            and this window will load.</p>
            <p><a href="\(loginItemsURL)">Open System Settings</a></p>
            """
    case .failed(let message):
        body = """
            <h1>Bright Lantern could not start running in the background</h1>
            <pre>\(escape(message))</pre>
            """
    }
    return page(body)
}

private func page(_ body: String) -> String {
    """
        <!doctype html>
        <meta charset="utf-8">
        <style>
          :root { color-scheme: light dark; }
          body { font: 15px -apple-system, sans-serif; margin: 20vh auto 0;
                 max-width: 32em; padding: 0 2em; line-height: 1.5; }
          h1 { font-size: 1.3em; font-weight: 600; }
          p, code, pre { opacity: 0.8; }
          pre { white-space: pre-wrap; font-size: 0.85em; }
        \(lanternStyle)
        </style>
        \(body)
        """
}

func escape(_ s: String) -> String {
    s.replacingOccurrences(of: "&", with: "&amp;")
        .replacingOccurrences(of: "<", with: "&lt;")
        .replacingOccurrences(of: ">", with: "&gt;")
        .replacingOccurrences(of: "\"", with: "&quot;")
}
