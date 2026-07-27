package logredact

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintfRedactsAllSecrets(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, []string{"sk-secret-key", "tok-abc", ""}) // empty is ignored

	// A line that echoes an upstream error containing the key and token.
	l.Printf("agent: run %s failed: code=%s err=upstream said sk-secret-key and tok-abc", "r1", "UPSTREAM")

	out := buf.String()
	if strings.Contains(out, "sk-secret-key") {
		t.Errorf("log leaks api key: %q", out)
	}
	if strings.Contains(out, "tok-abc") {
		t.Errorf("log leaks auth token: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected redaction marker in: %q", out)
	}
	// Non-secret structure is preserved so the line stays useful.
	if !strings.Contains(out, "run r1 failed") || !strings.Contains(out, "UPSTREAM") {
		t.Errorf("log lost useful context: %q", out)
	}
}

func TestPrintfNoSecretsPrintsAsIs(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, nil)
	l.Printf("reconciled %d interrupted run(s)", 2)
	if buf.String() != "reconciled 2 interrupted run(s)\n" {
		t.Errorf("unexpected output: %q", buf.String())
	}
}
