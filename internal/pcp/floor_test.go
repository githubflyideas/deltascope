package pcp

import "testing"

// eventCounters are WorseUp count/sec metrics where the first non-zero
// value IS the finding -- an OOM kill, a listen-queue drop, a SYN-cookie
// emission. For these, 0 -> N is meant to fire (judge routes it through the
// verdictFor path, not a ratio), so they legitimately need no absolute
// floor. Every OTHER WorseUp rate metric must have one, or an idle machine's
// fractional background rate dropping to zero reads as a huge negative
// percentage and lights the report up. This is the udp.noports class of bug.
var eventCounters = map[string]bool{
	"mem.vmstat.oom_kill":          true,
	"network.tcp.listendrops":      true,
	"network.tcp.listenoverflows":  true,
	"network.tcp.syncookiessent":   true,
	"network.tcp.syncookiesfailed": true,
	"network.udp.noports":          false, // has a floor; listed for clarity
}

// TestEveryWorseUpRateHasAFloor is the structural guard the audit called
// for: a WorseUp count/sec metric with no absolute floor is a false
// positive waiting for an idle host. It must either carry a MinAbs or be an
// explicit event counter.
func TestEveryWorseUpRateHasAFloor(t *testing.T) {
	for _, m := range Catalog {
		if m.Polarity != WorseUp {
			continue
		}
		if inferUnit(m.Metric) != "count / sec" {
			continue
		}
		if m.MinAbs > 0 {
			continue
		}
		if eventCounters[m.Metric] {
			continue
		}
		t.Errorf("%s is a WorseUp count/sec metric with no absolute floor and is not an "+
			"event counter -- an idle machine's background rate dropping to zero will flag it. "+
			"Add a floor to minAbsDefault or mark it an event counter.", m.Metric)
	}
}

// TestEveryTriageCoreMetricHasAFloor: a core metric can flip an entire
// triage block red on its own, so it must never be floorless if it is a
// rate. (Gauges and fractions like avactive are exempt -- they are already
// bounded.)
func TestEveryTriageCoreRateHasAFloor(t *testing.T) {
	for metric := range coreMetrics {
		info, ok := Lookup(metric)
		if !ok {
			t.Errorf("coreMetrics lists %s but it is not in the catalog", metric)
			continue
		}
		if inferUnit(metric) != "count / sec" {
			continue
		}
		if info.MinAbs == 0 && !eventCounters[metric] {
			t.Errorf("core metric %s is a floorless rate -- it can redden the whole block on "+
				"idle noise", metric)
		}
	}
}
