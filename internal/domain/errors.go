package domain

import "errors"

var (
	// ErrNotFound is returned by a repository when the requested entity does
	// not exist.
	ErrNotFound = errors.New("domain: entity not found")
	// ErrConflict is returned when an operation would overwrite a newer
	// version of an entity or create a duplicate entity.
	ErrConflict = errors.New("domain: entity conflict")
	// ErrInvalidArgument is returned when a domain value cannot be created from
	// the supplied input.
	ErrInvalidArgument = errors.New("domain: invalid argument")
	// ErrInvalidTransition is returned when a run lifecycle transition is not
	// permitted from its current state.
	ErrInvalidTransition = errors.New("domain: invalid run transition")
	// ErrTerminalRun is returned when a caller tries to mutate a finished run.
	ErrTerminalRun = errors.New("domain: run is terminal")
	// ErrAlreadyDecided is returned when an approval is resolved more than
	// once.
	ErrAlreadyDecided = errors.New("domain: approval is already decided")
	// ErrNotPermitted is returned when policy does not allow an operation.
	ErrNotPermitted = errors.New("domain: operation is not permitted")
)
