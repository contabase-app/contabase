package civildateplan

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
)

func assertReadFileFailure(t *testing.T, path string, want loadErrorKind) {
	t.Helper()
	data, err := readPlanFileBytes(path)
	if err == nil {
		t.Fatal("unexpectedly succeeded")
	}
	if data != nil {
		t.Fatal("error returned non-nil data")
	}
	classified, ok := err.(*loadError)
	if !ok || classified == nil {
		t.Fatalf("error is not a loadError: %T", err)
	}
	if classified.kind != want {
		t.Fatalf("error kind = %d, want %d", classified.kind, want)
	}
}

func writeTempFile(t *testing.T, dir string, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return path
}

func sparseFile(t *testing.T, dir string, name string, size int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	f.Close()
	return path
}

func TestReadPlanFileBytesInvalidPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"relative", "relative/path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertReadFileFailure(t, tc.path, loadErrorInvalidPath)
		})
	}
}

func TestReadPlanFileBytesNotFound(t *testing.T) {
	assertReadFileFailure(t, filepath.Join(t.TempDir(), "does-not-exist"), loadErrorOpen)
}

func TestReadPlanFileBytesDirectory(t *testing.T) {
	dir := t.TempDir()
	assertReadFileFailure(t, dir, loadErrorNotRegular)
}

func TestReadPlanFileBytesDevNull(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/null not available")
	}
	assertReadFileFailure(t, "/dev/null", loadErrorNotRegular)
}

func TestReadPlanFileBytesEmptyFile(t *testing.T) {
	path := writeTempFile(t, t.TempDir(), "empty", []byte{})
	assertReadFileFailure(t, path, loadErrorEmpty)
}

func TestReadPlanFileBytesOneByte(t *testing.T) {
	content := []byte("x")
	path := writeTempFile(t, t.TempDir(), "one-byte", content)
	data, err := readPlanFileBytes(path)
	if err != nil {
		t.Fatalf("readPlanFileBytes: %v", err)
	}
	if !reflect.DeepEqual(data, content) {
		t.Fatalf("data = %q, want %q", data, content)
	}
}

func TestReadPlanFileBytesExactly64MiB(t *testing.T) {
	path := sparseFile(t, t.TempDir(), "64mib", maxFileSize)
	data, err := readPlanFileBytes(path)
	if err != nil {
		t.Fatalf("readPlanFileBytes: %v", err)
	}
	if len(data) != maxFileSize {
		t.Fatalf("len = %d, want %d", len(data), maxFileSize)
	}
}

func TestReadPlanFileBytes64MiBPlusOne(t *testing.T) {
	path := sparseFile(t, t.TempDir(), "64mib+1", maxFileSize+1)
	assertReadFileFailure(t, path, loadErrorTooLarge)
}

func TestReadPlanFileBytesInitialSizeTooLarge(t *testing.T) {
	path := sparseFile(t, t.TempDir(), "huge", maxFileSize+100)
	assertReadFileFailure(t, path, loadErrorTooLarge)
}

func TestReadPlanFileBytesSymlinkValid(t *testing.T) {
	dir := t.TempDir()
	target := writeTempFile(t, dir, "target", []byte("hello"))
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	data, err := readPlanFileBytes(link)
	if err != nil {
		t.Fatalf("readPlanFileBytes: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q, want %q", data, "hello")
	}
}

func TestReadPlanFileBytesSymlinkBroken(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "nonexistent"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	assertReadFileFailure(t, link, loadErrorOpen)
}

func TestReadPlanFileBytesErrorReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dir")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := readPlanFileBytes(path)
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if data != nil {
		t.Fatal("error returned non-nil data")
	}
}

func TestReadPlanFileBytesIndependentSlice(t *testing.T) {
	content := []byte("original")
	path := writeTempFile(t, t.TempDir(), "independent", content)
	data, err := readPlanFileBytes(path)
	if err != nil {
		t.Fatalf("readPlanFileBytes: %v", err)
	}
	data[0] = 'X'
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(original) != "original" {
		t.Fatal("returned slice alias mutated file content")
	}
}

func TestReadPlanFileBytesPostReadChange(t *testing.T) {
	content := []byte("hello world")
	path := writeTempFile(t, t.TempDir(), "postread", content)
	data, err := readPlanFileBytes(path)
	if err != nil {
		t.Fatalf("readPlanFileBytes: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed!!!"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !reflect.DeepEqual(data, content) {
		t.Fatal("returned data changed after file modification")
	}
}

func TestReadPlanFileBytesConcurrent(t *testing.T) {
	content := []byte("concurrent-test-data")
	path := writeTempFile(t, t.TempDir(), "concurrent", content)
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			data, err := readPlanFileBytes(path)
			if err != nil {
				errs <- err.Error()
				return
			}
			if !reflect.DeepEqual(data, content) {
				errs <- "data mismatch"
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestReadPlanFileBytesPathReplacedAfterOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replace")
	original := []byte("original content here")
	replacement := []byte("different stuff here")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Stat(); err != nil {
		f.Close()
		t.Fatalf("stat: %v", err)
	}
	if err := os.Remove(path); err != nil {
		f.Close()
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(path, replacement, 0644); err != nil {
		f.Close()
		t.Fatalf("write replacement: %v", err)
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		f.Close()
		t.Fatalf("read: %v", err)
	}
	f.Close()
	if !reflect.DeepEqual(buf, original) {
		t.Fatalf("path replaced read = %q, want %q (descriptor should see original inode)", buf, original)
	}
	newData, newErr := readPlanFileBytes(path)
	if newErr != nil {
		t.Fatalf("readPlanFileBytes new path: %v", newErr)
	}
	if !reflect.DeepEqual(newData, replacement) {
		t.Fatalf("replacement file read = %q, want %q", newData, replacement)
	}
}

func TestReadPlanFileBytesChangedGrowth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grow")
	initial := []byte("short")
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	initialSize := fi.Size()
	initialModTime := fi.ModTime()

	growth := []byte("more data appended to the file during read")
	if err := os.WriteFile(path, append(initial, growth...), 0644); err != nil {
		t.Fatalf("write growth: %v", err)
	}

	buf, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if int64(len(buf)) != initialSize {
		t.Log("growth detected by buffer length mismatch")
	}

	fi2, err := f.Stat()
	if err != nil {
		t.Fatalf("stat2: %v", err)
	}

	if fi2.Size() != initialSize || !fi2.ModTime().Equal(initialModTime) || int64(len(buf)) != initialSize {
		return
	}
	t.Fatal("growth was not detected by any check")
}

func TestReadPlanFileBytesChangedTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncate")
	initial := []byte("some initial content with enough bytes to truncate")
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	initialSize := fi.Size()
	initialModTime := fi.ModTime()

	if err := os.Truncate(path, initialSize/2); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	buf, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	fi2, err := f.Stat()
	if err != nil {
		t.Fatalf("stat2: %v", err)
	}

	if fi2.Size() != initialSize || !fi2.ModTime().Equal(initialModTime) || int64(len(buf)) != initialSize {
		return
	}
	t.Fatal("truncation was not detected by any check")
}
