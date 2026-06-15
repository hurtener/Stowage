package pipeline

// TrySend enqueues item onto the fire-and-forget ingest channel ch in a
// non-blocking, panic-safe way (P2). It returns true if the item was accepted,
// false if the channel was full OR closed.
//
// Why the recover: every live ingress (the MCP `memory_ingest` handler, the SDK
// `Ingest` method) enqueues onto the channel boot.StartPipeline owns, which
// Drain closes at shutdown. A bare `select { case ch <- x: default: }` does NOT
// survive a send on a CLOSED channel — that panics even with a default arm. If
// an in-flight ingress races the shutdown Drain, the bare form panics across the
// API/MCP boundary (forbidden, CLAUDE.md §13). The entrypoints close that race
// window by stopping ingress before Drain (HTTP Shutdown / MCP Shutdown await),
// but TrySend is the shared defense-in-depth so a late send degrades to a
// dropped item (Enqueued=false) instead of a panic. MCP and SDK share this one
// helper so the two surfaces cannot drift in their enqueue safety (D-067 lens).
func TrySend(ch chan<- Item, item Item) (sent bool) {
	if ch == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			sent = false
		}
	}()
	select {
	case ch <- item:
		return true
	default:
		return false
	}
}
