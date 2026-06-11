package pgstore

import "github.com/hurtener/stowage/internal/store"

func init() {
	store.Register("postgres", Open)
}
