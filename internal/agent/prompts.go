package agent

// prompts.go holds the system + per-stage user-turn prompts for the Vibe Forge
// single agent loop (FLO-60). One Claude conversation thread progresses
// PM -> Architect -> Engineer -> QA serially; each stage is one user turn and
// the model's reply is the stage artifact. The Engineer turn exposes a single
// sandboxed write_file tool; QA is a server-side structural compile gate (see
// compile.go), and a QA failure is re-fed to the Engineer as another user turn
// for bounded auto-repair.
//
// Prompts are deliberately explicit about the sandbox (only /src/App.tsx is
// writable; React + TypeScript + Tailwind; no shell / npm / fs / process.env)
// so the model produces a self-contained, compilable single file - the fixed
// "real API smoke" prompt (acceptance 6) only has to vary the user idea.

// systemPrompt is the coordinator system prompt shared by every stage turn. It
// sets the role, the four-stage pipeline, and the hard sandbox rules. Per-stage
// turns (below) ask the model to act as one role at a time.
const systemPrompt = `You are Vibe Forge, an agent that builds a single-file React application from a user idea, working through four strictly serial stages: Product Manager (PM) -> Architect -> Engineer -> QA.

You will be asked to act as ONE role per turn. Stay in that role and produce only that stage's output. Do not skip ahead.

Sandbox (hard rules, never violate):
- The project is a single business file: /src/App.tsx. It is the ONLY writable file. The scaffold (index.html, src/main.tsx, src/index.css) is read-only and already provides React 18, TypeScript, and Tailwind CSS. main.tsx renders <App /> as the default export of /src/App.tsx.
- The app runs in a browser Sandpack. There is NO shell, NO node CLI, NO package install step. Never call npm/npx/yarn/pnpm, never import "fs" or "child_process", never read process.env. Do not add runtime dependencies; use only React (already imported via the scaffold) and Tailwind utility classes.
- /src/App.tsx MUST have a default export (e.g. "export default function App()"). It must be valid TypeScript/TSX that compiles. Keep everything in this one file (types, components, hooks, styles via Tailwind classes).
- Balance all braces, parentheses, JSX tags. Close every component tag. Do not emit markdown fences around the code; the write_file tool takes raw source.

Produce real, complete, working code - not placeholders, not "..." ellipses, not TODOs. The QA stage compiles the file, so it must be syntactically complete.`

// pmUserTurn asks the PM to produce a concise spec from the user idea.
func pmUserTurn(userIdea string) string {
	return `You are the Product Manager. Given this user idea, produce a concise product spec (markdown, ~150-250 words):
- What the app does, the core user flow, and the 3-6 key features.
- Name the components/states you expect, but do NOT write code.
- Note any edge cases to handle.

User idea:
` + userIdea
}

// architectUserTurn asks the Architect to produce a single-file structure plan
// from the spec. The specText is the PM's prior turn output.
func architectUserTurn(specText string) string {
	return `You are the Architect. Given the PM spec below, produce a structure plan for the single file /src/App.tsx (markdown, ~150-250 words):
- The React component tree (all in App.tsx) and the state/hooks needed.
- The Tailwind layout approach. No code yet - just the plan.
- Call out what makes this compile cleanly as one self-contained TSX file.

PM spec:
` + specText
}

// engineerUserTurn asks the Engineer to implement /src/App.tsx by calling the
// write_file tool. planText is the Architect's prior turn output.
func engineerUserTurn(userIdea, planText string) string {
	return `You are the Engineer. Implement /src/App.tsx by calling the write_file tool with path "/src/App.tsx" and the full TypeScript/TSX source.

Requirements:
- React + TypeScript + Tailwind. Default-export the App component. Self-contained in this one file.
- Fulfill the user idea and the structure plan. Real, complete, working code - no placeholders.
- No shell, no npm, no fs, no process.env, no new dependencies.
- Call write_file exactly once with the complete file content as raw source (no markdown fences).

User idea:
` + userIdea + `

Structure plan:
` + planText
}

// engineerRepairTurn re-feeds a QA compile failure to the Engineer for one
// auto-repair round. errorsText is a human-readable list of the compile errors.
func engineerRepairTurn(errorsText string) string {
	return `QA compile check FAILED on the /src/App.tsx you wrote. Fix it by calling write_file again with path "/src/App.tsx" and the corrected, complete source.

Compile errors (file / line / message):
` + errorsText + `

Rules unchanged: React + TypeScript + Tailwind, default export, self-contained single file, no shell/npm/fs/process.env, no markdown fences. Call write_file once with the full corrected file.`
}

// formatCompileErrors renders a CompileResult's errors for the repair turn.
func formatCompileErrors(errs []CompileError) string {
	if len(errs) == 0 {
		return "(no specific error messages)"
	}
	out := ""
	for _, e := range errs {
		out += "- " + e.File + ":" + itoa(e.Line) + " " + e.Message + "\n"
	}
	return out
}

// itoa is a dependency-free int->string (avoids strconv in this small file).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
