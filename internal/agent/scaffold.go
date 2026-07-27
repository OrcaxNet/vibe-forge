// Package agent implements the Vibe Forge single agent loop (FLO-60):
// PM -> Architect -> Engineer -> QA, four real serial stages driven by the
// Claude API with tool use, an SSE event stream persisted to SQLite, and a
// bounded auto-repair loop.
//
// Design (PRD-A/B v1.0):
//   - One Claude conversation thread progresses through the stages serially.
//     PM produces a spec, Architect a structure plan, Engineer writes the single
//     writable file /src/App.tsx via the write_file tool, QA validates it.
//   - write_file is sandboxed: only /src/App.tsx is writable; path traversal,
//     scaffold writes, shell and npm install are rejected (422) (B-FR-01).
//   - Every stage emits SSE events with a monotonic per-run seq persisted to
//     SQLite, so a reconnect resumes from Last-Event-ID with no dup/reorder
//     (A-FR-07).
//   - QA compile failure feeds back to Engineer for at most maxAutoFixRounds
//     (2) re-feeds; a 3rd consecutive failure enters run_failed. The previous
//     stable version is never changed on failure (B-FR-06, C-FR-04).
//   - Timeout / 429 / 5xx map to a retryable run_failed; the run watchdog
//     (60s no effective event) is a backstop (A-FR-08).
package agent

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/OrcaxNet/vibe-forge/scaffold"
)

// scaffoldFiles is the read-only React + Tailwind scaffold pinned in
// scaffold/manifest.json. Only /src/App.tsx is writable; every other file is
// read-only (contract §limits.writableFilePath, B-FR-01).
//
// path -> (content, readonly). Loaded once at startup from the embedded
// scaffold package (single source of truth shared with the frontend).
var scaffoldFiles = loadScaffold()

const writablePath = "/src/App.tsx"

// scaffoldEntry is one scaffold file's content and readonly flag.
type scaffoldEntry struct {
	content  string
	readonly bool
}

// loadScaffold reads the embedded scaffold files. The manifest declares which
// files exist and which are read-only; the content comes from scaffold/files.
func loadScaffold() map[string]scaffoldEntry {
	entries := map[string]scaffoldEntry{}
	// The scaffold/files tree mirrors the Sandpack files map paths.
	// index.html -> /index.html, src/* -> /src/*
	files := []string{
		"/index.html",
		"/src/main.tsx",
		"/src/index.css",
		"/src/App.tsx",
	}
	for _, p := range files {
		rel := strings.TrimPrefix(p, "/")
		b, err := scaffold.FS.ReadFile("files/" + rel)
		if err != nil {
			panic(fmt.Sprintf("agent: missing scaffold file %s: %v", p, err))
		}
		entries[p] = scaffoldEntry{content: string(b), readonly: p != writablePath}
	}
	return entries
}

// BuildFilesMap assembles the full Sandpack files map for a version: the
// read-only scaffold plus the Engineer-written /src/App.tsx. This is the file
// map Sandpack compiles (B-FR-04) and what gets stored in the draft version.
// appTSX is the generated business file content.
func BuildFilesMap(appTSX string) []FileEntry {
	paths := make([]string, 0, len(scaffoldFiles))
	for p := range scaffoldFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]FileEntry, 0, len(paths))
	for _, p := range paths {
		e := scaffoldFiles[p]
		content := e.content
		readonly := e.readonly
		if p == writablePath {
			content = appTSX
			readonly = false
		}
		out = append(out, FileEntry{Path: p, Content: content, Readonly: readonly})
	}
	return out
}

// FileEntry is one file in a version snapshot (mirrors store.FileSnapshot but
// kept in the agent package to avoid an import cycle in tests).
type FileEntry struct {
	Path     string
	Content  string
	Readonly bool
}

// ValidateFilePath enforces the writable-path contract (B-FR-01). Only
// /src/App.tsx is writable; path traversal and scaffold writes are rejected.
// Returns a sanitized reason when invalid (the caller surfaces it as 422).
//
// "path" arrives from the agent's write_file tool call. It is normalized to a
// POSIX absolute project-internal path and checked against the single writable
// file. `..`, absolute system paths, and any other scaffold file are rejected.
func ValidateFilePath(p string) error {
	// Normalize: ensure leading slash, clean internal . and ..
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	clean := path.Clean(p)
	if clean != writablePath {
		return fmt.Errorf("only %s is writable (got %q)", writablePath, p)
	}
	// path.Clean collapses "..", but reject any input that contained a traversal
	// attempt before cleaning so it can never be smuggled through.
	if strings.Contains(p, "..") {
		return fmt.Errorf("path traversal is not allowed")
	}
	return nil
}

// ForbiddenCodePatterns are tokens the generated App.tsx must never contain:
// the scaffold renders in a browser Sandpack with no shell, so shell/exec and
// install commands are categorically disallowed (B-FR-01: no shell, no npm
// install). Matches are case-insensitive.
var forbiddenCodePatterns = []string{
	"child_process",
	"execsync",
	"spawnSync",
	"require('fs')",
	"require(\"fs\")",
	"npm install",
	"npx ",
	"yarn add",
	"pnpm add",
	"import fs",
	"process.env",
}

// forbiddenInCode reports whether content contains a forbidden pattern.
func forbiddenInCode(content string) (string, bool) {
	lower := strings.ToLower(content)
	for _, pat := range forbiddenCodePatterns {
		if strings.Contains(lower, pat) {
			return pat, true
		}
	}
	return "", false
}
