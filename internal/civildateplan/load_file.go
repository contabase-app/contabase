package civildateplan

import (
	"io"
	"path/filepath"
)

const maxFileSize = 64 * 1024 * 1024

func readPlanFileBytes(path string) ([]byte, error) {
	if path == "" {
		return nil, newLoadError(loadErrorInvalidPath)
	}
	if !filepath.IsAbs(path) {
		return nil, newLoadError(loadErrorInvalidPath)
	}

	f, err := openPlanFile(path)
	if err != nil {
		if _, ok := err.(*loadError); ok {
			return nil, err
		}
		return nil, newLoadError(loadErrorOpen)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, newLoadError(loadErrorOpen)
	}
	if !fi.Mode().IsRegular() {
		return nil, newLoadError(loadErrorNotRegular)
	}

	initialSize := fi.Size()
	initialModTime := fi.ModTime()
	if initialSize > maxFileSize {
		return nil, newLoadError(loadErrorTooLarge)
	}

	buf, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, newLoadError(loadErrorRead)
	}

	if len(buf) > maxFileSize {
		return nil, newLoadError(loadErrorTooLarge)
	}

	fi2, err := f.Stat()
	if err != nil {
		return nil, newLoadError(loadErrorRead)
	}
	if !fi2.Mode().IsRegular() {
		return nil, newLoadError(loadErrorChangedDuringRead)
	}

	if fi2.Size() != initialSize || !fi2.ModTime().Equal(initialModTime) || int64(len(buf)) != initialSize {
		return nil, newLoadError(loadErrorChangedDuringRead)
	}

	if len(buf) == 0 {
		return nil, newLoadError(loadErrorEmpty)
	}

	result := make([]byte, len(buf))
	copy(result, buf)
	return result, nil
}
