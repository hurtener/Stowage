package store

import "errors"

// ErrNotFound is returned when a requested entity does not exist in the store.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert would violate a uniqueness constraint.
var ErrConflict = errors.New("store: conflict")

// ErrChecksumMismatch is returned when an already-applied migration's SQL
// content has changed since it was applied.
var ErrChecksumMismatch = errors.New("store: migration checksum mismatch")

// ErrDriverNotRegistered is returned by Open when no factory has been
// registered for the requested driver name.
var ErrDriverNotRegistered = errors.New("store: driver not registered")
