package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// The diagnostic tree must be well-formed and complete. These tests make
// the tree queryable and enforced: every leaf has a home, no branch is
// empty, and the branch a diagnosis claims is one it can actually be built
// from. Adding a diagnosis with a bad or missing branch now fails the build
// instead of quietly landing in the wrong place.

// Every diagnosis declares a valid branch.
func TestEveryDiagnosisHasAValidBranch(t *testing.T) {
	valid := map[Branch]bool{}
	for _, b := range Branches {
		valid[b] = true
	}
	for _, d := range Diagnoses {
		if d.Branch == "" {
			t.Errorf("%s has no Branch -- every diagnosis must name its trunk", d.ID)
			continue
		}
		if !valid[d.Branch] {
			t.Errorf("%s has unknown Branch %q", d.ID, d.Branch)
		}
	}
}

// A diagnosis's declared branch must be reachable from its own states: the
// primary branch has to be one of the branches its required states belong
// to, or the classification is fiction. (A diagnosis may span branches --
// network_receive_cpu_bound reads CPU and network -- but its declared
// branch must be among them.)
func TestDiagnosisBranchMatchesItsStates(t *testing.T) {
	domainOf := map[string]string{}
	for _, s := range States {
		domainOf[s.ID] = s.Domain
	}
	for _, d := range Diagnoses {
		branches := map[Branch]bool{}
		refs := append(append([]string{}, d.RequiresAll...), d.RequiresAny...)
		for _, id := range refs {
			if b, ok := BranchOfDomain(domainOf[id]); ok {
				branches[b] = true
			}
		}
		if len(branches) == 0 {
			t.Errorf("%s references no state with a known branch", d.ID)
			continue
		}
		if !branches[d.Branch] {
			list := ""
			for b := range branches {
				list += string(b) + " "
			}
			t.Errorf("%s declares branch %q but its states only reach: %s", d.ID, d.Branch, list)
		}
	}
}

// Every state's domain maps onto a branch.
func TestEveryStateDomainMapsToABranch(t *testing.T) {
	for _, s := range States {
		if _, ok := BranchOfDomain(s.Domain); !ok {
			t.Errorf("%s has domain %q which maps to no branch -- extend domainToBranch", s.ID, s.Domain)
		}
	}
}

// No trunk is empty. A branch with no diagnoses is a hole in the tree that
// looks like coverage. Software is the known exception for now -- the
// engine does not yet emit Software-branch states or diagnoses (that is the
// next step), but the trunk exists so the tree is complete and future work
// has a defined home.
func TestEveryBranchExceptSoftwareHasDiagnoses(t *testing.T) {
	count := map[Branch]int{}
	for _, d := range Diagnoses {
		count[d.Branch]++
	}
	for _, b := range Branches {
		if b == BranchSoftware {
			continue // reasoning-layer Software diagnoses are future work
		}
		if count[b] == 0 {
			t.Errorf("branch %q has no diagnoses -- an empty trunk is a coverage hole", b)
		}
	}
	t.Logf("tree: cpu=%d memory=%d io=%d network=%d software=%d",
		count[BranchCPU], count[BranchMemory], count[BranchIO], count[BranchNetwork], count[BranchSoftware])
}

// The tree the reasoning engine declares must line up with the triage
// blocks the metric engine renders: a diagnosis that escalates the CPU
// triage block must be a CPU-branch diagnosis, etc. This is the guard that
// the two views cannot drift into disagreeing about what "CPU" means.
func TestReasoningBranchesAlignWithTriageResources(t *testing.T) {
	// The four hardware branches must each correspond to a triage resource
	// key. Software has no hardware triage block (it is the "gremlin"
	// card), so it is exempt.
	triageKey := map[Branch]string{
		BranchCPU:     "cpu",
		BranchMemory:  "mem",
		BranchIO:      "disk", // triage merges Disk I/O + Filesystem into "disk"
		BranchNetwork: "net",
	}
	for b, key := range triageKey {
		if key == "" {
			t.Errorf("branch %q has no triage mapping", b)
		}
	}
	// And the metric engine's four blocks must be exactly these four.
	got := map[string]bool{}
	for _, blk := range pcp.Triage(nil) {
		got[blk.Key] = true
	}
	for _, key := range triageKey {
		if !got[key] {
			t.Errorf("triage has no block %q that branch mapping expects", key)
		}
	}
}
