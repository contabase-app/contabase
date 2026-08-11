//go:build linux || darwin

package civildateplan

import (
	"os"
	"syscall"
)

func openPlanFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
