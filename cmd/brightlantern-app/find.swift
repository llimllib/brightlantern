// Find in this session: the Edit menu's Find items, which the page carries
// out. The bar is the page's (layout.html, app.ts): it sits at the foot of
// the reading pane and is as wide as it, which a native view laid over the
// web view could only match by asking the page where the pane was after
// every resize, zoom, and change of the list's width.
//
// NSTextFinder, which draws Safari's bar and which WKWebView can privately
// serve, was tried first and fell short three ways that none of its options
// reach: it searched the whole page, session list included; it scrolled only
// the document, which never scrolls here, so cmd-G moved to matches out of
// sight; and its bar shows a total but never which match is current.

import WebKit

// Calls one of window.brightlanternFind's methods. A page without it -- a
// startup page, or a daemon older than the app -- answers false, and the key
// does nothing; the log says why.
@MainActor
func pageFind(_ web: WKWebView, _ method: String) {
    let body = """
        const find = window.brightlanternFind;
        if (!find) return false;
        find.\(method)();
        return true;
        """
    web.callAsyncJavaScript(body, arguments: [:], in: nil, in: .page) { result in
        switch result {
        case .success(let value):
            if value as? Bool != true {
                say("find: the page has no brightlanternFind; is the daemon older than the app?")
            }
        case .failure(let error):
            // say() rather than print: see log.swift.
            say("find: \(method): \(error)")
        }
    }
}
