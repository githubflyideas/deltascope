package httpapi

import (
	"os"
	"os/exec"
)

// Capabilities records which data sources this host can actually serve.
//
// deltascope has four engines but only two data sources: PCP archives (read
// through pmlogsummary/pmrep) and the local snapshot store (which reads
// /proc and config files directly and needs no PCP at all). On a host
// without PCP -- Ubuntu and Debian do not ship pmlogger enabled the way
// RHEL-family distributions do -- the archive-backed engines can only fail.
// Reporting that up front lets the UI gray them out and land the user on an
// engine that works, instead of greeting a first-time user with a broken
// chart and a 502.
type Capabilities struct {
	// Metrics is true when the PCP archive path is queryable: both tools
	// present and the archive directory readable. It gates the regression
	// diff, the trend charts and the reasoning chain -- all three have no
	// data source other than pmlogsummary/pmrep.
	Metrics bool `json:"metrics"`

	// Change is true when the snapshot store opened, which gates change
	// accounting and process accounting. Independent of PCP entirely.
	Change bool `json:"change"`

	// Reason explains a false Metrics in one actionable sentence, so the UI
	// can tell the operator what to install rather than just that something
	// is missing.
	Reason string `json:"reason,omitempty"`
}

// DetectPCP reports whether the PCP archive path can serve metric queries.
// Detection happens once at startup: installing PCP afterwards needs a
// restart to be picked up, which the returned reason says out loud.
//
// A readable archive directory is not proof that it holds data for any
// particular window -- pmlogger may have only just started, or archives may
// have rotated away. Those cases still surface as a per-request error; this
// check only answers the coarser question of whether asking is worth it.
func DetectPCP(archive string) (bool, string) {
	var missing []string
	if _, err := exec.LookPath("pmlogsummary"); err != nil {
		missing = append(missing, "pmlogsummary (package: pcp)")
	}
	if _, err := exec.LookPath("pmrep"); err != nil {
		missing = append(missing, "pmrep (package: pcp-system-tools)")
	}
	if len(missing) > 0 {
		return false, "PCP tools not installed: " + joinAnd(missing) +
			". Install them and restart deltascope to enable metric comparison."
	}
	if _, err := os.Stat(archive); err != nil {
		return false, "PCP archive directory " + archive +
			" is not readable. Check that pmlogger is running, or point -archive at the right path."
	}
	return true, ""
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	out := xs[0]
	for _, x := range xs[1 : len(xs)-1] {
		out += ", " + x
	}
	return out + " and " + xs[len(xs)-1]
}
