package session

import "errors"

var (
	ErrInvalidSession   = errors.New("invalid session")
	ErrInvalidEvent     = errors.New("invalid event")
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionNotActive = errors.New("session not active")
)
