package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRequireMetricsPassesWhenAvailable is the regression that matters most:
// a host that does have PCP must not be gated at all, or the gate would have
// broken the feature it was meant to explain.
func TestRequireMetricsPassesWhenAvailable(t *testing.T) {
	s := &Server{Caps: Capabilities{Metrics: true}}
	rec := httptest.NewRecorder()
	if !s.requireMetrics(rec) {
		t.Fatal("requireMetrics denied a host that has PCP")
	}
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("requireMetrics wrote a response on the success path: %d %q", rec.Code, rec.Body)
	}
}

// TestRequireMetricsRejectsWithReason locks the contract the UI depends on:
// 503 (the feature is not available here) rather than 502 (something broke),
// and the operator-actionable reason in the body rather than a generic
// message. Without the reason travelling through, the user sees "unavailable"
// and has nothing to act on.
func TestRequireMetricsRejectsWithReason(t *testing.T) {
	const why = "PCP tools not installed: pmlogsummary (package: pcp)"
	s := &Server{Caps: Capabilities{Metrics: false, Reason: why}}
	rec := httptest.NewRecorder()
	if s.requireMetrics(rec) {
		t.Fatal("requireMetrics allowed a host with no PCP")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the {\"error\": ...} shape the frontend parses: %v", err)
	}
	if body["error"] != why {
		t.Errorf("error = %q, want the detection reason %q", body["error"], why)
	}
}

// An empty Reason must still produce a sentence. Capabilities is a plain
// struct, so a caller can leave Reason unset; falling through to an empty
// error string would render a blank notice in the UI.
func TestRequireMetricsFallsBackWhenReasonEmpty(t *testing.T) {
	s := &Server{Caps: Capabilities{Metrics: false}}
	rec := httptest.NewRecorder()
	s.requireMetrics(rec)
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if strings.TrimSpace(body["error"]) == "" {
		t.Error("empty Reason produced an empty error message")
	}
}

func TestMetricsNote(t *testing.T) {
	if got := (&Server{Caps: Capabilities{Metrics: true}}).metricsNote(); got != "" {
		t.Errorf("note on a PCP host = %q, want empty so diagnose stays silent", got)
	}
	s := &Server{Caps: Capabilities{Reason: "pmrep is missing"}}
	got := s.metricsNote()
	if !strings.Contains(got, "pmrep is missing") {
		t.Errorf("note = %q, want it to carry the reason", got)
	}
	if strings.TrimSpace((&Server{}).metricsNote()) == "" {
		t.Error("note with no reason set is empty; diagnose would report a blank note")
	}
}

func TestJoinAnd(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b and c"},
	} {
		if got := joinAnd(c.in); got != c.want {
			t.Errorf("joinAnd(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDetectPCPWithoutTools points PATH at an empty directory so neither
// tool can be found, which is exactly the Ubuntu-without-pcp case this
// whole code path exists for. The reason must name the packages to install,
// since "not installed" alone leaves the operator guessing.
func TestDetectPCPWithoutTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ok, why := DetectPCP(t.TempDir())
	if ok {
		t.Fatal("DetectPCP found PCP tools on an empty PATH")
	}
	for _, want := range []string{"pmlogsummary", "pmrep", "pcp"} {
		if !strings.Contains(why, want) {
			t.Errorf("reason %q does not mention %q", why, want)
		}
	}
}

// TestDetectPCPArchiveMissing covers the second failure mode: the tools are
// installed but pmlogger never wrote anything, which reads very differently
// to the operator and so must produce a different message.
func TestDetectPCPArchiveMissing(t *testing.T) {
	if runtime.GOOS == "linux" {
		bin := t.TempDir()
		for _, name := range []string{"pmlogsummary", "pmrep"} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin)
	} else {
		// LookPath needs a PATHEXT-matching extension on Windows, so a stub
		// script would not be discoverable. Skip rather than assert the
		// wrong branch and pass for the wrong reason.
		t.Skip("stubbing exec.LookPath needs a POSIX PATH")
	}
	missing := filepath.Join(t.TempDir(), "no-such-archive-dir")
	ok, why := DetectPCP(missing)
	if ok {
		t.Fatal("DetectPCP accepted a nonexistent archive directory")
	}
	if !strings.Contains(why, missing) {
		t.Errorf("reason %q does not name the directory it could not read", why)
	}
}
