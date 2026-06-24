package longmemeval

import "time"

// ExportParseHaystackDate exposes parseHaystackDate for the external test package.
func ExportParseHaystackDate(s string) time.Time { return parseHaystackDate(s) }
