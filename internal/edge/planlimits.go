package edge

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/bloom"
)

// mauLocalMax bounds the users one edge process remembers per project for
// the month (beyond it, only the feed's filter decides).
const mauLocalMax = 50_000

// mauSeen is the users this process let sign in or refresh this month, so
// that users counted since the last sweep keep working once a monthly
// active users limit is reached (V4.1 §3.2).
type mauSeen struct {
	mu    sync.Mutex
	month time.Month
	users map[uuid.UUID]map[uuid.UUID]struct{} // project → users
}

func (m *mauSeen) has(project, user uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll()
	_, ok := m.users[project][user]
	return ok
}

func (m *mauSeen) add(project, user uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll()
	u := m.users[project]
	if u == nil {
		u = map[uuid.UUID]struct{}{}
		m.users[project] = u
	}
	if len(u) < mauLocalMax {
		u[user] = struct{}{}
	}
}

// roll forgets last month's users.
func (m *mauSeen) roll() {
	if now := time.Now().UTC().Month(); m.users == nil || m.month != now {
		m.month, m.users = now, map[uuid.UUID]map[uuid.UUID]struct{}{}
	}
}

// mauAllowed refuses a user who would be a new monthly active user once the
// plan's limit is reached; users already counted this month carry on.
func (e *Edge) mauAllowed(c *call, user uuid.UUID) error {
	p := c.p
	if p.cfg.MAUBlocked && !bloom.Has(p.cfg.MAUCounted, user) && !e.mau.has(p.cfg.ProjectID, user) {
		c.w.Header().Set("Retry-After", strconv.Itoa(untilNextMonth(time.Now())))
		return refuse(http.StatusTooManyRequests, "mau_limit_reached",
			"this project's organisation has reached its plan's monthly active users; users already active this month can sign in, new ones can when the month ends or the plan is upgraded")
	}
	e.mau.add(p.cfg.ProjectID, user)
	return nil
}
