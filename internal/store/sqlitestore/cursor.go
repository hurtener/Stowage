package sqlitestore

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hurtener/stowage/internal/store"
)

// encodeCursor encodes a (timestamp-millis, id) pair into an opaque cursor
// string of the form "<millis>:<id>" (Q1 composite cursor).
func encodeCursor(tsMs int64, id string) string {
	return fmt.Sprintf("%d:%s", tsMs, id)
}

// parseCursor decodes a cursor produced by encodeCursor.
// Returns store.ErrBadCursor for any malformed input so callers can distinguish
// a bad cursor from other errors.
func parseCursor(cursor string) (int64, string, error) {
	idx := strings.IndexByte(cursor, ':')
	if idx < 0 {
		return 0, "", fmt.Errorf("%w: missing colon in %q", store.ErrBadCursor, cursor)
	}
	ts, err := strconv.ParseInt(cursor[:idx], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%w: non-numeric timestamp in %q", store.ErrBadCursor, cursor)
	}
	id := cursor[idx+1:]
	if id == "" {
		return 0, "", fmt.Errorf("%w: empty id in %q", store.ErrBadCursor, cursor)
	}
	return ts, id, nil
}
