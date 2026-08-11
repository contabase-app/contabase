//go:build !linux && !darwin

package civildateplan

import "os"

func openPlanFile(path string) (*os.File, error) {
	return nil, newLoadError(loadErrorUnsupportedPlatform)
}
