package main

import (
	"os"
	"syscall"
	"unsafe"
)

// stdinIsTerminal asks the OS whether stdin is a terminal, which the --ticket
// confirmation and the Planning Session need.
func stdinIsTerminal() bool { return isTerminal(os.Stdin) }

// stdoutIsTerminal asks the OS whether stdout is a terminal, which decides
// whether a Dashboard starts.
func stdoutIsTerminal() bool { return isTerminal(os.Stdout) }

// isTerminal asks the OS whether f is a terminal. Being a character device
// isn't enough, because /dev/null is one too, and cron runs commands with stdin
// on /dev/null.
func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), getTermios, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
