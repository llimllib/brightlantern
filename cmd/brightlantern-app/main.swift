// Bright Lantern.app: a window showing the daemon's web interface.
//
// A client, not a host. The LaunchAgent from M13 is already running and
// already indexing; this shows it without browser chrome. Nothing here
// starts or stops the daemon: address.swift finds where it listens, and
// startup.swift decides what to say while it is not answering.
//
// Built with plain swiftc from Command Line Tools: `mise run app`, into
// build/Bright Lantern.app. docs/plans/2026-10-06-wkwebview-shell-design.md
// has the reasoning.

import AppKit
import WebKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate {
    let url: URL
    var window: NSWindow!
    var web: WKWebView!

    init(url: URL) {
        self.url = url
    }

    // Before launch finishes, so the menu exists by the time the first key
    // equivalent could arrive.
    func applicationWillFinishLaunching(_ notification: Notification) {
        NSApp.mainMenu = mainMenu(target: self)
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.navigationDelegate = self
        web.pageZoom = savedZoom()

        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1100, height: 800),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false)
        window.title = "Bright Lantern"
        window.contentView = web
        window.center()
        // After center(), because this restores the saved frame when there is
        // one: center() places a first launch, and every later launch puts the
        // window back where it was.
        window.setFrameAutosaveName("main")
        window.makeKeyAndOrderFront(nil)
        NSApp.activate()

        startProbing()
    }

    // MARK: Waiting for the daemon (#75)

    // Each probe loop carries the generation it was started in, and stops
    // when a newer one begins, so Reload or a failed load can restart the
    // wait without leaving a second loop running behind it.
    private var probeGeneration = 0
    private var probeStarted = Date()
    private var shown = Startup.quiet

    private func startProbing() {
        probeGeneration += 1
        probeStarted = Date()
        shown = .quiet
        probe(probeGeneration)
    }

    private func probe(_ generation: Int) {
        var request = URLRequest(url: statusURL(for: url))
        request.cachePolicy = .reloadIgnoringLocalCacheData
        // Long, because serve binds its port before it is ready to answer:
        // a connection that is accepted and then waits is a daemon starting
        // up, not a missing one. A refused connection fails at once whatever
        // this says.
        request.timeoutInterval = 10
        URLSession.shared.dataTask(with: request) { _, response, _ in
            let answered = response != nil
            Task { @MainActor in self.probed(generation, answered: answered) }
        }.resume()
    }

    private func probed(_ generation: Int, answered: Bool) {
        guard generation == probeGeneration else { return }
        let state = startup(answered: answered, elapsed: Date().timeIntervalSince(probeStarted))
        if state == .ready {
            web.load(URLRequest(url: url))
            return
        }
        // Only on a change: reloading the same page every half second would
        // flicker and lose any text someone had selected to copy.
        if state != shown, let page = startupPage(state, address: displayAddress, log: logPath()) {
            web.loadHTMLString(page, baseURL: nil)
        }
        shown = state
        // Quickly while it is plausibly starting, so the page appears as soon
        // as it can; slowly once it has been long enough to be stuck.
        let interval: Duration = state == .stuck ? .seconds(2) : .milliseconds(500)
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: interval)
            self?.probe(generation)
        }
    }

    private var displayAddress: String {
        let host = url.host(percentEncoded: false) ?? url.absoluteString
        return url.port.map { "\(host):\($0)" } ?? host
    }

    // Whether the web view is showing the daemon, as opposed to one of the
    // startup pages, which load with no URL of their own.
    private var showingDaemon: Bool {
        guard let current = web.url else { return false }
        return current.scheme == url.scheme && current.host == url.host && current.port == url.port
    }

    // One window, so closing it is quitting. Quitting is the app and nothing
    // else: the daemon belongs to launchd and keeps indexing.
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    // MARK: View menu

    // reload() alone would reload a startup page, or nothing at all after a
    // load that never connected -- and "the daemon was down and is back" is
    // the case Reload exists for (#73). Away from the daemon, Reload starts
    // the wait over, which loads it at once if it is there.
    @objc func reloadPage(_ sender: Any?) {
        if showingDaemon {
            web.reload()
        } else {
            startProbing()
        }
    }

    // Safari's steps, so that zooming in and back out lands on the size it
    // started from rather than drifting by multiplication.
    private static let zoomSteps: [CGFloat] = [0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3]
    private static let zoomKey = "pageZoom"

    @objc func actualSize(_ sender: Any?) { setZoom(1) }

    @objc func zoomIn(_ sender: Any?) {
        if let next = Self.zoomSteps.first(where: { $0 > web.pageZoom + 0.001 }) {
            setZoom(next)
        }
    }

    @objc func zoomOut(_ sender: Any?) {
        if let next = Self.zoomSteps.last(where: { $0 < web.pageZoom - 0.001 }) {
            setZoom(next)
        }
    }

    // Remembered like the window frame, and for the same reason: a setting
    // that resets on every launch is one people stop using.
    private func setZoom(_ zoom: CGFloat) {
        web.pageZoom = zoom
        UserDefaults.standard.set(Double(zoom), forKey: Self.zoomKey)
    }

    private func savedZoom() -> CGFloat {
        let z = UserDefaults.standard.double(forKey: Self.zoomKey)
        return Self.zoomSteps.contains(CGFloat(z)) ? CGFloat(z) : 1
    }

    // MARK: Failed loads

    // A daemon that answered the probe can still be gone by the time the
    // page loads, or go away under a page someone is reading and then be
    // clicked on. Either is the wait again rather than WebKit's error page.
    // Everything else is logged, because a blocked load and a dead daemon
    // otherwise leave the same blank window and the code tells them apart.
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        failed(error)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        failed(error)
    }

    private func failed(_ error: Error) {
        report(error)
        let e = error as NSError
        if e.domain == NSURLErrorDomain
            && (e.code == NSURLErrorCannotConnectToHost || e.code == NSURLErrorNetworkConnectionLost) {
            startProbing()
        }
    }

    private func report(_ error: Error) {
        let e = error as NSError
        let why: String
        switch (e.domain, e.code) {
        case (NSURLErrorDomain, NSURLErrorAppTransportSecurityRequiresSecureConnection):
            why = "blocked by App Transport Security, which Info.plist makes no exception for (#72)"
        case (NSURLErrorDomain, NSURLErrorCannotConnectToHost):
            why = "nothing is listening there; is the brightlantern daemon running?"
        default:
            why = e.localizedDescription
        }
        // NSLog rather than print: it reaches stderr when run from a
        // terminal and the unified log when launched from the Dock, where
        // stderr goes nowhere.
        NSLog("loading %@: %@ (%@ %d)", url.absoluteString, why, e.domain, e.code)
    }
}

let config = try? String(contentsOfFile: configPath(), encoding: .utf8)
let url = resolveURL(arguments: CommandLine.arguments, config: config) { NSLog("%@", $0) }

// Top-level code runs on the main thread but is not main-actor isolated,
// and AppKit is. assumeIsolated says the first, and traps if it is ever false.
MainActor.assumeIsolated {
    let app = NSApplication.shared
    // NSApplication holds its delegate weakly. run() does not return until
    // the app quits, so this local outlives everything that uses it.
    let delegate = AppDelegate(url: url)
    app.delegate = delegate
    app.run()
}
