package titles

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHelper stands in for brightlantern-apple, recording what it was sent.
// The real one needs Apple Intelligence, which CI does not have.
func fakeHelper(t *testing.T, script string) (bin, stdinPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, AppleHelper)
	stdinPath = filepath.Join(dir, "stdin")
	body := "#!/usr/bin/env bash\n" +
		`if [ "$1" != --check ]; then cat > ` + stdinPath + "; fi\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, stdinPath
}

func TestAppleSummarize(t *testing.T) {
	bin, stdinPath := fakeHelper(t, `echo '"Centering a div with flexbox."'`)
	a := &Apple{Bin: bin}

	got, err := a.Summarize(context.Background(), "user: how do I center a div")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Centering a div with flexbox" {
		t.Errorf("Summarize() = %q", got)
	}

	raw, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	var req appleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("stdin was not a request: %v\n%s", err, raw)
	}
	// The instructions travel separately, the transcript is fenced, and the
	// instruction comes again after it, so a slice ending mid-sentence is not
	// the last thing the model reads.
	if req.Instructions != systemPrompt {
		t.Error("instructions are not the shared system prompt")
	}
	if !strings.Contains(req.Prompt, "--- begin transcript ---\nuser: how do I center a div\n--- end transcript ---") {
		t.Errorf("transcript is not fenced:\n%s", req.Prompt)
	}
	if !strings.HasSuffix(strings.TrimSpace(req.Prompt), "and nothing else.") {
		t.Errorf("prompt does not end with the instruction:\n%s", req.Prompt)
	}
}

// Exit 4 is the helper saying asking again gets the same answer; anything
// else might be transient and must stay a plain failure, or a rate-limited
// session would be settled without a title.
func TestAppleDeclinedIsDistinctFromFailed(t *testing.T) {
	for _, tc := range []struct {
		script   string
		declined bool
	}{
		{`echo "declined: guardrailViolation" >&2; exit 4`, true},
		{`echo "rateLimited" >&2; exit 1`, false},
	} {
		bin, _ := fakeHelper(t, tc.script)
		_, err := (&Apple{Bin: bin}).Summarize(context.Background(), "user: hello")
		if err == nil {
			t.Fatalf("%s: want an error", tc.script)
		}
		if got := errors.Is(err, ErrDeclined); got != tc.declined {
			t.Errorf("%s: declined = %v, want %v (%v)", tc.script, got, tc.declined, err)
		}
	}
}

// The helper's own reason is what someone without Apple Intelligence needs
// to read, once, at startup.
func TestNewAppleReportsWhyItIsUnavailable(t *testing.T) {
	bin, _ := fakeHelper(t, `echo "Apple Intelligence is turned off" >&2; exit 3`)
	t.Setenv("BRIGHTLANTERN_APPLE_BIN", bin)

	_, err := NewApple()
	if err == nil || !strings.Contains(err.Error(), "Apple Intelligence is turned off") {
		t.Errorf("NewApple() = %v, want the helper's reason", err)
	}
}

func TestNewAppleNamesAMissingHelper(t *testing.T) {
	t.Setenv("BRIGHTLANTERN_APPLE_BIN", "")
	t.Setenv("BRIGHTLANTERN_DATA_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := NewApple()
	if err == nil || !strings.Contains(err.Error(), "mise run apple") {
		t.Errorf("NewApple() = %v, want it to say how to build the helper", err)
	}
}
