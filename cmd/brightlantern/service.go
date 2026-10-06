package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/llimllib/brightlantern/internal/config"
)

// serviceLabel names the LaunchAgent. It is the bundle identifier too, so
// that the app in M14 registers the same job through SMAppService (#74)
// rather than a second one -- two labels is two daemons. It must never
// change: it is in launchctl, in Login Items, and in every registration on
// every machine that has one.
const serviceLabel = "org.billmill.brightlantern"

// agentPlist is the LaunchAgent.
//
// An agent, not a daemon: a LaunchDaemon runs as root with no user session,
// and everything here is the user's -- the index, the session files, the GPU
// of a logged-in session that the model wants.
//
// KeepAlive with --wait rather than KeepAlive alone: see listenWhenFree. No
// EnvironmentVariables: the titles backend that works here needs none, and
// anything put in this file ends up pasted into issues.
var agentPlist = template.Must(template.New("plist").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label | xml}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Program | xml}}</string>
		<string>serve</string>
		<string>--wait</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>{{.Home | xml}}</string>
	<key>StandardOutPath</key>
	<string>{{.Log | xml}}</string>
	<key>StandardErrorPath</key>
	<string>{{.Log | xml}}</string>
</dict>
</plist>
`))

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

type agent struct {
	Label, Program, Home, Log string
}

func (a agent) render() ([]byte, error) {
	var b bytes.Buffer
	if err := agentPlist.Execute(&b, a); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// servicePaths are where the agent's files live for this user.
type servicePaths struct {
	home, plist, log string
}

func defaultServicePaths() (servicePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return servicePaths{}, err
	}
	return servicePaths{
		home:  home,
		plist: filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
		// Where Console.app looks. One file for both streams, because every
		// note() is stderr and the startup line is stdout, and reading them
		// interleaved is the only way the order makes sense.
		log: filepath.Join(home, "Library", "Logs", "brightlantern", "brightlantern.log"),
	}, nil
}

const serviceUsage = `usage: brightlantern service install | uninstall | restart | status

  install    run brightlantern at login and keep it running
  uninstall  stop it and remove the LaunchAgent
  restart    restart it, e.g. after an upgrade
  status     say whether it is installed and running
`

func runService(args []string, addr string) error {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, serviceUsage)
		os.Exit(2)
	}
	p, err := defaultServicePaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		return serviceInstall(p)
	case "uninstall":
		return serviceUninstall(p)
	case "restart":
		return launchctl("kickstart", "-k", serviceTarget())
	case "status":
		return serviceStatus(p, addr)
	default:
		fmt.Fprint(os.Stderr, serviceUsage)
		os.Exit(2)
	}
	return nil
}

// serviceInstall writes the plist and loads it.
func serviceInstall(p servicePaths) error {
	// A first run under launchd would detect session directories in launchd's
	// environment and write that answer down -- permanently, since a settings
	// file is exactly what stops detection running again. So the first run has
	// to have happened somewhere with a real environment.
	cfg, had, err := config.Load()
	if err != nil {
		return err
	}
	if !had {
		return fmt.Errorf("run brightlantern once from a terminal first: it finds your "+
			"session directories and writes them to %s, and should not do that from "+
			"launchd's environment", config.Path())
	}
	if cfg.Titles != "" && cfg.Titles != config.TitlesApple && cfg.Titles != config.TitlesOff {
		note("titles = %q in %s, which needs a key or a PATH that launchd does not provide; "+
			"the agent will not generate titles. titles = \"apple\" works under launchd",
			cfg.Titles, config.Path())
	}

	program, err := agentProgram()
	if err != nil {
		return err
	}
	plist, err := agent{Label: serviceLabel, Program: program, Home: p.home, Log: p.log}.render()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.log), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.plist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p.plist, plist, 0o644); err != nil {
		return err
	}

	// Reinstalling replaces: unload whatever is loaded, ignoring "not loaded".
	_ = launchctlQuiet("bootout", serviceTarget())
	if err := launchctl("bootstrap", serviceDomain(), p.plist); err != nil {
		return err
	}
	fmt.Printf("installed %s\nrunning %s serve at login; logs in %s\n", p.plist, program, p.log)
	return nil
}

// agentProgram is the path the plist runs.
//
// Not resolved through symlinks, which is the opposite of embed.DefaultPaths:
// Homebrew's symlink is the stable name, and its target is a versioned
// Caskroom directory that an upgrade deletes. And never a go run binary, which
// lives in a temporary directory that is gone when it exits.
func agentProgram() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe = filepath.Clean(exe)
	if strings.HasPrefix(exe, filepath.Clean(os.TempDir())+string(filepath.Separator)) ||
		strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", fmt.Errorf("%s is a temporary build; install from a built binary "+
			"(mise run build, or the Homebrew cask)", exe)
	}
	return exe, nil
}

func serviceUninstall(p servicePaths) error {
	_ = launchctlQuiet("bootout", serviceTarget())
	if err := os.Remove(p.plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Printf("removed %s\nlogs are left in %s\n", p.plist, p.log)
	return nil
}

func serviceStatus(p servicePaths, addr string) error {
	if _, err := os.Stat(p.plist); errors.Is(err, os.ErrNotExist) {
		fmt.Println("not installed; 'brightlantern service install' installs it")
		return nil
	}
	fmt.Printf("installed  %s\n", p.plist)

	out, err := exec.Command("launchctl", "print", serviceTarget()).CombinedOutput()
	if err != nil {
		fmt.Println("loaded     no (it loads at the next login, or on install)")
	} else {
		state, pid := launchctlState(string(out))
		line := state
		if pid != "" {
			line += ", pid " + pid
		}
		fmt.Printf("loaded     yes, %s\n", line)
	}

	if in := probeInstance(addr); in != nil {
		search := "keyword only (model loading)"
		if in.Semantic {
			search = "semantic"
		}
		fmt.Printf("serving    brightlantern %s on http://%s, %s search\nindex      %s\n",
			in.Version, dialable(addr), search, in.Index)
	} else {
		fmt.Printf("serving    nothing answers on %s\n", addr)
	}
	fmt.Printf("logs       %s\n", p.log)
	return nil
}

var (
	launchctlStateRe = regexp.MustCompile(`(?m)^\s*state = (\S+)`)
	launchctlPIDRe   = regexp.MustCompile(`(?m)^\s*pid = (\d+)`)
)

// launchctlState pulls the state and pid out of `launchctl print`, whose
// format Apple documents as unstable -- so only the two lines that have been
// there since it was introduced, and nothing fails if they are not.
func launchctlState(out string) (state, pid string) {
	state = "state unknown"
	if m := launchctlStateRe.FindStringSubmatch(out); m != nil {
		state = m[1]
	}
	if m := launchctlPIDRe.FindStringSubmatch(out); m != nil {
		pid = m[1]
	}
	return state, pid
}

func serviceDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }
func serviceTarget() string { return serviceDomain() + "/" + serviceLabel }

// launchctl runs it, reporting its own output on failure: its errors are
// terse ("Bootstrap failed: 5: Input/output error") but they are all there is.
func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err,
			strings.TrimSpace(string(out)))
	}
	return nil
}

func launchctlQuiet(args ...string) error {
	return exec.Command("launchctl", args...).Run()
}
