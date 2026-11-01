//go:build contabase_legacy_fixtures && !unix

package database

import "testing"

func brelPeakRSS(t *testing.T) uint64 {
	t.Helper()
	t.Log("RSS unavailable on this platform")
	return 0
}
