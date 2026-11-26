package eventstore

import "time"

// closedSessionLimit bounds how many closed sessions are remembered. Past it the oldest are
// forgotten first; a write that still races their close leaves records the TTL reclaims.
const closedSessionLimit = 10_000

// closedRetention is how long a closed session is remembered: as long as the records a late
// write could recreate would live.
func closedRetention(ttl time.Duration) time.Duration {
	if ttl == NoTTL {
		return DefaultTTL * recordTTLFactor
	}
	return recordTTL(ttl)
}

// closedSessions remembers recently closed sessions, oldest first, so writes that race
// SessionClosed are refused instead of recreating the session's records. Guarded by Store.mu.
type closedSessions struct {
	retention time.Duration
	limit     int
	closedAt  map[string]time.Time
	order     []string
}

func newClosedSessions(retention time.Duration, limit int) *closedSessions {
	return &closedSessions{retention: retention, limit: limit, closedAt: make(map[string]time.Time)}
}

// add records that sessionID closed at now.
func (c *closedSessions) add(sessionID string, now time.Time) {
	if _, ok := c.closedAt[sessionID]; !ok {
		c.closedAt[sessionID] = now
		c.order = append(c.order, sessionID)
	}
	c.prune(now)
}

// contains reports whether sessionID closed within the retention period.
func (c *closedSessions) contains(sessionID string, now time.Time) bool {
	at, ok := c.closedAt[sessionID]
	return ok && now.Sub(at) < c.retention
}

// prune forgets sessions past the retention period, then the oldest past the limit.
func (c *closedSessions) prune(now time.Time) {
	drop := 0
	for drop < len(c.order) {
		id := c.order[drop]
		if now.Sub(c.closedAt[id]) < c.retention && len(c.order)-drop <= c.limit {
			break
		}
		delete(c.closedAt, id)
		drop++
	}
	// Reslicing drops the prefix; append releases it when it next grows the array.
	c.order = c.order[drop:]
}
