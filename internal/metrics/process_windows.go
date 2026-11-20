//go:build windows

package metrics

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// golang.org/x/sys/windows has no GetProcessMemoryInfo; kernel32 exports it as
// K32GetProcessMemoryInfo since Windows 7.
var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// processMemoryCounters is PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func readPlatform(p *Process) {
	self := windows.CurrentProcess()

	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(self, &created, &exited, &kernel, &user) == nil {
		// Filetime counts 100 ns units.
		ticks := filetimeTicks(kernel) + filetimeTicks(user)
		cpu := float64(ticks) / 1e7
		p.CPUSeconds = &cpu
	}

	if procGetProcessMemoryInfo.Find() == nil {
		var pmc processMemoryCounters
		pmc.cb = uint32(unsafe.Sizeof(pmc))
		r1, _, _ := procGetProcessMemoryInfo.Call(uintptr(self), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb))
		if r1 != 0 {
			rss := uint64(pmc.WorkingSetSize)
			p.RSSBytes = &rss
		}
	}

	if n, ok := ownThreadCount(); ok {
		p.Threads = &n
	}
}

func filetimeTicks(f windows.Filetime) uint64 {
	return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)
}

// ownThreadCount reads this process's thread count from a Toolhelp32 process
// snapshot.
func ownThreadCount() (uint32, bool) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(snap)
	pid := windows.GetCurrentProcessId()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == pid {
			return e.Threads, true
		}
	}
	return 0, false
}
