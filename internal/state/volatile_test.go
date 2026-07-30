package state

import "testing"

// The three units from the real report -- each volatile for a different
// reason -- must be classified as noise, while an ordinary long-running
// service must not.
func TestParseVolatileClassifiesOnDemandUnits(t *testing.T) {
	out := `Id=fwupd.service
Type=dbus
TriggeredBy=
BusName=org.freedesktop.fwupd
Id=fprintd.service
Type=dbus
TriggeredBy=
BusName=net.reactivated.Fprint
Id=NetworkManager-dispatcher.service
Type=oneshot
TriggeredBy=
BusName=
Id=logrotate.service
Type=oneshot
TriggeredBy=logrotate.timer
BusName=
Id=man-db.service
Type=oneshot
TriggeredBy=man-db.timer
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

	for _, id := range []string{
		"fwupd.service",                     // D-Bus activated
		"fprintd.service",                   // D-Bus activated
		"NetworkManager-dispatcher.service", // oneshot
		"logrotate.service",                 // timer-triggered oneshot
		"man-db.service",                    // timer-triggered
	} {
		if !vol[id] {
			t.Errorf("%s should be classified volatile (on-demand/oneshot)", id)
		}
	}
	for _, id := range []string{"nginx.service", "mysqld.service"} {
		if vol[id] {
			t.Errorf("%s is a long-running service and must NOT be suppressed", id)
		}
	}
}

// The running-state row of a volatile service must be dropped from the
// snapshot, but its enabled-state row must survive: whether fwupd is set
// to start is real configuration, whether it happens to be running right
// now is not.
func TestVolatileRunningStateDroppedButEnabledKept(t *testing.T) {
	sec := Section{Name: "services", Title: "Service Status"}
	vol := map[string]bool{"fwupd.service": true}

	// mimic the collector's two loops over one volatile + one normal unit
	units := []struct{ name, active string }{
		{"fwupd.service", "active/running"},
		{"nginx.service", "active/running"},
	}
	for _, u := range units {
		if vol[u.name] {
			continue
		}
		sec.Items = append(sec.Items, Item{Key: u.name, Value: u.active})
	}
	for _, u := range units {
		sec.Items = append(sec.Items, Item{Key: "enabled:" + u.name, Value: "enabled"})
	}

	m := itemMap(sec)
	if _, ok := m["fwupd.service"]; ok {
		t.Error("fwupd running-state row should have been dropped")
	}
	if _, ok := m["enabled:fwupd.service"]; !ok {
		t.Error("fwupd enabled-state row must be kept -- that is the real config")
	}
	if _, ok := m["nginx.service"]; !ok {
		t.Error("nginx running-state row must be kept")
	}
}

// A machine where fwupd exited between snapshots must produce no service
// change once the running row is filtered -- the exact false positive from
// the report.
func TestOnDemandServiceExitProducesNoChange(t *testing.T) {
	mk := func(fwupdActive string, withFwupdRow bool) Snapshot {
		sec := Section{Name: "services", Title: "Service Status"}
		if withFwupdRow {
			sec.Items = append(sec.Items, Item{Key: "fwupd.service", Value: fwupdActive})
		}
		sec.Items = append(sec.Items,
			Item{Key: "nginx.service", Value: "active/running"},
			Item{Key: "enabled:fwupd.service", Value: "enabled"},
			Item{Key: "enabled:nginx.service", Value: "enabled"},
		)
		return Snapshot{Sections: []Section{sec}}
	}
	// Steady state after the filter ships: both snapshots omit fwupd's
	// running row entirely (it is volatile), so its exit is invisible to
	// the diff -- which is the whole point.
	a := mk("", false)
	b := mk("", false)

	d := Compare(a, b)
	if d.Total != 0 {
		t.Errorf("an on-demand service exiting must not count as a change, got %d: %+v", d.Total, d.Sections)
	}
}

// The filter must not swallow a genuine enable/disable: that is precisely
// what change accounting exists to catch.
func TestServiceEnableStillReported(t *testing.T) {
	mkEnabled := func(state string) Snapshot {
		return Snapshot{Sections: []Section{{
			Name: "services", Title: "Service Status",
			Items: []Item{{Key: "enabled:nginx.service", Value: state}},
		}}}
	}
	d := Compare(mkEnabled("disabled"), mkEnabled("enabled"))
	if d.Total != 1 {
		t.Fatalf("enabling a service is a real change and must be reported, got %d", d.Total)
	}
}
