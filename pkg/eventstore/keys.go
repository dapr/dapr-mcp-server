package eventstore

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Key layout, with every ID path-escaped so a "/" or "#" inside an ID cannot collide with
// a separator:
//
//	<prefix>/<session>                    the session record: how many streams it opened
//	<prefix>/<session>#<n>                the ID of the n-th stream the session opened
//	<prefix>/<session>/<stream>           the stream record: how many events it holds
//	<prefix>/<session>/<stream>/<index>   one event
const (
	keySeparator         = "/"
	streamIndexSeparator = "#"
)

func sessionKey(prefix, sessionID string) string {
	return joinKey(prefix, url.PathEscape(sessionID))
}

func streamKey(prefix, sessionID, streamID string) string {
	return joinKey(sessionKey(prefix, sessionID), url.PathEscape(streamID))
}

func streamIndexKey(prefix, sessionID string, n int) string {
	return sessionKey(prefix, sessionID) + streamIndexSeparator + strconv.Itoa(n)
}

func eventKey(prefix, sessionID, streamID string, index int) string {
	return joinKey(streamKey(prefix, sessionID, streamID), strconv.Itoa(index))
}

func joinKey(parts ...string) string {
	return strings.Join(parts, keySeparator)
}

// sessionRecord holds how many streams a session opened. It stays the same size however
// many requests the session makes; the stream IDs are in streamIndexRecords.
type sessionRecord struct {
	Streams int `json:"streams"`
}

// streamIndexRecord names one stream a session opened, so SessionClosed can find it.
type streamIndexRecord struct {
	Stream string `json:"stream"`
}

// streamRecord holds the number of events appended to a stream; the next event's index is Count.
type streamRecord struct {
	Count int `json:"count"`
}

// eventRecord wraps an event's bytes. The envelope keeps an empty event distinguishable
// from a missing key, because the Dapr state API returns an empty value for both.
type eventRecord struct {
	Data []byte `json:"data"`
}

func encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode %T: %w", v, err)
	}
	return b, nil
}

// decode reads raw into v and reports whether a value was present.
func decode(raw []byte, v any) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return true, fmt.Errorf("decode %T: %w", v, err)
	}
	return true, nil
}
