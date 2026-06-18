package retrieval

import "github.com/hurtener/stowage/internal/excerpt"

// ClampExcerpt is the canonical drill-down excerpt shaper used by the HTTP handler
// (internal/api), the MCP handler (internal/mcpserver), and the embedded SDK
// (sdk/stowage) so the three surfaces cannot diverge (D-069, parity-lens BUG-5). It
// delegates to the leaf internal/excerpt package — the shaper itself carries no
// dependency, so gateway-free consumers (internal/traces, D-086) import the leaf
// directly rather than this gateway-pulling package.
func ClampExcerpt(content string, s, e int) string { return excerpt.Clamp(content, s, e) }
