// Package logredact provides a log.Printf-compatible logger that scrubs
// secrets from every line before it reaches the writer. It exists so the agent
// loop's run-lifecycle logs (run started / aborted by shutdown / failed with a
// code) are safe to ship to container stdout: an accidental leak - a key echoed
// back in an upstream error, a token in a URL - is redacted rather than printed.
//
// The redactor never logs the full prompt or generated code because the loop's
// own logf callsites only pass runId / stage / code / error; this layer is the
// defense-in-depth backstop (FLO-59 acceptance: logs must contain no keys, full
// prompts, or generated code).
package logredact

import (
	"fmt"
	"io"
	"strings"
)

// redactionMarker replaces every occurrence of a secret in the output.
const redactionMarker = "[REDACTED]"

// Logger is a Printf-style logger that redacts configured secrets.
type Logger struct {
	w      io.Writer
	secrets []string
}

// New returns a Logger that writes redacted lines to w. Empty secrets are
// ignored. The caller passes the live secret values (API key, auth token) from
// the environment; they are kept only in memory for matching.
func New(w io.Writer, secrets []string) *Logger {
	kept := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			kept = append(kept, s)
		}
	}
	return &Logger{w: w, secrets: kept}
}

// Printf formats and writes a redacted line. It mirrors log.Printf so it can be
// installed as the agent loop's logger via Loop.SetLogger.
func (l *Logger) Printf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	for _, s := range l.secrets {
		if s == "" {
			continue
		}
		line = strings.ReplaceAll(line, s, redactionMarker)
	}
	fmt.Fprintln(l.w, line)
}
