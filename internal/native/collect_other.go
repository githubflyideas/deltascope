//go:build !linux

package native

import "time"

// snapshot on a non-Linux host has nothing to read: every source in this
// package is a Linux /proc or statfs interface. It returns ErrUnsupported
// rather than an empty sample so a caller cannot mistake "cannot measure
// this machine" for "this machine has no problems" -- the same distinction
// the rest of the package is built around.
//
// This file exists so the package compiles and its parsers stay testable
// on any OS. deltascope is developed on Windows and deployed on Linux, and
// a parser that can only be exercised on the target host is a parser that
// ships unverified.
func snapshot(time.Time) (Sample, error) {
	return Sample{}, ErrUnsupported
}
