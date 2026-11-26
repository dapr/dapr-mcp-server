package eventstore

import (
	"context"
	"errors"
	"iter"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loggingStore logs the errors of an mcp.EventStore. The SDK turns them into HTTP errors
// or dropped events without logging them.
type loggingStore struct {
	inner  mcp.EventStore
	logger *slog.Logger
}

// WithLogging returns an mcp.EventStore that logs every error of inner to logger, with the
// state store detail that Open leaves out of its error. Errors a client or a session close
// causes in normal use are logged at Debug, real store failures at Error.
func WithLogging(inner mcp.EventStore, logger *slog.Logger) mcp.EventStore {
	return &loggingStore{inner: inner, logger: logger}
}

func (l *loggingStore) Open(ctx context.Context, sessionID, streamID string) error {
	err := l.inner.Open(ctx, sessionID, streamID)
	l.logError(ctx, "open stream", streamID, err, isClosed)
	return err
}

func (l *loggingStore) Append(ctx context.Context, sessionID, streamID string, data []byte) error {
	err := l.inner.Append(ctx, sessionID, streamID, data)
	l.logError(ctx, "store event", streamID, err, isClosed)
	return err
}

func (l *loggingStore) After(ctx context.Context, sessionID, streamID string, index int) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for data, err := range l.inner.After(ctx, sessionID, streamID, index) {
			l.logError(ctx, "replay events", streamID, err, isExpectedReplayError)
			if !yield(data, err) {
				return
			}
		}
	}
}

func (l *loggingStore) SessionClosed(ctx context.Context, sessionID string) error {
	err := l.inner.SessionClosed(ctx, sessionID)
	l.logError(ctx, "delete session", "", err, nil)
	return err
}

// logError logs err, at Debug when expected, if not nil, reports it as normal operation.
func (l *loggingStore) logError(ctx context.Context, op, streamID string, err error, expected func(error) bool) {
	if err == nil {
		return
	}
	level := slog.LevelError
	if expected != nil && expected(err) {
		level = slog.LevelDebug
	}
	l.logger.Log(ctx, level, "Event store failed", "operation", op, "stream", streamID, "error", errorDetail(err))
}

// isClosed reports a write refused because its session has closed, which races the close.
func isClosed(err error) bool {
	return errors.Is(err, ErrSessionClosed)
}

// isExpectedReplayError reports a replay a client asked for that cannot be served: its events
// have expired, or its Last-Event-ID names no stream.
func isExpectedReplayError(err error) bool {
	return errors.Is(err, mcp.ErrEventsPurged) || errors.Is(err, errUnknownStream)
}
