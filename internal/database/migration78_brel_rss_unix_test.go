//go:build unix

package database

import (
	"golang.org/x/sys/unix"
	"runtime"
	"testing"
)

func brelPeakRSS(t *testing.T) uint64 {
	t.Helper()
	var r unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &r); err != nil {
		t.Logf("RSS unavailable: %v", err)
		return 0
	}
	v := uint64(r.Maxrss)
	if runtime.GOOS != "darwin" {
		v *= 1024
	}
	return v
}
