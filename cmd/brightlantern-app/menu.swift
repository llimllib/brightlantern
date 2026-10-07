// The menu bar (#73).
//
// Not decoration. An app built from a bare main.swift has no menu unless one
// is made, and cmd-Q, cmd-W and cmd-C are menu item key equivalents rather
// than anything AppKit provides for free -- without this the app could not be
// quit from the keyboard, and nothing could be copied out of a transcript.
//
// The Edit items have no target: they go down the responder chain, where the
// focused WKWebView handles them, including in the page's search field. The
// View items target the delegate, which owns the web view.
//
// No Settings item yet. It belongs to the first-run picker (#76), and an item
// that opens nothing is worse than no item.

import AppKit

@MainActor
func mainMenu(target: AppDelegate) -> NSMenu {
    let bar = NSMenu()

    // AppKit titles the application menu with CFBundleName whatever it is
    // given here; the items name the app themselves.
    let app = submenu(of: bar, titled: "Bright Lantern")
    app.addItem(item("About Bright Lantern", #selector(NSApplication.orderFrontStandardAboutPanel(_:))))
    app.addItem(.separator())
    app.addItem(item("Hide Bright Lantern", #selector(NSApplication.hide(_:)), "h"))
    app.addItem(item("Hide Others", #selector(NSApplication.hideOtherApplications(_:)), "h", [.command, .option]))
    app.addItem(item("Show All", #selector(NSApplication.unhideAllApplications(_:))))
    app.addItem(.separator())
    // Quits the app and nothing else. The daemon is launchd's, not ours, and
    // goes on indexing with no window open -- which is the point of M13.
    app.addItem(item("Quit Bright Lantern", #selector(NSApplication.terminate(_:)), "q"))

    let file = submenu(of: bar, titled: "File")
    file.addItem(item("Close Window", #selector(NSWindow.performClose(_:)), "w"))

    let edit = submenu(of: bar, titled: "Edit")
    edit.addItem(item("Undo", Selector(("undo:")), "z"))
    edit.addItem(item("Redo", Selector(("redo:")), "z", [.command, .shift]))
    edit.addItem(.separator())
    edit.addItem(item("Cut", #selector(NSText.cut(_:)), "x"))
    edit.addItem(item("Copy", #selector(NSText.copy(_:)), "c"))
    edit.addItem(item("Paste", #selector(NSText.paste(_:)), "v"))
    edit.addItem(item("Select All", #selector(NSText.selectAll(_:)), "a"))

    let view = submenu(of: bar, titled: "View")
    view.addItem(item("Reload Page", #selector(AppDelegate.reloadPage(_:)), "r", target: target))
    view.addItem(.separator())
    view.addItem(item("Actual Size", #selector(AppDelegate.actualSize(_:)), "0", target: target))
    // "=" rather than "+": cmd-+ needs shift on most layouts, and cmd-= is
    // what people press.
    view.addItem(item("Zoom In", #selector(AppDelegate.zoomIn(_:)), "=", target: target))
    view.addItem(item("Zoom Out", #selector(AppDelegate.zoomOut(_:)), "-", target: target))
    view.addItem(.separator())
    view.addItem(item("Enter Full Screen", #selector(NSWindow.toggleFullScreen(_:)), "f", [.command, .control]))

    let window = submenu(of: bar, titled: "Window")
    window.addItem(item("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m"))
    window.addItem(item("Zoom", #selector(NSWindow.performZoom(_:))))
    // Lets AppKit list open windows here and add its own items.
    NSApplication.shared.windowsMenu = window

    return bar
}

@MainActor
private func submenu(of bar: NSMenu, titled title: String) -> NSMenu {
    let menu = NSMenu(title: title)
    let holder = NSMenuItem()
    holder.submenu = menu
    bar.addItem(holder)
    return menu
}

@MainActor
private func item(
    _ title: String, _ action: Selector, _ key: String = "",
    _ modifiers: NSEvent.ModifierFlags = .command, target: AnyObject? = nil
) -> NSMenuItem {
    let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
    item.keyEquivalentModifierMask = modifiers
    item.target = target
    return item
}
