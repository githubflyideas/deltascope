package state

import "testing"

// TestIptablesHashIgnoresTrafficCounters replays the exact symptom
// reported live: the hash changing on every check even though nobody
// touched the firewall. iptables-save embeds live packet/byte counters
// in chain policy lines ([pkts:bytes]) that increment continuously as
// traffic flows -- hashing them made every capture look like a change.
func TestIptablesHashIgnoresTrafficCounters(t *testing.T) {
	before := []string{
		"*filter",
		":INPUT ACCEPT [153:9876]",
		":FORWARD ACCEPT [0:0]",
		":OUTPUT ACCEPT [42:3210]",
		"-A INPUT -i lo -j ACCEPT",
		"-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT",
		"COMMIT",
	}
	// same ruleset, later capture -- only the traffic counters moved
	after := []string{
		"*filter",
		":INPUT ACCEPT [98765:6543210]",
		":FORWARD ACCEPT [12:800]",
		":OUTPUT ACCEPT [50231:998877]",
		"-A INPUT -i lo -j ACCEPT",
		"-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT",
		"COMMIT",
	}
	h1 := hashBytes([]byte(joinLines(stripIptablesCounters(before))))
	h2 := hashBytes([]byte(joinLines(stripIptablesCounters(after))))
	if h1 != h2 {
		t.Errorf("hash changed from counter drift alone with no rule change: %s vs %s", h1, h2)
	}

	// a genuine rule change must still be detected
	changed := []string{
		"*filter",
		":INPUT ACCEPT [153:9876]",
		":FORWARD ACCEPT [0:0]",
		":OUTPUT ACCEPT [42:3210]",
		"-A INPUT -i lo -j ACCEPT",
		"-A INPUT -p tcp --dport 22 -j DROP", // a real new rule
		"-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT",
		"COMMIT",
	}
	h3 := hashBytes([]byte(joinLines(stripIptablesCounters(changed))))
	if h1 == h3 {
		t.Error("a genuine new rule should change the hash")
	}

	// a real policy change (ACCEPT -> DROP) must also be detected, even
	// though it lives on the same line format as the stripped counters
	policyChanged := []string{
		"*filter",
		":INPUT DROP [153:9876]",
		":FORWARD ACCEPT [0:0]",
		":OUTPUT ACCEPT [42:3210]",
		"-A INPUT -i lo -j ACCEPT",
		"-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT",
		"COMMIT",
	}
	h4 := hashBytes([]byte(joinLines(stripIptablesCounters(policyChanged))))
	if h1 == h4 {
		t.Error("a default policy change (ACCEPT -> DROP) should change the hash")
	}
}

func TestNftCountersStripped(t *testing.T) {
	before := `table inet filter {
	chain input {
		counter packets 1000 bytes 50000 accept
	}
}`
	after := `table inet filter {
	chain input {
		counter packets 9999999 bytes 888888888 accept
	}
}`
	h1 := hashBytes([]byte(stripNftCounters(before)))
	h2 := hashBytes([]byte(stripNftCounters(after)))
	if h1 != h2 {
		t.Errorf("nft hash should be stable when only counters moved: %s vs %s", h1, h2)
	}
}

func joinLines(ls []string) string {
	out := ""
	for i, l := range ls {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
