//go:build windows

package metrics

import "testing"

func TestProcessStats(t *testing.T) {
	p1 := readProcess()
	// Burn a little CPU so the second reading has something to add.
	x := 0
	for i := 0; i < 5_000_000; i++ {
		x += i % 7
	}
	_ = x
	p2 := readProcess()
	for _, p := range []Process{p1, p2} {
		if p.CPUSeconds == nil || *p.CPUSeconds < 0 {
			t.Fatalf("cpu_seconds %v", p.CPUSeconds)
		}
		if p.RSSBytes == nil || *p.RSSBytes == 0 {
			t.Fatalf("rss_bytes %v", p.RSSBytes)
		}
		if p.Threads == nil || *p.Threads < 1 {
			t.Fatalf("threads %v", p.Threads)
		}
		if p.Goroutines < 1 {
			t.Fatalf("goroutines %d", p.Goroutines)
		}
	}
	if *p2.CPUSeconds < *p1.CPUSeconds {
		t.Fatalf("cpu_seconds went down: %v -> %v", *p1.CPUSeconds, *p2.CPUSeconds)
	}
}
