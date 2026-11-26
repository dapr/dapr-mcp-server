package eventstore

import (
	"errors"
	"fmt"
)

var (
	// ErrUnavailable is the error Open returns when the state store fails. The SDK writes
	// Open's error into the HTTP response, so its message names no key, store or cause;
	// errors.Unwrap still reaches the cause.
	ErrUnavailable = errors.New("event store unavailable")
	// ErrSessionClosed is returned for a write to a session that SessionClosed has deleted,
	// so a write racing the close cannot recreate its records.
	ErrSessionClosed = errors.New("event store: session closed")

	// errUnknownStream is returned for a stream that has no record, such as one a client
	// names in Last-Event-ID.
	errUnknownStream = errors.New("unknown stream")
)

// unavailableError reports ErrUnavailable and keeps the state store error behind it.
type unavailableError struct {
	cause error
}

func (e *unavailableError) Error() string { return ErrUnavailable.Error() }

func (e *unavailableError) Unwrap() []error { return []error{ErrUnavailable, e.cause} }

// errorDetail returns the state store error behind an ErrUnavailable, or err itself.
func errorDetail(err error) error {
	var u *unavailableError
	if errors.As(err, &u) {
		return u.cause
	}
	return err
}

func unknownStream(sessionID, streamID string) error {
	return fmt.Errorf("event store: %w %q in session %q", errUnknownStream, streamID, sessionID)
}

func sessionClosed(sessionID string) error {
	return fmt.Errorf("%w: %q", ErrSessionClosed, sessionID)
}
