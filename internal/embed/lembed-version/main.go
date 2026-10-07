// Command lembed-version prints the version of a sqlite-lembed extension and
// fails unless it is the landrix fork.
//
// mise-tasks/setup runs it to check what it built. It used to do that from
// python3, which only works with a Python whose sqlite3 module allows loading
// extensions: Homebrew's does, and macOS's and mise's do not. Go is already a
// requirement, and this loads the extension into the SQLite brightlantern
// actually links.
package main

import (
	"fmt"
	"os"

	"github.com/llimllib/brightlantern/internal/embed"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: lembed-version EXTENSION")
		os.Exit(2)
	}
	ext := os.Args[1]
	v, err := embed.ExtensionVersion(ext)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("lembed:", v)
	if !embed.IsForkVersion(v) {
		fmt.Fprintf(os.Stderr, "ERROR: %s is not the patched fork; run 'rm %s' and retry\n", ext, ext)
		os.Exit(1)
	}
}
