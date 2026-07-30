package state

import "testing"

func netItems(sec Section) map[string]string {
	m := map[string]string{}
	for _, it := range sec.Items {
		m[it.Key] = it.Value
	}
	return m
}

// Ephemeral container/VM interfaces churn their addresses and routes on
// every workload start. Those must not be recorded, or a container restart
// reads as a network configuration change. A real interface must survive.
func TestEphemeralInterfaceRegexClassification(t *testing.T) {
	volatile := []string{
		"veth1a2b3c", "docker0", "br-9f8e7d6c5b4a", "cni0", "flannel.1",
		"cali1234abcd", "cilium_host", "tap0", "tun0", "vnet3",
	}
	for _, n := range volatile {
		if !ephemeralIfaceRe.MatchString(n) {
			t.Errorf("%s should be classified ephemeral", n)
		}
	}
	real := []string{"eth0", "ens3", "eno1", "enp0s31f6", "bond0", "br0", "wlan0", "lo"}
	for _, n := range real {
		if ephemeralIfaceRe.MatchString(n) {
			t.Errorf("%s is an operator-managed interface and must NOT be filtered", n)
		}
	}
}

// A veth pair prints as "vethXXXX@if7"; the @peer suffix must be stripped
// before matching.
func TestVethPeerSuffixStripped(t *testing.T) {
	if ifaceName("veth1a2b@if7") != "veth1a2b" {
		t.Errorf("ifaceName should strip @peer, got %q", ifaceName("veth1a2b@if7"))
	}
	if !ephemeralIfaceRe.MatchString(ifaceName("veth1a2b@if7")) {
		t.Error("a veth with an @peer suffix must still be classified ephemeral")
	}
}

// routeDev finds the device in an `ip route` line.
func TestRouteDev(t *testing.T) {
	got := routeDev([]string{"default", "via", "10.0.0.1", "dev", "eth0"})
	if got != "eth0" {
		t.Errorf("routeDev = %q, want eth0", got)
	}
	if routeDev([]string{"blackhole", "10.1.1.0/24"}) != "" {
		t.Error("a route with no dev should return empty")
	}
}

// The self-inflicted one: deltascope's own listening-ports collector runs
// `ss`, which autoloads inet_diag/tcp_diag/udp_diag. Recording them makes
// the tool report a "module loaded" change it caused itself. They must be
// suppressed; a real driver must not be.
func TestDiagModulesSuppressed(t *testing.T) {
	if !autoloadedDiagModules["inet_diag"] {
		t.Error("inet_diag (loaded by ss) must be suppressed")
	}
	if !autoloadedDiagModules["tcp_diag"] || !autoloadedDiagModules["udp_diag"] {
		t.Error("tcp_diag/udp_diag must be suppressed")
	}
	if autoloadedDiagModules["ext4"] || autoloadedDiagModules["nvme"] {
		t.Error("a real filesystem/driver module must NOT be suppressed")
	}
}
