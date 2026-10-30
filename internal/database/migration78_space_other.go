//go:build !unix

package database

import "fmt"

func admission78FreeBytes(string) (uint64, error) {
	return 0, fmt.Errorf("migration 78 space verification unavailable on this platform")
}
