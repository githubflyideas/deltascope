package state

import "testing"

func svcSnap(schema int, items ...Item) Snapshot {
	return Snapshot{Schema: schema, Sections: []Section{{
		Name: "services", Title: "Service Status", Items: items,
	}}}
}

// The setvtrgb class: a running: key present in A, gone in B (systemd
// unloaded the idle oneshot). Same schema, so not a version boundary --
// this must be suppressed by ModifyOnly alone.
func TestRunningKeyDisappearanceIsNotAChange(t *testing.T) {
	a := svcSnap(SchemaVersion,
		Item{Key: "running:setvtrgb.service", Value: "active/exited", ModifyOnly: true},
		Item{Key: "enabled:nginx.service", Value: "enabled"},
	)
	b := svcSnap(SchemaVersion,
		Item{Key: "enabled:nginx.service", Value: "enabled"},
	)
	if d := Compare(a, b); d.Total != 0 {
		t.Errorf("a running: key vanishing is list churn, not a change; got %d: %+v", d.Total, d.Sections)
	}
}

// The appearance direction, likewise.
func TestRunningKeyAppearanceIsNotAChange(t *testing.T) {
	a := svcSnap(SchemaVersion, Item{Key: "enabled:nginx.service", Value: "enabled"})
	b := svcSnap(SchemaVersion,
		Item{Key: "running:NetworkManager-dispatcher.service", Value: "active/running", ModifyOnly: true},
		Item{Key: "enabled:nginx.service", Value: "enabled"},
	)
	if d := Compare(a, b); d.Total != 0 {
		t.Errorf("a running: key appearing is list churn; got %d", d.Total)
	}
}

// The signal we DO want: a provisioned daemon present on both sides whose
// running value flips to failed. ModifyOnly suppresses churn, not this.
func TestRunningValueChangeIsReported(t *testing.T) {
	a := svcSnap(SchemaVersion, Item{Key: "running:nginx.service", Value: "active/running", ModifyOnly: true})
	b := svcSnap(SchemaVersion, Item{Key: "running:nginx.service", Value: "failed/failed", ModifyOnly: true})
	d := Compare(a, b)
	if d.Total != 1 {
		t.Fatalf("a daemon going active/running -> failed IS a change; got %d", d.Total)
	}
	if d.Sections[0].Changes[0].Kind != Modified {
		t.Errorf("expected Modified, got %s", d.Sections[0].Changes[0].Kind)
	}
}

// A genuine enable/disable is NOT modify-only and must still be reported.
func TestEnableChangeStillReported(t *testing.T) {
	a := svcSnap(SchemaVersion, Item{Key: "enabled:nginx.service", Value: "disabled"})
	b := svcSnap(SchemaVersion, Item{Key: "enabled:nginx.service", Value: "enabled"})
	if d := Compare(a, b); d.Total != 1 {
		t.Errorf("enabling a service is a real change; got %d", d.Total)
	}
}

// A newly listening port -- a non-modify-only add -- must still be reported:
// existence itself is the signal for config entities.
func TestConfigAdditionStillReported(t *testing.T) {
	mk := func(schema int, items ...Item) Snapshot {
		return Snapshot{Schema: schema, Sections: []Section{{Name: "listen", Title: "Listening Ports", Items: items}}}
	}
	a := mk(SchemaVersion)
	b := mk(SchemaVersion, Item{Key: "tcp 0.0.0.0:6379", Value: "redis-server"})
	if d := Compare(a, b); d.Total != 1 {
		t.Errorf("a new listening port is a real change; got %d", d.Total)
	}
}

// Cross-version: an old snapshot (schema 0) vs a current one. Every add and
// remove is format migration and must be suppressed, even for config
// entities whose add/remove would normally count. Value changes on shared
// keys still show. And the boundary is flagged.
func TestSchemaBoundarySuppressesMigrationNoise(t *testing.T) {
	// Old binary keyed services bare ("nginx.service"); new binary keys
	// them "running:nginx.service" + "enabled:...". Different keys = all
	// add/remove, which pre-fix flooded the report.
	old := Snapshot{Schema: 0, Sections: []Section{{
		Name: "services", Title: "Service Status",
		Items: []Item{
			{Key: "nginx.service", Value: "active/running"},
			{Key: "docker0-addr", Value: "172.17.0.1/16"},
			{Key: "shared.sysctl", Value: "1"},
		},
	}}}
	cur := Snapshot{Schema: SchemaVersion, Sections: []Section{{
		Name: "services", Title: "Service Status",
		Items: []Item{
			{Key: "running:nginx.service", Value: "active/running", ModifyOnly: true},
			{Key: "enabled:nginx.service", Value: "enabled"},
			{Key: "shared.sysctl", Value: "2"}, // a real value change
		},
	}}}
	d := Compare(old, cur)
	if !d.SchemaBoundary {
		t.Error("a schema mismatch must set SchemaBoundary")
	}
	// only the shared.sysctl value change should survive
	if d.Total != 1 {
		t.Fatalf("across a version boundary only shared value changes show; got %d: %+v", d.Total, d.Sections)
	}
	if d.Sections[0].Changes[0].Key != "shared.sysctl" || d.Sections[0].Changes[0].Kind != Modified {
		t.Errorf("expected the shared.sysctl modify to survive, got %+v", d.Sections[0].Changes[0])
	}
}

// Same schema is the steady state and must behave exactly as before -- a
// config add is reported, proving the boundary logic only kicks in on
// mismatch.
func TestSameSchemaUnaffected(t *testing.T) {
	a := Snapshot{Schema: SchemaVersion, Sections: []Section{{Name: "packages", Title: "Packages", Items: []Item{}}}}
	b := Snapshot{Schema: SchemaVersion, Sections: []Section{{Name: "packages", Title: "Packages", Items: []Item{{Key: "vim", Value: "9.1"}}}}}
	d := Compare(a, b)
	if d.SchemaBoundary {
		t.Error("same schema must not flag a boundary")
	}
	if d.Total != 1 {
		t.Errorf("a package install at the same schema is a real change; got %d", d.Total)
	}
}
