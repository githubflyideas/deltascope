package reasoning

// The diagnostic tree.
//
// Every state and every diagnosis belongs to exactly one of five main
// branches -- the way an operator actually partitions a machine when
// something is wrong. This is the single source of truth for that
// partition; triage blocks, the reasoning-page grouping, and the coverage
// tests all derive from it rather than re-deciding the split ad hoc.
//
// The branches are deliberately the classic five, not four: Software is a
// first-class trunk, not a footnote. "Most incidents are caused by a
// change", and a change is a Software-branch event -- a config edit, a
// package, a service, a runaway process -- so it earns equal standing with
// the four hardware resources. (The reasoning engine does not yet emit
// Software-branch *states*; that is the next step. The branch exists now so
// the tree is complete and every future diagnosis has a defined home.)
type Branch string

const (
	BranchCPU      Branch = "cpu"
	BranchMemory   Branch = "memory"
	BranchIO       Branch = "io"
	BranchNetwork  Branch = "network"
	BranchSoftware Branch = "software"
)

// Branches is the ordered trunk list, for stable iteration and display.
var Branches = []Branch{BranchCPU, BranchMemory, BranchIO, BranchNetwork, BranchSoftware}

// branchName is the human label per branch.
var branchName = map[Branch]string{
	BranchCPU:      "CPU",
	BranchMemory:   "Memory",
	BranchIO:       "IO",
	BranchNetwork:  "Network",
	BranchSoftware: "Software",
}

func (b Branch) Name() string { return branchName[b] }

// domainToBranch maps a state's Domain string onto its trunk. Domain
// strings are kept as-is (changing them would be a behaviour change in the
// JSON and the UI grouping); the mapping folds the finer domains into the
// five trunks. Filesystem lives under IO -- a full disk and a saturated
// disk are the same trunk to the person triaging storage.
var domainToBranch = map[string]Branch{
	"cpu":        BranchCPU,
	"memory":     BranchMemory,
	"io":         BranchIO,
	"filesystem": BranchIO,
	"network":    BranchNetwork,
	"software":   BranchSoftware,
}

// BranchOfDomain resolves a Domain string to its trunk, ok=false if the
// domain is unknown (the catalog test rejects that).
func BranchOfDomain(domain string) (Branch, bool) {
	b, ok := domainToBranch[domain]
	return b, ok
}
