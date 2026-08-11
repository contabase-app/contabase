//go:build linux || darwin

package civildateplan

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestReadPlanFileBytesFIFO(t *testing.T) {
	if fifoPath := os.Getenv("CONTABASE_FIFO_PATH"); fifoPath != "" {
		data, err := readPlanFileBytes(fifoPath)
		if err == nil {
			fmt.Fprintf(os.Stderr, "FIFO child: unexpectedly succeeded\n")
			os.Exit(1)
		}
		if data != nil {
			fmt.Fprintf(os.Stderr, "FIFO child: error returned non-nil data\n")
			os.Exit(2)
		}
		classified, ok := err.(*loadError)
		if !ok || classified.kind != loadErrorNotRegular {
			fmt.Fprintf(os.Stderr, "FIFO child: unexpected error kind: %v\n", err)
			os.Exit(3)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifoPath, 0600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestReadPlanFileBytesFIFO$")
	cmd.Env = append(os.Environ(), "CONTABASE_FIFO_PATH="+fifoPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(5_000_000_000)
	defer timer.Stop()

	select {
	case runErr := <-done:
		if !timer.Stop() {
			<-timer.C
		}
		if runErr != nil {
			t.Fatalf("FIFO child failed: %v\nstderr: %s", runErr, stderr.String())
		}
	case <-timer.C:
		cmd.Process.Kill()
		<-done
		t.Fatal("FIFO open/read blocked (O_NONBLOCK missing or ineffective)")
	}
}

func TestReadPlanFileBytesUnixSocket(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix domain sockets not available")
	}
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "socket")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer ln.Close()

	data, err := readPlanFileBytes(sockPath)
	if err == nil {
		t.Fatal("expected error for Unix socket")
	}
	if data != nil {
		t.Fatal("error returned non-nil data for socket")
	}
	classified, ok := err.(*loadError)
	if !ok || classified.kind != loadErrorOpen {
		t.Fatalf("expected loadErrorOpen for socket, got %T: %v", err, err)
	}
}
