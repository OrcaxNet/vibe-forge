// Package compile is the server-side structural compile gate for /src/App.tsx
// (FLO-60 QA stage). It is a leaf package with no store/agent dependency so that
// both the agent loop and the store's manual-edit path (FLO-77) run the SAME
// gate - a manual "保存并编译" can never skip the compile check that protects
// stableVersionId.
//
// Sandpack (browser) remains the MVP preview runtime and the authoritative
// bundler (PRD-B: do not self-build a bundler). This gate performs a real
// structural validation of the generated /src/App.tsx so the backend is
// self-sufficient and testable without a browser: it catches the failures that
// actually break a React/TSX compile (missing export, unbalanced JSX/braces,
// forbidden shell/install tokens, empty or oversized output) and produces the
// structured file/line/message errors the contract pins (B-FR-06). When the
// frontend is integrated, POST /api/runs/:id/compile-result lets Sandpack report
// its own result for the QA node detail.
package compile

import (
	"strings"

	"github.com/OrcaxNet/vibe-forge/contracts"
)

// Error is one structured compile error (B-FR-06: file/line/message).
type Error struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Result is the QA stage artifact (artifactType "compile_result").
type Result struct {
	Pass      bool    `json:"pass"`
	Errors    []Error `json:"errors,omitempty"`
	FilesHash string  `json:"filesHash,omitempty"`
}

// writablePath returns the single writable file this gate validates
// (contract §limits.writableFilePath, B-FR-01).
func writablePath() string { return contracts.Load().Limits.WritableFilePath }

// Validate runs the QA structural compile check on the Engineer's /src/App.tsx
// content. A pass means the file is structurally a compilable React component; a
// fail returns structured errors to feed back to the Engineer (auto-repair) or
// to reject a manual edit. It never panics and never inspects anything beyond
// the single business file.
func Validate(appTSX string) Result {
	file := writablePath()
	var errs []Error

	lim := contracts.Load().Limits
	if len(appTSX) > lim.FileContentMaxBytes {
		errs = append(errs, Error{File: file, Line: 1, Message: "file exceeds the 200KB size limit"})
	}
	trimmed := strings.TrimSpace(appTSX)
	if trimmed == "" {
		errs = append(errs, Error{File: file, Line: 1, Message: "App.tsx is empty"})
	}

	if pat, ok := ForbiddenInCode(appTSX); ok {
		errs = append(errs, Error{File: file, Line: 1, Message: "forbidden token in generated code: " + pat + " (shell/install are not allowed)"})
	}

	if !hasDefaultExport(appTSX) {
		errs = append(errs, Error{File: file, Line: 1, Message: "missing a default export (e.g. `export default function App()`)"})
	}

	if brErr, ok := braceBalance(appTSX); !ok {
		errs = append(errs, brErr)
	}
	if jsxErr, ok := jsxSanity(appTSX); !ok {
		errs = append(errs, jsxErr)
	}

	return Result{Pass: len(errs) == 0, Errors: errs}
}

// hasDefaultExport reports whether the source declares a default export. This
// is what main.tsx (`import App from "./App"`) requires to compile.
func hasDefaultExport(src string) bool {
	s := stripStringsAndComments(src)
	return strings.Contains(s, "exportdefault") ||
		strings.Contains(s, "export default")
}

// braceBalance verifies (), {}, [] are balanced and reports the first offending
// line. It ignores delimiters inside string literals and comments via
// stripStringsAndComments. This catches the most common LLM compile break
// (a missing/extra brace) that Sandpack would reject.
func braceBalance(src string) (Error, bool) {
	s := stripStringsAndComments(src)
	file := writablePath()
	type frame struct {
		ch   byte
		line int
	}
	var stack []frame
	line := 1
	pairs := map[byte]byte{')': '(', '}': '{', ']': '['}
	open := map[byte]bool{'(': true, '{': true, '[': true}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' {
			line++
			continue
		}
		if open[c] {
			stack = append(stack, frame{ch: c, line: line})
		} else if want, isClose := pairs[c]; isClose {
			if len(stack) == 0 {
				return Error{File: file, Line: line, Message: "unexpected closing '" + string(c) + "'"}, false
			}
			top := stack[len(stack)-1]
			if top.ch != want {
				return Error{File: file, Line: line, Message: "mismatched '" + string(c) + "' (expected '" + string(matchClose(top.ch)) + "')"}, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return Error{File: file, Line: top.line, Message: "unclosed '" + string(top.ch) + "'"}, false
	}
	return Error{}, true
}

func matchClose(open byte) byte {
	switch open {
	case '(':
		return ')'
	case '{':
		return '}'

	case '[':
		return ']'
	}
	return open
}

