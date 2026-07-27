// Package contracts is the single source of truth for the Vibe Forge API
// contract. It embeds contracts/contract.json (the same file the React frontend
// imports) via go:embed and exposes typed accessors so the backend can never
// drift from the frontend.
//
// The canonical contract is contracts/contract.json. Do not duplicate these
// values by hand elsewhere; add accessors here instead.
package contracts

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed contract.json
var raw []byte

// Contract is the parsed shared contract.
type Contract struct {
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Stages       Stages             `json:"stages"`
	States       map[string]Enum    `json:"states"`
	ArtifactTypes map[string]string `json:"artifactTypes"`
	Events       Events             `json:"events"`
	Paths        map[string]Path    `json:"paths"`
	Idempotency  Idempotency        `json:"idempotency"`
	Errors       Errors             `json:"errors"`
	Limits       Limits             `json:"limits"`
}

// Enum is a string state enumeration.
type Enum struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// Stages defines the four serial agent-loop stages in order.
type Stages struct {
	Order        []string          `json:"order"`
	Descriptions map[string]string `json:"descriptions"`
}

// Events is the SSE event contract.
type Events struct {
	Protocol    Protocol                  `json:"protocol"`
	Definitions map[string]EventDef       `json:"definitions"`
}

// Protocol describes the SSE transport rules.
type Protocol struct {
	ContentType    string   `json:"contentType"`
	Seq            string   `json:"seq"`
	ReplayHeader   string   `json:"replayHeader"`
	TerminalEvents []string `json:"terminalEvents"`
}

// EventDef describes one SSE event.
type EventDef struct {
	Fields    map[string]string `json:"fields"`
	Semantic  string            `json:"semantic"`
}

// Path describes one REST endpoint.
type Path struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Type    string            `json:"type,omitempty"`
	Success map[string]any    `json:"success,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
}

// Idempotency describes the idempotency-key rules.
type Idempotency struct {
	Header    string   `json:"header"`
	TTLSeconds int      `json:"ttlSeconds"`
	AppliesTo []string `json:"appliesTo"`
}

// Errors is the stable error contract.
type Errors struct {
	Structure  map[string]string         `json:"structure"`
	Codes      map[string]ErrorCode       `json:"codes"`
	RunFailureCodes map[string]string     `json:"runFailureCodes"`
}

// ErrorCode maps a stable code to its HTTP status and retryability.
type ErrorCode struct {
	HTTP      int  `json:"http"`
	Retryable bool `json:"retryable"`
}

// Limits holds the shared numeric/string limits.
type Limits struct {
	PromptMinChars              int    `json:"promptMinChars"`
	PromptMaxChars              int    `json:"promptMaxChars"`
	FileContentMaxBytes         int    `json:"fileContentMaxBytes"`
	WritableFilePath            string `json:"writableFilePath"`
	RunWatchdogSeconds          int    `json:"runWatchdogSeconds"`
	SandpackReadyTimeoutSeconds int    `json:"sandpackReadyTimeoutSeconds"`
	MaxAutoFixRounds            int    `json:"maxAutoFixRounds"`
}

// cached is the parsed contract, populated on first use.
var cached *Contract

// Load parses and returns the embedded contract. It panics on parse failure
// because a corrupt contract is a compile-time-class bug that must surface
// immediately, never silently.
func Load() *Contract {
	if cached != nil {
		return cached
	}
	var c Contract
	if err := json.Unmarshal(raw, &c); err != nil {
		panic(fmt.Sprintf("contracts: failed to parse embedded contract.json: %v", err))
	}
	cached = &c
	return cached
}

// Raw returns the raw embedded contract bytes (for serving / debugging).
func Raw() []byte { return raw }

// Version returns the contract version string.
func Version() string { return Load().Version }

// HTTPStatusFor returns the HTTP status code for a stable error code.
// Returns 500 if the code is unknown.
func HTTPStatusFor(code string) int {
	if ec, ok := Load().Errors.Codes[code]; ok {
		return ec.HTTP
	}
	return 500
}

// IsRetryable reports whether a stable error code is retryable.
func IsRetryable(code string) bool {
	if ec, ok := Load().Errors.Codes[code]; ok {
		return ec.Retryable
	}
	return true
}
