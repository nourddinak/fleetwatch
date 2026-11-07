// Package emailworker holds the Cloudflare Email Worker, so the dashboard can deploy
// the exact script this folder ships (see README.md for deploying it by hand).
package emailworker

import _ "embed"

//go:embed src/worker.js
var Script []byte
