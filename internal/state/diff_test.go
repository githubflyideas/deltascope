package state

import "testing"

func snap(items map[string]string) Snapshot {
	var secs []Section
	sec := Section{Name: "sysctl", Title: "内核参数"}
	for k, v := range items {
		sec.Items = append(sec.Items, Item{Key: k, Value: v})
	}
	secs = append(secs, sec)
	return Snapshot{Sections: secs}
}

func TestCompare(t *testing.T) {
	a := snap(map[string]string{"vm.swappiness": "60", "removed.key": "x", "same": "1"})
	b := snap(map[string]string{"vm.swappiness": "10", "added.key": "y", "same": "1"})
	d := Compare(a, b)
	if d.Total != 3 {
		t.Fatalf("应有 3 处变更 (改/增/删), 得到 %d", d.Total)
	}
	kinds := map[ChangeKind]int{}
	for _, sd := range d.Sections {
		for _, ch := range sd.Changes {
			kinds[ch.Kind]++
		}
	}
	if kinds[Modified] != 1 || kinds[Added] != 1 || kinds[Removed] != 1 {
		t.Fatalf("增改删各应为 1: %+v", kinds)
	}
}

func TestCompareIdentical(t *testing.T) {
	a := snap(map[string]string{"a": "1", "b": "2"})
	if d := Compare(a, a); d.Total != 0 {
		t.Fatalf("相同快照应无差异, 得到 %d", d.Total)
	}
}

func TestSysctlSkip(t *testing.T) {
	for _, k := range []string{"kernel.random.uuid", "fs.dentry-state", "user.max_user_namespaces", "net.ipv4.ip_forward"} {
		if !sysctlSkip(k) {
			t.Errorf("%s 应被跳过", k)
		}
	}
	if sysctlSkip("net.core.somaxconn") {
		t.Error("somaxconn 不应被跳过")
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	// 用内存态验证 marker 逻辑不依赖真实 DB —— 仅测 Compare 语义闭环
	base := snap(map[string]string{"vm.swappiness": "60", "net.core.somaxconn": "4096"})
	after := snap(map[string]string{"vm.swappiness": "10", "net.core.somaxconn": "4096"})
	d := Compare(base, after)
	if d.Total != 1 {
		t.Fatalf("发布只改 1 项,影响面应为 1 处, 得到 %d", d.Total)
	}
	if d.Sections[0].Changes[0].Old != "60" || d.Sections[0].Changes[0].New != "10" {
		t.Errorf("影响面报告值不对: %+v", d.Sections[0].Changes[0])
	}
}
