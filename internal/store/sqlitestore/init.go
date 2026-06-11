package sqlitestore

import "github.com/hurtener/stowage/internal/store"

func init() {
	store.Register("sqlite", Open)
}
