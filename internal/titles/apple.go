package titles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/llimllib/brightlantern/internal/embed"
)

// AppleHelper is the executable that reaches Apple's on-device model. It is
// Swift, built from cmd/brightlantern-apple by `mise run apple`, and shipped
// beside the binary.
const AppleHelper = "brightlantern-apple"

// Exit codes from the helper; see cmd/brightlantern-apple/main.swift.
const (
	appleExitUnavailable = 3
	appleExitDeclined    = 4
)

// Apple summarizes with Apple's on-device Foundation Models.
//
// The reason to want it is everything it does not need: no key to keep, no
// bill, no network, and no PATH -- which is what makes it the backend that
// works under launchd, where neither ANTHROPIC_API_KEY nor claude can be
// found (#70). Measured on 25 sessions from a real corpus (#84): no failures,
// median 2.6s a title, quality moderately below Haiku -- more generic, fewer
// ticket numbers and library names -- and never wrong.
//
// A process per title, like the claude backend, rather than linking the
// framework in through cgo: a crash in there would take the daemon with it.
type Apple struct {
	// Bin is the helper's path.
	Bin string
}

// NewApple finds the helper and asks it whether the model is usable, so that
// a machine without Apple Intelligence gets one message at startup rather
// than one per session.
func NewApple() (*Apple, error) {
	bin, ok := AppleHelperPath()
	if !ok {
		return nil, fmt.Errorf("%s is not installed (looked for %s); run 'mise run apple'", AppleHelper, bin)
	}
	a := &Apple{Bin: bin}

	var stderr bytes.Buffer
	cmd := exec.Command(bin, "--check")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("%s --check: %w", bin, err)
	}
	return a, nil
}

// AppleHelperPath is where the helper is, or where it would be installed when
// it is nowhere. BRIGHTLANTERN_APPLE_BIN overrides the search.
func AppleHelperPath() (string, bool) {
	if env := strings.TrimSpace(os.Getenv("BRIGHTLANTERN_APPLE_BIN")); env != "" {
		return env, true
	}
	return embed.Beside(AppleHelper)
}

func (a *Apple) Name() string { return "apple-foundation-models" }

// appleRequest is what the helper reads on stdin.
type appleRequest struct {
	Instructions string `json:"instructions"`
	Prompt       string `json:"prompt"`
	MaxTokens    int    `json:"max_tokens"`
}

// Summarize runs one prompt through the helper.
func (a *Apple) Summarize(ctx context.Context, slice string) (string, error) {
	if strings.TrimSpace(slice) == "" {
		return "", fmt.Errorf("empty session")
	}

	// The system prompt as instructions, which this model -- unlike claude -p
	// -- takes separately and honours. The transcript is still fenced and
	// followed by the instruction, for the same reason as there: a slice cut
	// at 10k characters usually ends mid-sentence, and a small model is more
	// inclined than a large one to answer the last thing it read.
	req, err := json.Marshal(appleRequest{
		Instructions: systemPrompt,
		Prompt: "--- begin transcript ---\n" + slice +
			"\n--- end transcript ---\n\n" +
			"Reply with the title for that transcript, and nothing else.",
		// Eight words, with room for a model that overshoots; Clean trims
		// whatever it says to a title anyway.
		MaxTokens: 40,
	})
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, a.Bin)
	cmd.Stdin = bytes.NewReader(req)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == appleExitDeclined {
			return "", fmt.Errorf("%w: %s", ErrDeclined, msg)
		}
		return "", fmt.Errorf("%s: %w: %s", AppleHelper, err, msg)
	}

	title := Clean(stdout.String())
	if title == "" {
		return "", fmt.Errorf("no title in output")
	}
	return title, nil
}

// AppleConcurrency is how many helpers run at once.
//
// The model runs on this machine, not a service, so more workers buy
// contention rather than throughput.
const AppleConcurrency = 2
