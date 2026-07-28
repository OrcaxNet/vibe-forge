package compile

import (
	"strings"
	"testing"
)

// compile_test.go directly covers the leaf compile gate that both the agent loop
// and the store's manual-edit path (FLO-77) depend on. The agent package's
// compile_test.go exercises the same logic through the ValidateCompile wrapper;
// these tests pin the leaf contract independently.

func TestValidatePass(t *testing.T) {
	good := `import { useState } from "react";
export default function App() {
  const [n, setN] = useState(0);
  return <main><h1>{n}</h1></main>;
}`
	r := Validate(good)
	if !r.Pass {
		t.Fatalf("expected pass, got errors: %+v", r.Errors)
	}
	if len(r.Errors) != 0 {
		t.Errorf("passing result should have no errors, got %+v", r.Errors)
	}
}

func TestValidateFailureClasses(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantSub string // a fragment expected in some error message
	}{
		{"invalid JSX", `export default function App() { return <main><h1>x</main>; }`, "JSX"},
		{"missing default export", `const App = () => <main><div /></main>;`, "default export"},
		{"unclosed bracket", `export default function App() { return <main>x</main>; `, "unclosed"},
		{"forbidden token", `export default function App() { void process.env.X; return <main />; }`, "forbidden"},
		{"empty", ``, "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Validate(c.src)
			if r.Pass {
				t.Fatalf("expected fail, got pass")
			}
			if len(r.Errors) == 0 {
				t.Fatal("expected at least one error, got none")
			}
			joined := ""
			for _, e := range r.Errors {
				joined += e.Message + " "
			}
			if !strings.Contains(strings.ToLower(joined), strings.ToLower(c.wantSub)) {
				t.Errorf("errors = %+v; want a message containing %q", r.Errors, c.wantSub)
			}
			for _, e := range r.Errors {
				if e.File == "" {
					t.Errorf("error %+v missing file field", e)
				}
				if e.Line < 1 {
					t.Errorf("error %+v has invalid line %d", e, e.Line)
				}
			}
		})
	}
}

// TestValidateUsesContractWritablePath: the error file is the contract's writable
// path (/src/App.tsx), not a hardcoded agent constant.
func TestValidateUsesContractWritablePath(t *testing.T) {
	r := Validate(`const App = () => <div />;`)
	if r.Pass {
		t.Fatal("expected fail")
	}
	if r.Errors[0].File != "/src/App.tsx" {
		t.Errorf("error file = %q, want /src/App.tsx", r.Errors[0].File)
	}
}

func TestForbiddenInCode(t *testing.T) {
	if _, ok := ForbiddenInCode("npm install left-pad"); !ok {
		t.Error("ForbiddenInCode missed 'npm install'")
	}
	if _, ok := ForbiddenInCode("const x = 1;"); ok {
		t.Error("ForbiddenInCode flagged clean code")
	}
}
