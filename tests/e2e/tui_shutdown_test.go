package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type terminalOutput struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (output *terminalOutput) Write(data []byte) (int, error) {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.buffer.Write(data)
}

func (output *terminalOutput) contains(value string) bool {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return strings.Contains(output.buffer.String(), value)
}

func assertTUISignalExits(t *testing.T, ctx context.Context, binary string, environment []string) {
	t.Helper()
	for _, mode := range []string{"quit", "interrupt", "terminate", "quit-interrupt"} {
		for iteration := range 5 {
			t.Run(fmt.Sprintf("%s-%d", mode, iteration), func(t *testing.T) {
				assertTUITerminalExit(t, ctx, binary, environment, mode)
			})
		}
	}
}

func assertTUITerminalExit(t *testing.T, parent context.Context, binary string, environment []string, mode string) {
	t.Helper()
	master, slave := openTestPTY(t)
	defer func() { _ = master.Close(); _ = slave.Close() }()
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "tui")
	command.Env = append(append([]string{}, environment...), "TERM=xterm-256color")
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			cancel()
			_ = command.Wait()
		}
	}()
	output := &terminalOutput{}
	readerDone := make(chan struct{})
	go func() { _, _ = io.Copy(output, master); close(readerDone) }()
	defer func() {
		if command.ProcessState == nil {
			cancel()
			_ = command.Wait()
		}
		_ = slave.Close()
		_ = master.Close()
		<-readerDone
	}()
	for !output.contains("Downloads") {
		if ctx.Err() != nil {
			t.Fatal("TUI did not render its initial screen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if mode == "quit" || mode == "quit-interrupt" {
		if _, err := master.Write([]byte("q")); err != nil {
			t.Fatal(err)
		}
	}
	if mode != "quit" {
		sig := os.Signal(os.Interrupt)
		if mode == "terminate" {
			sig = syscall.SIGTERM
		}
		if err := command.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatal(err)
		}
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("TUI shutdown failed or hung: %v (context: %v)", err, ctx.Err())
	}
	restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || restored.Lflag != original.Lflag || restored.Iflag != original.Iflag || restored.Oflag != original.Oflag {
		t.Fatalf("terminal settings were not restored: original=%+v restored=%+v error=%v", original, restored, err)
	}
	deadline := time.Now().Add(time.Second)
	for !output.contains("\x1b[?1049l") {
		if time.Now().After(deadline) {
			t.Fatal("TUI did not leave the alternate screen")
		}
		time.Sleep(time.Millisecond)
	}
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 100}); err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatal(err)
	}
	return master, slave
}
