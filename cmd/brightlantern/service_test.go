package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llimllib/brightlantern/internal/config"
)

// The plist is generated from a template with a path in it, so the test that
// matters is whether launchd's own parser accepts it -- including a path with
// characters XML has opinions about.
func TestAgentPlistIsValid(t *testing.T) {
	a := agent{
		Label:   serviceLabel,
		Program: "/Users/me/R&D <tools>/brightlantern",
		Home:    "/Users/me",
		Log:     "/Users/me/Library/Logs/brightlantern/brightlantern.log",
	}
	b, err := a.render()
	if err != nil {
		t.Fatal(err)
	}
	plist := string(b)
	for _, want := range []string{
		"<string>org.billmill.brightlantern</string>",
		"<string>/Users/me/R&amp;D &lt;tools&gt;/brightlantern</string>",
		"<string>serve</string>\n\t\t<string>--wait</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is missing %q:\n%s", want, plist)
		}
	}
	// Nothing secret belongs in a file that gets pasted into issues.
	if strings.Contains(plist, "EnvironmentVariables") {
		t.Error("plist sets environment variables")
	}

	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available; this is not macOS")
	}
	path := filepath.Join(t.TempDir(), "agent.plist")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v\n%s", err, out)
	}
}

// A first run under launchd would write launchd's idea of the session
// directories into the settings file for good, so install refuses until a
// terminal run has written it -- and refuses before writing anything.
func TestServiceInstallNeedsASettingsFile(t *testing.T) {
	sandbox(t)
	dir := t.TempDir()
	p := servicePaths{
		home:  dir,
		plist: filepath.Join(dir, "LaunchAgents", serviceLabel+".plist"),
		log:   filepath.Join(dir, "Logs", "brightlantern.log"),
	}

	err := serviceInstall(p)
	if err == nil || !strings.Contains(err.Error(), "from a terminal first") {
		t.Fatalf("serviceInstall() = %v; want a refusal naming the first run", err)
	}
	if _, err := os.Stat(p.plist); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused install wrote the plist")
	}
}

// What `launchctl print` said about the app's agent, trimmed. No "program ="
// line, which is why managedByApp does not look for one.
const appAgentPrint = `gui/501/org.billmill.brightlantern = {
	active count = 0
	path = (submitted by smd.416)
	type = Submitted
	managed_by = com.apple.xpc.ServiceManagement
	state = spawn scheduled

	program identifier = Contents/MacOS/brightlantern (mode: 2)
	parent bundle identifier = org.billmill.brightlantern
}
`

const m13AgentPrint = "gui/501/org.billmill.brightlantern = {\n\tstate = running\n\tprogram = /opt/homebrew/bin/brightlantern\n}\n"

// stubLaunchctl answers launchctl print with out, and records whether
// anything was booted out.
func stubLaunchctl(t *testing.T, out string) (bootedOut *bool) {
	t.Helper()
	oldPrint, oldQuiet := launchctlPrint, launchctlQuiet
	t.Cleanup(func() { launchctlPrint, launchctlQuiet = oldPrint, oldQuiet })
	launchctlPrint = func() string { return out }
	called := false
	launchctlQuiet = func(args ...string) error {
		if len(args) > 0 && args[0] == "bootout" {
			called = true
		}
		return nil
	}
	return &called
}

func testServicePaths(t *testing.T) servicePaths {
	dir := t.TempDir()
	return servicePaths{
		home:  dir,
		plist: filepath.Join(dir, "LaunchAgents", serviceLabel+".plist"),
		log:   filepath.Join(dir, "Logs", "brightlantern.log"),
	}
}

// With the app's agent loaded, installing would boot it out and then fail to
// bootstrap the label SMAppService still holds, leaving nothing running. So
// it refuses, before writing or booting out anything (#74).
func TestServiceInstallRefusesWhileTheAppOwnsTheLabel(t *testing.T) {
	home := sandbox(t)
	installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))
	writeConfig(t, config.Config{Titles: config.TitlesApple})
	bootedOut := stubLaunchctl(t, appAgentPrint)
	p := testServicePaths(t)

	err := serviceInstall(p)
	if err == nil || !strings.Contains(err.Error(), "through Bright Lantern.app") {
		t.Fatalf("serviceInstall() = %v; want a refusal naming the app", err)
	}
	if _, err := os.Stat(p.plist); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused install wrote the plist")
	}
	if *bootedOut {
		t.Error("a refused install booted the app's agent out")
	}
}

// The app runs uninstall to clear an M13 plist before registering. It may
// remove the file and must not boot out the label, which is the app's job.
func TestServiceUninstallLeavesTheAppsAgentRunning(t *testing.T) {
	bootedOut := stubLaunchctl(t, appAgentPrint)
	p := testServicePaths(t)
	if err := os.MkdirAll(filepath.Dir(p.plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.plist, []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := serviceUninstall(p); err != nil {
		t.Fatal(err)
	}
	if *bootedOut {
		t.Error("uninstall booted the app's agent out")
	}
	if _, err := os.Stat(p.plist); !errors.Is(err, os.ErrNotExist) {
		t.Error("the stray plist is still there")
	}
}

func TestServiceUninstallBootsOutItsOwnAgent(t *testing.T) {
	bootedOut := stubLaunchctl(t, m13AgentPrint)
	if err := serviceUninstall(testServicePaths(t)); err != nil {
		t.Fatal(err)
	}
	if !*bootedOut {
		t.Error("uninstall left an M13 agent loaded")
	}
}

func TestManagedByApp(t *testing.T) {
	if !managedByApp(appAgentPrint) {
		t.Error("managedByApp(the app's agent) = false")
	}
	if managedByApp("") || managedByApp(m13AgentPrint) {
		t.Error("managedByApp(nothing, or an M13 agent) = true")
	}
}

func TestLaunchctlState(t *testing.T) {
	out := "gui/501/org.billmill.brightlantern = {\n\tactive count = 1\n\tstate = running\n\n\tprogram = /x\n\tpid = 4242\n}\n"
	if state, pid := launchctlState(out); state != "running" || pid != "4242" {
		t.Errorf("launchctlState() = %q, %q; want running, 4242", state, pid)
	}
	if state, pid := launchctlState("something else entirely"); state != "state unknown" || pid != "" {
		t.Errorf("unparseable output = %q, %q; want a placeholder and no pid", state, pid)
	}
}

// The LaunchAgent's serve waits for a taken port rather than exiting into
// launchd's restart loop, and takes over once it is free.
func TestListenWhenFreeTakesOverAFreedPort(t *testing.T) {
	old := waitInterval
	waitInterval = 20 * time.Millisecond
	t.Cleanup(func() { waitInterval = old })

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := held.Addr().String()

	got := make(chan error, 1)
	go func() {
		ln, err := listenWhenFree(context.Background(), addr)
		if err == nil {
			ln.Close()
		}
		got <- err
	}()

	select {
	case err := <-got:
		t.Fatalf("returned while the port was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	held.Close()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("listenWhenFree() = %v once the port was free", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not take the port after it was freed")
	}
}

func TestListenWhenFreeStopsWhenCancelled(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listenWhenFree(ctx, held.Addr().String()); !errors.Is(err, context.Canceled) {
		t.Errorf("listenWhenFree() = %v, want context.Canceled", err)
	}
}
