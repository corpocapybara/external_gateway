package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestSnapshotSorted(t *testing.T) {
	c := New()
	start := c.startedAt
	c.now = func() time.Time { return start.Add(90 * time.Second) }
	c.Executed("b", "x.op", 12, false)
	c.Denied("a", "z.op")
	c.Rejected("a", "b.op")
	c.Executed("a", "b.op", 3, true)
	c.Unattributed()
	c.Unattributed()

	s := c.Snapshot()
	want := []OpStats{
		{Workspace: "a", Op: "b.op", Requests: 2, Rejected: 1, Errors: 1,
			LatencyMS: Latency{Count: 1, P50: 3, P95: 3, P99: 3, Max: 3}},
		{Workspace: "a", Op: "z.op", Requests: 1, Denied: 1},
		{Workspace: "b", Op: "x.op", Requests: 1,
			LatencyMS: Latency{Count: 1, P50: 12, P95: 12, P99: 12, Max: 12}},
	}
	if len(s.Ops) != len(want) {
		t.Fatalf("ops %+v", s.Ops)
	}
	for i := range want {
		if s.Ops[i] != want[i] {
			t.Errorf("ops[%d] = %+v, want %+v", i, s.Ops[i], want[i])
		}
	}
	if s.DeniedUnattributed != 2 || s.UptimeSeconds != 90 || !s.StartedAt.Equal(start) ||
		s.StartedAt.Location() != time.UTC {
		t.Fatalf("header: %+v", s)
	}
}

func TestRecordConcurrent(t *testing.T) {
	c := New()
	const workers, each = 8, 1000
	stop := make(chan struct{})
	snapDone := make(chan string)
	go func() {
		bad := ""
		for {
			select {
			case <-stop:
				snapDone <- bad
				return
			default:
			}
			for _, o := range c.Snapshot().Ops {
				if o.Requests != o.Denied+o.Rejected+o.LatencyMS.Count && bad == "" {
					bad = o.Workspace + "/" + o.Op
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				switch i % 3 {
				case 0:
					c.Executed("ws", "a.op", int64(i%50), i%2 == 0)
				case 1:
					c.Denied("ws", "a.op")
				default:
					c.Rejected("ws", "b.op")
				}
				if i%100 == 0 {
					c.Unattributed()
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	if bad := <-snapDone; bad != "" {
		t.Fatalf("a mid-run snapshot of %s broke requests = denied + rejected + count", bad)
	}

	// Per worker: i%3==0 -> 334 executed (167 with even i, i.e. i%6==0 -> failed), 333 denied, 333 rejected.
	s := c.Snapshot()
	if len(s.Ops) != 2 {
		t.Fatalf("ops %+v", s.Ops)
	}
	a, b := s.Ops[0], s.Ops[1]
	if a.Requests != workers*667 || a.Denied != workers*333 || a.LatencyMS.Count != workers*334 ||
		a.Errors != workers*167 || a.Rejected != 0 {
		t.Fatalf("a.op %+v", a)
	}
	if b.Requests != workers*333 || b.Rejected != workers*333 {
		t.Fatalf("b.op %+v", b)
	}
	if s.DeniedUnattributed != workers*10 {
		t.Fatalf("unattributed %d", s.DeniedUnattributed)
	}
}

func TestNilCollector(t *testing.T) {
	var c *Collector
	c.Unattributed()
	c.Denied("w", "o")
	c.Rejected("w", "o")
	c.Executed("w", "o", 5, true)
	s := c.Snapshot()
	if s.Ops == nil || len(s.Ops) != 0 || s.DeniedUnattributed != 0 || !s.StartedAt.IsZero() {
		t.Fatalf("nil snapshot %+v", s)
	}
}
