package scheduler

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Readers and Cancel run while the scheduler executes jobs; run with -race.
func TestConcurrentExecuteAndRead(t *testing.T) {
	st := NewStore()
	sc := NewScheduler(st)
	for i := 0; i < 50; i++ {
		_ = st.Add(&Job{ID: fmt.Sprintf("j%d", i), AgentID: "a", Workspace: "w", Type: "unknown", ScheduledAt: time.Now().Add(-time.Second)})
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); sc.processJobs() }()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			for _, j := range st.ListByAgent("a") {
				_ = j.Status
				_ = j.Result
			}
			for _, j := range st.ListByWorkspace("w") {
				_ = j.LastRun
			}
			if j, ok := st.Get("j7"); ok {
				_ = j.Status
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i += 2 {
			_ = st.Cancel(fmt.Sprintf("j%d", i), "a")
		}
	}()
	wg.Wait()
}

func TestFinishDoesNotOverwriteCancel(t *testing.T) {
	st := NewStore()
	_ = st.Add(&Job{ID: "x", AgentID: "a", ScheduledAt: time.Now().Add(-time.Second)})
	if _, ok := st.claim("x", time.Now()); !ok {
		t.Fatal("claim failed")
	}
	if err := st.Cancel("x", "a"); err != nil {
		t.Fatal(err)
	}
	st.finish("x", &JobResult{Success: true})
	if j, _ := st.Get("x"); j.Status != "cancelled" {
		t.Fatalf("status %q, want cancelled", j.Status)
	}
	// a cancelled job is not claimed again
	if _, ok := st.claim("x", time.Now()); ok {
		t.Fatal("cancelled job was claimed")
	}
}

func TestExecuteJobSetsTerminalStatus(t *testing.T) {
	st := NewStore()
	sc := NewScheduler(st)
	_ = st.Add(&Job{ID: "x", AgentID: "a", Type: "unknown", ScheduledAt: time.Now().Add(-time.Second)})
	sc.processJobs()
	j, _ := st.Get("x")
	if j.Status != "failed" || j.Result == nil || j.Result.Success || j.LastRun == nil {
		t.Fatalf("unexpected job %+v", j)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	st := NewStore()
	_ = st.Add(&Job{ID: "x", AgentID: "a", Params: map[string]interface{}{"k": 1}})
	j, _ := st.Get("x")
	j.Status = "hacked"
	j.Params["k"] = 2
	again, _ := st.Get("x")
	if again.Status != "pending" || again.Params["k"] != 1 {
		t.Fatalf("store mutated through returned copy: %+v", again)
	}
}
