package state

import "errors"

// ErrNotFound is returned when a requested resource does not exist in the database.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write violates a uniqueness/conflict constraint.
var ErrConflict = errors.New("conflict")
