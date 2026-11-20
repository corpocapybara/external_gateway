//go:build !windows

package metrics

// readPlatform supplies nothing off Windows: CPU, RSS and threads stay omitted.
func readPlatform(*Process) {}
