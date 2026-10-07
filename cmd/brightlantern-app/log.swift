// Where the app says what it did.
//
// os.Logger rather than NSLog. NSLog was assumed to reach the unified log
// from an app opened in Finder, and measured, it does not: registering the
// agent logged six lines that `log show` never had (#74). A Logger with the
// bundle identifier as its subsystem does, and filters on one predicate:
//
//     log show --last 10m --predicate 'subsystem == "org.billmill.brightlantern"'
//
// Notice, the default level, because `log show` skips info and debug unless
// asked. Public, because everything here is a path or an error and none of it
// worth redacting -- private would print <private> in the one place anyone
// will read it. Echoed to stderr when there is a terminal to read it.

import Foundation
import os

private let logger = Logger(subsystem: "org.billmill.brightlantern", category: "app")

func say(_ message: String) {
    logger.notice("\(message, privacy: .public)")
    if isatty(STDERR_FILENO) != 0 {
        FileHandle.standardError.write(Data((message + "\n").utf8))
    }
}
