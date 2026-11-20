// Package metrics keeps the gateway's runtime metrics since start: per
// workspace+op request, deny, reject and error counts with a latency histogram,
// plus process CPU, memory, goroutines and threads (EG0066). The server reports
// a Snapshot on the admin endpoint GET /admin/metrics.
package metrics

import (
	"sort"
	"sync"
	"time"
)

type opKey struct{ workspace, op string }

type opStats struct {
	requests, denied, rejected, errors uint64
	lat                                histogram
}

// Collector counts op requests. Entries are created only for a workspace and op
// the caller has resolved against the loaded config, so memory is bounded by
// what was ever loaded; anything unresolved goes to one unattributed counter.
// One mutex guards everything, so a Snapshot is a consistent cut. Every method
// is safe on a nil *Collector (a Server built without New in tests).
type Collector struct {
	mu           sync.Mutex
	ops          map[opKey]*opStats
	unattributed uint64
	startedAt    time.Time
	now          func() time.Time
}

// New returns an empty collector whose start time is now.
func New() *Collector {
	return &Collector{ops: map[opKey]*opStats{}, startedAt: time.Now(), now: time.Now}
}

// Unattributed counts a denial before the workspace and op resolved
// (authentication, agent, unknown workspace or op).
func (c *Collector) Unattributed() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.unattributed++
	c.mu.Unlock()
}

// Denied counts a resolved request that was refused with a 403.
func (c *Collector) Denied(workspace, op string) {
	c.record(workspace, op, func(s *opStats) { s.denied++ })
}

// Rejected counts a resolved request whose body was unreadable (400 invalid
// JSON, 413 too large).
func (c *Collector) Rejected(workspace, op string) {
	c.record(workspace, op, func(s *opStats) { s.rejected++ })
}

// Executed counts a resolved request that reached its connector, with its
// latency in ms (the audit latency_ms) and whether it failed (connector error
// or upstream status >= 400).
func (c *Collector) Executed(workspace, op string, latencyMS int64, failed bool) {
	c.record(workspace, op, func(s *opStats) {
		s.lat.add(latencyMS)
		if failed {
			s.errors++
		}
	})
}

func (c *Collector) record(workspace, op string, f func(*opStats)) {
	if c == nil {
		return
	}
	k := opKey{workspace, op}
	c.mu.Lock()
	s := c.ops[k]
	if s == nil {
		s = &opStats{}
		c.ops[k] = s
	}
	s.requests++
	f(s)
	c.mu.Unlock()
}

// OpStats is one workspace+op entry of a Snapshot.
type OpStats struct {
	Workspace string  `json:"workspace"`
	Op        string  `json:"op"`
	Requests  uint64  `json:"requests"`
	Denied    uint64  `json:"denied"`
	Rejected  uint64  `json:"rejected"`
	Errors    uint64  `json:"errors"`
	LatencyMS Latency `json:"latency_ms"`
}

// Snapshot is what GET /admin/metrics returns.
type Snapshot struct {
	StartedAt          time.Time `json:"started_at"`
	UptimeSeconds      float64   `json:"uptime_seconds"`
	DeniedUnattributed uint64    `json:"denied_unattributed"`
	Process            Process   `json:"process"`
	Ops                []OpStats `json:"ops"`
}

// Snapshot copies the counters under the lock, then reads the process stats.
// Ops are sorted by workspace, then op. A nil collector gives an empty snapshot.
func (c *Collector) Snapshot() Snapshot {
	snap := Snapshot{Ops: []OpStats{}}
	if c == nil {
		return snap
	}
	c.mu.Lock()
	snap.StartedAt = c.startedAt.UTC()
	snap.UptimeSeconds = c.now().Sub(c.startedAt).Seconds()
	snap.DeniedUnattributed = c.unattributed
	for k, s := range c.ops {
		snap.Ops = append(snap.Ops, OpStats{
			Workspace: k.workspace, Op: k.op,
			Requests: s.requests, Denied: s.denied, Rejected: s.rejected, Errors: s.errors,
			LatencyMS: s.lat.summary(),
		})
	}
	c.mu.Unlock()
	sort.Slice(snap.Ops, func(i, j int) bool {
		if snap.Ops[i].Workspace != snap.Ops[j].Workspace {
			return snap.Ops[i].Workspace < snap.Ops[j].Workspace
		}
		return snap.Ops[i].Op < snap.Ops[j].Op
	})
	snap.Process = readProcess()
	return snap
}
