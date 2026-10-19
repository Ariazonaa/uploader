package store

import "errors"

// ErrNotFound is returned when a query matches no rows. Callers translate this
// into a 404 (or a generic auth failure) as appropriate.
var ErrNotFound = errors.New("not found")

// ErrInvalidInvite is returned when an invite code is missing, already used or
// expired. Callers translate this into a generic registration failure.
var ErrInvalidInvite = errors.New("invalid invite code")
