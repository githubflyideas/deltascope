package pcp

import "math"

// significance.go is the single source of truth for the relative-change
// comparison the product's diffs are built on. The same three-case rule --
// both sides idle, zero denominator, or a real ratio -- was written out in
// judge (metric diff), state.pctChange (process diff), and the reasoning
// matcher. RelChange is that shared core; the callers route through it so a
// change to the rule lands in one place.
//
// It is deliberately ONLY the shared core. The process path layers one
// extra, stricter policy on top (it also rejects a denominator that is
// below the floor while the other side is above it, because a process going
// 0.0005 -> 1.7 % of a core is a division by noise). The metric path does
// NOT want that: a metric like ICMP messages going 10 -> 25 with a floor of
// 20 is a legitimate +150%, and suppressing it would hide a real signal.
// That difference is real, not drift, so it stays in the process path
// rather than being forced into the shared rule.
//
// Returns:
//
//	delta -- the ratio, or nil when a is a zero denominator (0 -> nonzero
//	         has no finite ratio). A nil delta is "no honest percentage",
//	         not "no change".
//	noise -- true when BOTH sides are below minAbs: movement on an idle
//	         metric, not a signal, whatever the ratio. minAbs <= 0 disables
//	         the floor.
func RelChange(a, b, minAbs float64) (delta *float64, noise bool) {
	bothIdle := minAbs > 0 && math.Abs(a) < minAbs && math.Abs(b) < minAbs
	if a == 0 {
		if b == 0 {
			z := 0.0
			return &z, bothIdle
		}
		return nil, bothIdle // 0 -> nonzero: no finite ratio
	}
	d := (b - a) / math.Abs(a) * 100
	return &d, bothIdle
}
