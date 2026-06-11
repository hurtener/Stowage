package retrieval

// ExportRRF exposes the internal rrf function for benchmarking.
func ExportRRF(lanes map[string][]string) []FusedHit {
	return rrf(lanes)
}
