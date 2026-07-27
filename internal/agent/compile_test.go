package agent

import (
	"strings"
	"testing"
)

// TestValidateCompilePass: a complete, default-exported, balanced TSX file with
// balanced JSX tags passes the structural QA gate (B-FR-06).
func TestValidateCompilePass(t *testing.T) {
	good := `import { useState } from "react";

export default function App() {
  const [n, setN] = useState(0);
  return (
    <div className="p-4">
      <h1>Count: {n}</h1>
      <button onClick={() => setN(n + 1)}>inc</button>
    </div>
  );
}`
	r := ValidateCompile(good)
	if !r.Pass {
		t.Fatalf("expected pass, got errors: %+v", r.Errors)
	}
}

// TestValidateCompileGenericsPass: TypeScript generics and type assertions
// (Array<T>, Record<K,V>, useState<T>, <string>x) must NOT be misread as
// unclosed JSX tags. This is the regression for the real-API smoke false
// positive where a Tailwind-typed habit tracker was rejected as <string>.
func TestValidateCompileGenericsPass(t *testing.T) {
	cases := []string{
		`import { useState } from "react";
type Habit = { id: string; streak: number };
export default function App() {
  const [h, setH] = useState<Array<Habit>>([]);
  const m: Record<string, number> = {};
  const n = h.length as number;
  return <div>{h.length}{m["x"]}{n}</div>;
}`,
		`const x = <string>"hi";
export default function App() { return <div>{x}</div>; }`,
		`import { useState } from "react";
export default function App() {
  const [v, setV] = useState<number>(0);
  const pairs: Array<[string, number]> = [["a", 1]];
  return <div>{pairs.length < v ? "lt" : "ge"}</div>;
}`,
		// Self-closing components whose attributes contain '>' (comparison) or
		// '=>' (arrow) inside JSX expression containers must still be detected
		// as self-closing, not pushed as unclosed tags.
		`import { useState } from "react";
function Card({ n }: { n: number }) { return <div>{n}</div>; }
export default function App() {
  const [n, setN] = useState(0);
  return (
    <main>
      <Card n={n > 0 ? n : 0} />
      <Card n={n} onClick={() => setN(n + 1)} />
    </main>
  );
}`,
		// JSX text containing apostrophes and quotes (e.g. "today's", "let's")
		// must NOT be misread as string literals that swallow the closing tags.
		`import { useState } from "react";
export default function App() {
  const [n] = useState(0);
  return (
    <div>
      <p>Today's streak is {n}. Let's keep it up!</p>
      <footer>Don't give up on "you" today.</footer>
    </div>
  );
}`,
		// CJK / non-ASCII JSX text before a tag, a tag right after a JSX
		// expression container close '}', and a tag after '.' JSX text. These
		// are the real FLO-58 failure shapes: the opening tag was skipped, so
		// its closer looked "unexpected"/"mismatched".
		`import { useState } from "react";
export default function App() {
  const [n] = useState(0);
  return (
    <div>
      <label>备注 <span className="muted">（可选）</span></label>
      <p>当前进度：{n}%<span className="unit">完成</span></p>
      <p>第 {n} 项.<span className="tail">尾</span></p>
    </div>
  );
}`,
	}
	for i, src := range cases {
		r := ValidateCompile(src)
		if !r.Pass {
			t.Errorf("case %d: expected pass (generics must not be flagged as JSX), got: %+v", i, r.Errors)
		}
	}
}

// TestValidateCompileFailures: each common compile break produces a structured
// file/line/message error (B-FR-06) and a non-pass result.
func TestValidateCompileFailures(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		wantIn string // substring expected in some error message
	}{
		{"empty", "   \n  ", "empty"},
		{"missing default export", "function App() { return <div/>; }", "default export"},
		{"unclosed brace", "export default function App() { return <div/>;", "unclosed"},
		{"mismatched brace", "export default function App() { return <div/>; ]}", "mismatched"},
		{"forbidden shell token", "export default function App() {\n process.env.X\n return <div/>; }", "forbidden"},
		{"forbidden npm install", "export default function App() {\n // npm install foo\n return <div/>; }", "forbidden"},
		{"unclosed jsx tag", "export default function App() { return <div><span></div>; }", "mismatched"}, // </div> closes <div> but <span> still open -> mismatched
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ValidateCompile(c.src)
			if r.Pass {
				t.Fatalf("expected non-pass for %q", c.name)
			}
			if len(r.Errors) == 0 {
				t.Fatalf("expected structured errors for %q, got none", c.name)
			}
			// Every error must carry the writable file and a >=1 line (B-FR-06).
			joined := ""
			for _, e := range r.Errors {
				if e.File != writablePath {
					t.Errorf("error file = %q, want %q", e.File, writablePath)
				}
				if e.Line < 1 {
					t.Errorf("error line = %d, want >= 1", e.Line)
				}
				if e.Message == "" {
					t.Errorf("error has empty message")
				}
				joined += e.Message + " "
			}
			if c.wantIn != "" && !strings.Contains(strings.ToLower(joined), c.wantIn) {
				t.Errorf("expected an error mentioning %q, got: %s", c.wantIn, joined)
			}
		})
	}
}

// TestValidateCompileForbiddenOnly: forbidden tokens alone (with otherwise valid
// structure) still fail, so shell/install code never slips through.
func TestValidateCompileForbiddenOnly(t *testing.T) {
	src := `export default function App() {
  return <div>ok</div>;
}`
	if !ValidateCompile(src).Pass {
		t.Fatal("baseline should pass")
	}
	bad := strings.Replace(src, "ok", "npx create-react-app", 1)
	if ValidateCompile(bad).Pass {
		t.Fatal("forbidden npx token must fail the gate")
	}
}
