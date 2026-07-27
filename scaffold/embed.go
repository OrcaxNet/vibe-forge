// Package scaffold is the fixed React + Tailwind scaffold Vibe Forge compiles
// in Sandpack. Only /src/App.tsx is writable; every other file is read-only
// (contract §limits.writableFilePath, B-FR-01). The files live under files/ and
// are embedded here so the Go backend (Engineer stage, Sandpack files map) and
// the React frontend share one frozen source of truth.
package scaffold

import "embed"

// FS embeds the scaffold/files tree (paths: index.html, src/main.tsx,
// src/index.css, src/App.tsx). Read with FS.ReadFile("files/<path>").
//
//go:embed all:files
var FS embed.FS
