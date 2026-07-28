package agent

import (
	"github.com/OrcaxNet/vibe-forge/internal/compile"
)

// compile.go re-exports the server-side QA-stage compile gate (FLO-60) from the
// internal/compile leaf package. The gate lives in a leaf package so the store's
// manual-edit path (FLO-77) runs the SAME compile check as the agent loop - a
// manual "保存并编译" can never skip the gate that protects stableVersionId.
//
// Sandpack (browser) is the MVP preview runtime and the authoritative bundler
// (PRD-B: do not self-build a bundler); the gate does a real structural
// validation of /src/App.tsx so the backend is self-sufficient and testable
// without a browser (B-FR-06). See internal/compile/compile.go for the checks.

// CompileError is one structured compile error (B-FR-06: file/line/message).
type CompileError = compile.Error

// CompileResult is the QA stage artifact (artifactType "compile_result").
type CompileResult = compile.Result

// ValidateCompile runs the QA structural compile check on the Engineer's
// /src/App.tsx content. A pass means the file is structurally a compilable
// React component; a fail returns structured errors to feed back to the
// Engineer (auto-repair). It never panics and never inspects anything beyond
// the single business file.
func ValidateCompile(appTSX string) CompileResult {
	return compile.Validate(appTSX)
}
