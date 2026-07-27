package agent

import "testing"

// TestValidateFilePath: only /src/App.tsx is writable; traversal and scaffold
// paths are rejected (B-FR-01, acceptance 3: illegal path -> 422).
func TestValidateFilePath(t *testing.T) {
	valid := []string{
		"/src/App.tsx",
		"src/App.tsx",
		"//src//App.tsx",
		"/src/./App.tsx",
	}
	for _, p := range valid {
		if err := ValidateFilePath(p); err != nil {
			t.Errorf("ValidateFilePath(%q) = %v, want nil", p, err)
		}
	}
	illegal := []string{
		"/src/main.tsx",           // scaffold file (read-only)
		"/src/index.css",          // scaffold file
		"/index.html",             // scaffold file
		"/src/App.ts",             // wrong extension
		"/src/components/Foo.tsx", // not the writable file
		"/etc/passwd",             // system path
		"/src/../main.tsx",        // traversal to scaffold
		"../src/App.tsx",          // traversal attempt
		"/src/App.tsx/../../etc",  // traversal
	}
	for _, p := range illegal {
		if err := ValidateFilePath(p); err == nil {
			t.Errorf("ValidateFilePath(%q) = nil, want error (illegal path)", p)
		}
	}
}

// TestBuildFilesMap: the files map is the scaffold (read-only) plus the
// Engineer-written App.tsx (writable), sorted by path.
func TestBuildFilesMap(t *testing.T) {
	app := "export default function App() { return <div/>; }"
	m := BuildFilesMap(app)
	if len(m) < 4 {
		t.Fatalf("expected at least 4 files, got %d", len(m))
	}
	var appEntry *FileEntry
	for i := range m {
		if m[i].Path == writablePath {
			appEntry = &m[i]
			break
		}
	}
	if appEntry == nil {
		t.Fatal("App.tsx missing from files map")
	}
	if appEntry.Content != app {
		t.Errorf("App.tsx content not overridden with engineer output")
	}
	if appEntry.Readonly {
		t.Error("App.tsx must be writable (readonly=false)")
	}
	// All scaffold files are read-only and unchanged.
	for _, e := range m {
		if e.Path == writablePath {
			continue
		}
		if !e.Readonly {
			t.Errorf("scaffold file %q must be read-only", e.Path)
		}
	}
}

// TestForbiddenInCode: shell/install/fs tokens are flagged.
func TestForbiddenInCode(t *testing.T) {
	bad := []string{
		"child_process.exec('rm -rf')",
		"npm install react",
		"npx create-vite",
		"require('fs')",
		"import fs from 'fs'",
		"process.env.SECRET",
	}
	for _, s := range bad {
		if _, ok := forbiddenInCode(s); !ok {
			t.Errorf("forbiddenInCode(%q) = false, want true", s)
		}
	}
	if _, ok := forbiddenInCode("const x = 1"); ok {
		t.Error("forbiddenInCode flagged clean code")
	}
}
