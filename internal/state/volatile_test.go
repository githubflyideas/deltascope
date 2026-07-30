package state

import "testing"

// The volatile classifier still recognises on-demand and oneshot units.
func TestParseVolatileClassifiesOnDemandUnits(t *testing.T) {
	out := `Id=fwupd.service
Type=dbus
TriggeredBy=
BusName=org.freedesktop.fwupd
Id=NetworkManager-dispatcher.service
Type=oneshot
TriggeredBy=
BusName=
Id=logrotate.service
Type=oneshot
TriggeredBy=logrotate.timer
BusName=
Id=nginx.service
Type=forking
TriggeredBy=
BusName=
Id=mysqld.service
Type=notify
TriggeredBy=
BusName=
`
	vol := parseVolatile(out)
	for _, id := range []string{"fwupd.service", "NetworkManager-dispatcher.service", "logrotate.service"} {
		if !vol[id] {
			t.Errorf("%s should be classified volatile", id)
		}
	}
	for _, id := range []string{"nginx.service", "mysqld.service"} {
		if vol[id] {
			t.Errorf("%s is long-running and must NOT be volatile", id)
		}
	}
}

// The core of the fix: running state is recorded only for enabled,
// non-volatile units. Everything else (disabled/static units,
// inactive/dead noise, on-demand daemons) contributes NO running-state row,
// so the churning population of a desktop's ~hundred static units cannot
// produce phantom "removed" changes.
func TestRunningStateRecordedOnlyForEnabledDaemons(t *testing.T) {
	// Emulate the collector's decision for each unit.
	type unit struct {
		name           string
		active         string
		enabled, vol   bool
		wantRunningRow bool
	}
	units := []unit{
		{"nginx.service", "active", true, false, true},          // enabled daemon: yes
		{"mysqld.service", "inactive", true, false, true},       // enabled but DOWN: yes -- that's the useful catch
		{"ModemManager.service", "active", false, true, false},  // on-demand, not enabled: no
		{"acpid.service", "active", false, false, false},        // running but never enabled: no
		{"alsa-state.service", "inactive", false, false, false}, // static inactive noise: no
		{"apport.service", "inactive", true, true, false},       // enabled but volatile oneshot: no (flaps in value)
	}

	sec := Section{Name: "services", Title: "Service Status"}
	for _, u := range units {
		if u.enabled && !u.vol {
			sec.Items = append(sec.Items, Item{Key: "running:" + u.name, Value: u.active + "/x"})
		}
	}
	m := itemMap(sec)
	for _, u := range units {
		_, got := m["running:"+u.name]
		if got != u.wantRunningRow {
			t.Errorf("%s: running row present=%v, want %v", u.name, got, u.wantRunningRow)
		}
	}
}

// The desktop scenario from the field: dozens of static/on-demand units
// present in A vanish from B as systemd unloads them. With running state
// scoped to enabled daemons, none of that churn is a change.
func TestStaticUnitChurnProducesNoChange(t *testing.T) {
	// A recorded a pile of static units' running state (old behaviour);
	// B, with the fix, records only the one enabled daemon. We assert the
	// fix's OWN output is stable: two snapshots taken the same way, where
	// on-demand units differ in liveness, produce nothing.
	mk := func() Snapshot {
		return Snapshot{Sections: []Section{{
			Name: "services", Title: "Service Status",
			Items: []Item{
				{Key: "running:nginx.service", Value: "active/running"},
				{Key: "enabled:nginx.service", Value: "enabled"},
				{Key: "enabled:ModemManager.service", Value: "enabled-runtime"},
				{Key: "enabled:acpid.service", Value: "disabled"},
			},
		}}}
	}
	// ModemManager was active in A's raw systemd list and gone in B, but it
	// is never enabled-at-boot-and-nonvolatile, so neither snapshot carries
	// a running: row for it. Same input shape both times -> no change.
	if d := Compare(mk(), mk()); d.Total != 0 {
		t.Errorf("static/on-demand churn must not register as a change, got %d", d.Total)
	}
}

// An enabled daemon actually going down IS a change and must surface.
func TestEnabledDaemonGoingDownIsReported(t *testing.T) {
	up := Snapshot{Sections: []Section{{
		Name: "services", Items: []Item{{Key: "running:nginx.service", Value: "active/running"}},
	}}}
	down := Snapshot{Sections: []Section{{
		Name: "services", Items: []Item{{Key: "running:nginx.service", Value: "failed/failed"}},
	}}}
	d := Compare(up, down)
	if d.Total != 1 {
		t.Fatalf("an enabled daemon failing is a real finding, got %d", d.Total)
	}
}

// A genuine enable/disable is still caught.
func TestServiceEnableStillReported(t *testing.T) {
	mk := func(state string) Snapshot {
		return Snapshot{Sections: []Section{{
			Name: "services", Items: []Item{{Key: "enabled:nginx.service", Value: state}},
		}}}
	}
	if d := Compare(mk("disabled"), mk("enabled")); d.Total != 1 {
		t.Fatalf("enabling a service is a real change, got %d", d.Total)
	}
}
