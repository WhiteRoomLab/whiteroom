package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// canReadHidden is true where readHidden can read from a terminal without echo.
const canReadHidden = true

// ioctl calls the ioctl req of f with arg, which has the type that req takes, without changing how f is read.
func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	return ioctl(f, syscall.TCGETS, unsafe.Pointer(&t)) == nil
}

// readHidden reads a line of at most limit bytes from the terminal f, without echo and without its line break, and
// restores the terminal as it was, also if ctx is done first, when it returns ctx.Err(). A line that is longer is read to
// its end and then is errTooLong. If ctx is done, the goroutine that reads stays until the terminal gives it a line or ends.
func readHidden(ctx context.Context, f *os.File, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var old syscall.Termios
	if err := ioctl(f, syscall.TCGETS, unsafe.Pointer(&old)); err != nil {
		return nil, fmt.Errorf("cannot set up the terminal: %w", err)
	}
	hidden := old
	hidden.Lflag &^= syscall.ECHO
	hidden.Lflag |= syscall.ICANON | syscall.ISIG
	hidden.Iflag |= syscall.ICRNL
	if err := ioctl(f, syscall.TCSETS, unsafe.Pointer(&hidden)); err != nil {
		return nil, fmt.Errorf("cannot set up the terminal: %w", err)
	}
	restore := func() error {
		if err := ioctl(f, syscall.TCSETS, unsafe.Pointer(&old)); err != nil {
			return fmt.Errorf("cannot restore the terminal: %w", err)
		}
		return nil
	}

	type result struct {
		line []byte
		err  error
	}
	read := make(chan result, 1)
	go func() {
		line, err := readLine(f, limit, true)
		read <- result{line, err}
	}()
	select {
	case r := <-read:
		if err := restore(); err != nil {
			clear(r.line)
			return nil, err
		}
		return r.line, r.err
	case <-ctx.Done():
		if err := restore(); err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
}