// jsxSanity does a light JSX tag check: every opening <Identifier must be
// closed by </Identifier> or self-close, ignoring JSX text. It catches a
// dangling component tag. This is intentionally conservative - it only flags
// clearly unbalanced component tags, not arbitrary HTML, to avoid false
// positives on valid Tailwind-heavy markup.
func jsxSanity(src string) (Error, bool) {
	s := stripStringsAndComments(src)
	file := writablePath()
	var stack []struct {
		name string
		line int
	}
	line := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line++
		}
		if s[i] != '<' {
			continue
		}
		// Skip "</" handled below; skip "<=" "<>" "<-" etc. by requiring a name char.
		if i+1 < len(s) && s[i+1] == '/' {
			// closing tag </Name>
			j := i + 2
			name := ""
			for j < len(s) && isNameChar(s[j]) {
				name += string(s[j])
				j++
			}
			if name == "" {
				continue
			}
			if len(stack) == 0 {
				return Error{File: file, Line: line, Message: "unexpected closing tag </" + name + ">"}, false
			}
			top := stack[len(stack)-1]
			if top.name != name {
				return Error{File: file, Line: line, Message: "mismatched JSX tag </" + name + "> (expected </" + top.name + ">)"}, false
			}
			stack = stack[:len(stack)-1]
			continue
		}
		// opening tag <Name ... possibly self-closing
		if i+1 >= len(s) || !isNameStart(s[i+1]) {
			continue
		}
		// Distinguish JSX from TypeScript generics / type assertions / less-than.
		// A '<' is JSX only in an *expression-start* position: after an operator
		// or punctuator (=, =>, &&, ?, :, ,, (, [, {, ;, ...) or an expression
		// keyword (return, throw, await, ...). After an identifier, ')', ']', or
		// a literal it is a generic (Array<T>, Record<K,V>) or a comparison
		// (a < b) - never an opening JSX tag. Without this, `Array<string>` is
		// misread as an unclosed JSX tag <string> and typed components fail.
		if !jsxPosition(s, i) {
			continue
		}
		j := i + 1
		name := ""
		for j < len(s) && isNameChar(s[j]) {
			name += string(s[j])
			j++
		}
		if name == "" {
			continue
		}
		// TS primitive/keyword type names are never valid JSX element names; a
		// `<string>`/`<number>` in expression position is a type assertion, not JSX.
		if isTypeName(name) {
			continue
		}
		// Find the matching '>' on a best-effort basis, skipping over JSX
		// expression containers {...} so a '>' inside an attribute like
		// `streak={n > 0 ? n : 0}` or `onClick={() => fn()}` does not end the
		// tag early (which would miss a trailing `/>` self-close and falsely
		// push the component). If the matching '>' is "/>" it self-closes.
		k := j
		selfClose := false
		depth := 0
		for k < len(s) {
			switch s[k] {
			case '{':
				depth++
			case '}':
				if depth > 0 {
					depth--
				}
			case '>':
				if depth == 0 {
					goto foundClose
				}
			}
			k++
		}
	foundClose:
		if k < len(s) && s[k] == '>' && k > 0 && s[k-1] == '/' {
			selfClose = true
		}
		if !selfClose {
			stack = append(stack, struct {
				name string
				line int
			}{name: name, line: line})
		}
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return Error{File: file, Line: top.line, Message: "unclosed JSX tag <" + top.name + ">"}, false
	}
	return Error{}, true
}

func isNameStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}
func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9') || c == '.' || c == '-'
}

// jsxPosition reports whether the '<' at index i is in an expression-start
// position (so it begins a JSX tag) vs a type-reference/comparison position
// (a generic like Array<T> or a less-than like a < b).
//
// It inspects the previous significant (non-whitespace) byte:
//   - an operand-expecting operator/punctuator (=, =>, &&, ?, :, ,, (, [, {,
//     ;, ...) or an expression keyword (return, throw, await, ...) => JSX;
//   - an identifier (that is not such a keyword), ')', ']', '}', or a literal
//     => generic/comparison, not JSX.
func jsxPosition(s string, i int) bool {
	k := i - 1
	for k >= 0 && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
		k--
	}
	if k < 0 {
		return true // start of input: `<Foo/>` at top is JSX
	}
	c := s[k]
	// Operand-expecting operators/punctuators, the JSX-expression-container
	// close '}', and '.' (JSX text like "end.") all put a '<' in JSX position.
	// '}' closes a `{expr}` container and returns to element context, so
	// `{expr}<div>` is a JSX tag, not a generic. Only ')', ']' and quotes are
	// unambiguous value/literal positions where '<' is a generic or comparison.
	switch c {
	case '=', '>', '+', '-', '*', '/', '%', '&', '|', '^', '~', '!', '?', ':',
		'(', '[', '{', '}', ',', ';', '<', '.':
		return true
	case ')', ']', '"', '\'', '`':
		return false
	}
	if c >= '0' && c <= '9' {
		return false // numeric literal => comparison
	}
	if isNameChar(c) {
		end := k + 1
		start := k
		for start > 0 && isNameChar(s[start-1]) {
			start--
		}
		switch s[start:end] {
		case "return", "throw", "yield", "await", "typeof", "void", "delete",
			"new", "instanceof", "in", "of", "do", "else", "case", "default":
			return true
		}
		return false // identifier/value/type reference => generic or comparison
	}
	// Any other byte (notably CJK/emoji JSX-text continuation bytes 0x80-0xBF:
	// "备注 <span>", "价格：<b>") means we are inside JSX text content, so a
	// following '<' is a JSX tag. Treating it as non-JSX skips the opening tag
	// and makes its legitimate closer look "unexpected"/"mismatched".
	return true
}

