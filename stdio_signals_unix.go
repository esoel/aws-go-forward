//go:build !windows

package main

import (
	"os"
	"syscall"
)

func hangupSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP}
}
