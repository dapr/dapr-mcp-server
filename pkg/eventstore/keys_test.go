package eventstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// IDs that contain a separator must not produce another record's key.
func TestKeysEscapeSeparatorsInIDs(t *testing.T) {
	t.Parallel()
	const p = DefaultKeyPrefix
	tests := []struct {
		name       string
		crafted    string
		legitimate string
	}{
		{name: "stream ID aliasing an event", crafted: streamKey(p, "s", "a/0"), legitimate: eventKey(p, "s", "a", 0)},
		{name: "stream ID aliasing a parent path", crafted: streamKey(p, "s", "../x"), legitimate: joinKey(p, "x")},
		{name: "session ID aliasing a stream record", crafted: sessionKey(p, "s/a"), legitimate: streamKey(p, "s", "a")},
		{name: "session ID aliasing a stream index entry", crafted: sessionKey(p, "s#0"), legitimate: streamIndexKey(p, "s", 0)},
		{name: "session and stream aliasing an event", crafted: streamKey(p, "s/a", "0"), legitimate: eventKey(p, "s", "a", 0)},
		{name: "stream ID aliasing another stream's event", crafted: eventKey(p, "s", "a/0", 1), legitimate: joinKey(eventKey(p, "s", "a", 0), "1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.NotEqual(t, tt.legitimate, tt.crafted)
		})
	}
}

func TestKeysAreDistinctPerID(t *testing.T) {
	t.Parallel()
	ids := []string{"a", "a/0", "a#0", "a%2F0", "../x", "a/", "/a", "#", "", "%"}
	seen := make(map[string]string, len(ids))
	for _, id := range ids {
		key := streamKey(DefaultKeyPrefix, "s", id)
		if other, ok := seen[key]; ok {
			t.Fatalf("stream IDs %q and %q share key %q", other, id, key)
		}
		seen[key] = id
	}
}
