//go:build windows

package main

import "os"

func hangupSignals() []os.Signal {
	return nil
}