// isStringStartPos reports whether the quote at index i is in an operand
// position where a real JS/TS string literal can begin (after =, (, [, {, ,, :,
// ?, an operator, ';', or an expression keyword like return). A quote after an
// identifier, ')', ']', '}', '.', or a literal is NOT a string - it is JSX text
// (today's, "you") or a comparison operand on the left - and must be left as a
// literal so it does not swallow following closing tags.
func isStringStartPos(s []byte, i int) bool {
	k := i - 1
	for k >= 0 && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
		k--
	}
	if k < 0 {
		return true
	}
	c := s[k]
	switch c {
	case '=', '(', '[', '{', ',', ':', '?', '+', '-', '*', '/', '%', '&', '|',
		'^', '~', '!', '<', '>', ';', '`':
		return true
	case ')', ']', '}', '"', '\'', '.':
		return false
	}
	if c >= '0' && c <= '9' {
		return false
	}
	if isNameChar(c) {
		end := k + 1
		start := k
		for start > 0 && isNameChar(s[start-1]) {
			start--
		}
		switch string(s[start:end]) {
		case "return", "throw", "yield", "await", "typeof", "void", "delete",
			"new", "instanceof", "in", "of", "do", "else", "case", "default":
			return true
		}
		return false
	}
	return false
}

func isTypeName(name string) bool {
	switch name {
	case "string", "number", "boolean", "any", "unknown", "void", "null",
		"undefined", "never", "object", "symbol", "bigint", "const", "typeof",
		"keyof", "infer", "readonly", "unique":
		return true
	}
	return false
}

// stripStringsAndComments returns src with string literals, template literals,
// line comments and block comments replaced by spaces (preserving newlines and
// length so line numbers and offsets stay valid). This lets the brace/JSX
// scanners ignore delimiters inside strings/comments.
func stripStringsAndComments(src string) string {
	b := []byte(src)
	i := 0
	for i < len(b) {
		switch {
		case i+1 < len(b) && b[i] == '/' && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		case i+1 < len(b) && b[i] == '/' && b[i+1] == '*':
			b[i], b[i+1] = ' ', ' '
			i += 2
			for i < len(b) && !(i+1 < len(b) && b[i] == '*' && b[i+1] == '/') {
				if b[i] != '\n' {
					b[i] = ' '
				}
				i++
			}
			if i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
				i += 2
			}
		case (b[i] == '"' || b[i] == '\'') && isStringStartPos(b, i):
			// Only enter a string scan when the quote is in an operand position
			// (after =, (, ,, :, return, ...). A quote that follows a letter or
			// ')' ']' '.' is JSX text (today's, "you") or a comparison, NOT a
			// string - treating it as one swallows the following closing tags.
			q := b[i]
			b[i] = ' '
			i++
			// JS single/double-quoted strings cannot contain a raw newline, so
			// stop at one (defensive: an unterminated quote should not eat the
			// rest of the file).
			for i < len(b) && b[i] != q {
				if b[i] == '\n' {
					break
				}
				if b[i] == '\\' && i+1 < len(b) {
					b[i] = ' '
					b[i+1] = ' '
					i += 2
					continue
				}
				b[i] = ' '
				i++
			}
			if i < len(b) && b[i] == q {
				b[i] = ' '
				i++
			}
		case b[i] == '`':
			b[i] = ' '
			i++
			for i < len(b) && b[i] != '`' {
				if b[i] == '\\' && i+1 < len(b) {
					b[i] = ' '
					b[i+1] = ' '
					i += 2
					continue
				}
				if b[i] != '\n' {
					b[i] = ' '
				}
				i++
			}
			if i < len(b) {
				b[i] = ' '
				i++
			}
		default:
			i++
		}
	}
	return string(b)
}

// ForbiddenCodePatterns are tokens the generated App.tsx must never contain:
// the scaffold renders in a browser Sandpack with no shell, so shell/exec and
// install commands are categorically disallowed (B-FR-01: no shell, no npm
// install). Matches are case-insensitive.
var ForbiddenCodePatterns = []string{
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

// ForbiddenInCode reports whether content contains a forbidden pattern.
func ForbiddenInCode(content string) (string, bool) {
	lower := strings.ToLower(content)
	for _, pat := range ForbiddenCodePatterns {
		if strings.Contains(lower, pat) {
			return pat, true
		}
	}
	return "", false
}
