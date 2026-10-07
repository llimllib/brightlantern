package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// logTo points stdout and stderr at a file, for serve --log.
//
// The agent the app registers needs it (#74). Its plist ships inside the
// signed bundle, so it cannot name a path in the user's home, and launchd does
// not expand ~ in StandardOutPath. So the plist passes a ~ path as an
// argument, and this expands it.
//
// The descriptors themselves, not os.Stdout and os.Stderr: a panic and
// anything cgo prints -- llama.cpp is chatty on failure -- write to fd 2
// directly, and those are the lines a log is for.
func logTo(path string) error {
	path, err := expandHome(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, fd := range []int{1, 2} {
		if err := unix.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}
