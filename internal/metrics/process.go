package metrics

import "runtime"

// Process is the gateway process's resource use. A field the platform (or a
// failing system call) cannot supply is nil and omitted from the JSON.
type Process struct {
	CPUSeconds *float64 `json:"cpu_seconds,omitempty"` // user + kernel time
	RSSBytes   *uint64  `json:"rss_bytes,omitempty"`   // working set
	Goroutines int      `json:"goroutines"`
	Threads    *uint32  `json:"threads,omitempty"` // OS threads of the process
}

func readProcess() Process {
	p := Process{Goroutines: runtime.NumGoroutine()}
	readPlatform(&p)
	return p
}
