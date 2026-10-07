package titles

// One backend: Apple's on-device model (#91).
//
// There were three. The Anthropic API and the Claude Code CLI both worked from
// a terminal and neither worked under launchd, which has no ANTHROPIC_API_KEY
// and no PATH that finds claude (#70) -- and launchd is how Bright Lantern.app
// runs the daemon. Making them work there meant a key file, a resolved binary
// path and a picker to choose between three backends; removing them meant
// titles a little more generic (#84) and nothing ever leaving the machine.

// NewSummarizer builds the on-device backend, or says why it cannot: most
// often Apple Intelligence is off, not yet downloaded, or unavailable for this
// region or language. The caller falls back to opening messages.
func NewSummarizer() (Summarizer, error) {
	return NewApple()
}

// ConcurrencyFor returns how many summaries to run at once. A stub in a test
// gets the default; the model is local, and more than AppleConcurrency is no
// faster.
func ConcurrencyFor(s Summarizer) int {
	if _, ok := s.(*Apple); ok {
		return AppleConcurrency
	}
	return DefaultConcurrency
}
