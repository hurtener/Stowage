package retrieval

// ExportRRF exposes the internal rrf function for benchmarking.
func ExportRRF(lanes map[string][]string) []FusedHit {
	return rrf(lanes)
}

// ExportNewHub creates a Hub with the given maxSize for testing.
func ExportNewHub(maxSize int) *Hub { return NewHub(maxSize) }

// ExportQuerySig exposes QuerySig for testing.
func ExportQuerySig(tokens []string) string { return QuerySig(tokens) }
