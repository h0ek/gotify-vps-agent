package terminal

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const maxPasswordBytes = 4096

func ReadPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	var oldState syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&oldState)), 0, 0, 0)
	if errno != 0 {
		return "", fmt.Errorf("application token must be entered from a terminal: %w", errno)
	}

	newState := oldState
	newState.Lflag &^= syscall.ECHO
	_, _, errno = syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&newState)), 0, 0, 0)
	if errno != 0 {
		return "", errno
	}

	var once sync.Once
	restore := func() {
		once.Do(func() {
			_, _, _ = syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&oldState)), 0, 0, 0)
		})
	}
	defer func() {
		restore()
		fmt.Println()
	}()

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	defer close(done)
	go func() {
		select {
		case received := <-signals:
			restore()
			signal.Reset(received)
			if value, ok := received.(syscall.Signal); ok {
				_ = syscall.Kill(os.Getpid(), value)
			}
		case <-done:
		}
	}()

	fmt.Print(prompt)
	reader := bufio.NewReaderSize(os.Stdin, maxPasswordBytes+1)
	value, err := reader.ReadString('\n')
	if len(value) > maxPasswordBytes {
		return "", fmt.Errorf("application token exceeds %d bytes", maxPasswordBytes)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
