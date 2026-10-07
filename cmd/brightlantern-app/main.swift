// Bright Lantern.app: a window showing the daemon's web interface.
//
// A client, not a host. The LaunchAgent from M13 is already running and
// already indexing; this shows it without browser chrome. Nothing here
// starts, stops or finds the daemon beyond reading where it listens -- see
// address.swift.
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

        web.load(URLRequest(url: url))
    }

    // One window, so closing it is quitting. Quitting is the app and nothing
    // else: the daemon belongs to launchd and keeps indexing.
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    // MARK: View menu

    // reload() alone does nothing after a load that never connected, because
    // there is no current page to reload -- and "the daemon was down and is
    // back" is the case Reload exists for (#73).
    @objc func reloadPage(_ sender: Any?) {
        if web.url == nil {
            web.load(URLRequest(url: url))
        } else {
            web.reload()
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

    // Failures are logged and not handled; #75 owns what the window shows
    // when the daemon is down. What is needed now is attribution: a blocked
    // load and a dead daemon leave the same blank window, and the error code
    // is the only thing that tells them apart.
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        report(error)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        report(error)
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
