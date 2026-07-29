package reasoning

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Machine describes the scale of the host being diagnosed, so a threshold
// can be expressed relative to it instead of as a bare number.
//
// This exists because the aggregate PCP metrics are whole-machine sums.
// kernel.all.cpu.user at 2000 ms/s means "two cores' worth of user time",
// which is most of a 2-core VM and background noise on a 64-core box. A
// state definition written as a fixed 2000 would be correct on one and
// badly wrong on the other. Same for run-queue length, which is only
// meaningful against the number of CPUs available to drain it.
//
// Memory is deliberately NOT used as a scaling base here: "available
// memory is low" is already expressed as a relative change in the state
// definitions, since a byte threshold has no defensible fixed value.
type Machine struct {
	// NCPU is the number of logical CPUs. Never zero -- falls back to the
	// diagnosing process's own view if the host is unreadable, which is
	// correct when deltascope runs on the machine it is diagnosing and a
	// safe approximation otherwise.
	NCPU int `json:"ncpu"`
}

var (
	machineOnce sync.Once
	machineVal  Machine
)

// Host returns the machine description, read once per process.
func Host() Machine {
	machineOnce.Do(func() {
		machineVal = Machine{NCPU: detectNCPU()}
	})
	return machineVal
}

// SetHost overrides the detected machine. Used by tests, and available
// for the case where an archive was collected on a different host than
// the one running deltascope -- in that situation the local CPU count is
// the wrong scaling base, and the caller knows better.
func SetHost(m Machine) {
	if m.NCPU < 1 {
		m.NCPU = 1
	}
	machineOnce.Do(func() {}) // mark as initialised so Host() won't re-detect
	machineVal = m
}

func detectNCPU() int {
	// /proc/cpuinfo is the authoritative view of the host being observed,
	// and unlike runtime.NumCPU() it is not affected by GOMAXPROCS or by
	// a CPU affinity mask on the deltascope process itself -- a service
	// pinned to one core still needs to reason about the whole machine.
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "processor") && strings.Contains(line, ":") {
				n++
			}
		}
		if n > 0 {
			return n
		}
	}
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 1
}

// cores converts a "number of cores' worth of CPU time" figure into the
// ms/s value the aggregate PCP metrics actually report. One fully busy
// core is 1000 ms of CPU time per second of wall time.
func (m Machine) cores(n float64) float64 { return n * 1000 }

// fractionOfMachine converts a fraction of total CPU capacity into ms/s.
// 0.25 on a 8-core host is two cores' worth.
func (m Machine) fractionOfMachine(f float64) float64 {
	return f * float64(m.NCPU) * 1000
}

// perCPU scales a per-CPU quantity by the core count -- used for the run
// queue, where "8 runnable tasks" is a crisis on 2 cores and unremarkable
// on 64.
func (m Machine) perCPU(n float64) float64 { return n * float64(m.NCPU) }

func itoa(n int) string { return strconv.Itoa(n) }
