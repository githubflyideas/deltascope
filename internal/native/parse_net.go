package native

import (
	"strconv"
	"strings"
)

// Network parsing. The /proc/net files are the ones where PCP's metric
// names diverge most from the kernel's, so the mapping is explicit tables
// rather than derived from the labels: guessing a translation rule would
// silently drop counters whose names do not follow it.

// netdevField is the /proc/net/dev column layout after "iface:". Receive
// occupies the first eight, transmit the next eight.
const (
	ndRxBytes   = 0
	ndRxPackets = 1
	ndRxErrs    = 2
	ndRxDrop    = 3
	ndTxBytes   = 8
	ndTxPackets = 9
	ndTxErrs    = 10
	ndTxDrop    = 11
	ndColls     = 13
)

// parseNetDev reads /proc/net/dev, one instance per interface.
//
// The loopback interface is excluded, matching internal/pcp/units.go's
// excludedInstance: lo carries every local connection's traffic, so a
// machine talking to itself would dominate every bandwidth metric and
// bury the interface that actually has a problem.
func (s *Sample) parseNetDev(content string) {
	leaves := map[string]int{
		"in.bytes": ndRxBytes, "in.packets": ndRxPackets,
		"in.errors": ndRxErrs, "in.drops": ndRxDrop,
		"out.bytes": ndTxBytes, "out.packets": ndTxPackets,
		"out.errors": ndTxErrs, "out.drops": ndTxDrop,
		"collisions": ndColls,
	}
	for _, line := range strings.Split(content, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		iface := strings.TrimSpace(name)
		if iface == "" || iface == "lo" || strings.Contains(iface, "|") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		for leaf, idx := range leaves {
			s.setField("network.interface."+leaf, iface, f, idx)
		}
	}
}

// snmpKeys maps "<Table>:<Field>" from /proc/net/snmp and
// /proc/net/netstat onto catalog metrics. Kernel field names have
// accumulated inconsistent prefixes over the years (PruneCalled has none,
// TCPRcvCollapsed does), so both spellings are listed where the kernel has
// used both -- an absent key is simply never matched.
var snmpKeys = map[string]string{
	"Ip:InReceives":       "network.ip.inreceives",
	"Ip:InHdrErrors":      "network.ip.inhdrerrors",
	"Ip:InDiscards":       "network.ip.indiscards",
	"Ip:OutRequests":      "network.ip.outrequests",
	"Ip:OutDiscards":      "network.ip.outdiscards",
	"Ip:ForwDatagrams":    "network.ip.forwdatagrams",
	"Ip:ReasmFails":       "network.ip.reasmfails",
	"Ip:FragFails":        "network.ip.fragfails",
	"Icmp:InMsgs":         "network.icmp.inmsgs",
	"Icmp:OutMsgs":        "network.icmp.outmsgs",
	"Icmp:InErrors":       "network.icmp.inerrors",
	"Icmp:InDestUnreachs": "network.icmp.indestunreachs",
	"Tcp:ActiveOpens":     "network.tcp.activeopens",
	"Tcp:PassiveOpens":    "network.tcp.passiveopens",
	"Tcp:AttemptFails":    "network.tcp.attemptfails",
	"Tcp:EstabResets":     "network.tcp.estabresets",
	"Tcp:CurrEstab":       "network.tcp.currestab",
	"Tcp:InSegs":          "network.tcp.insegs",
	"Tcp:OutSegs":         "network.tcp.outsegs",
	"Tcp:RetransSegs":     "network.tcp.retranssegs",
	"Tcp:InErrs":          "network.tcp.inerrs",
	"Tcp:OutRsts":         "network.tcp.outrsts",
	"Udp:InDatagrams":     "network.udp.indatagrams",
	"Udp:OutDatagrams":    "network.udp.outdatagrams",
	"Udp:NoPorts":         "network.udp.noports",
	"Udp:InErrors":        "network.udp.inerrors",
	"Udp:RcvbufErrors":    "network.udp.recvbuferrors",
	"Udp:SndbufErrors":    "network.udp.sndbuferrors",

	"TcpExt:ListenOverflows":  "network.tcp.listenoverflows",
	"TcpExt:ListenDrops":      "network.tcp.listendrops",
	"TcpExt:SyncookiesSent":   "network.tcp.syncookiessent",
	"TcpExt:SyncookiesRecv":   "network.tcp.syncookiesrecv",
	"TcpExt:SyncookiesFailed": "network.tcp.syncookiesfailed",
	"TcpExt:PruneCalled":      "network.tcp.prunecalled",
	"TcpExt:TCPRcvCollapsed":  "network.tcp.rcvcollapsed",
	"TcpExt:RcvCollapsed":     "network.tcp.rcvcollapsed",
	"TcpExt:DelayedACKs":      "network.tcp.delayedacks",
	"TcpExt:TCPTimeouts":      "network.tcp.timeouts",
	"TcpExt:Timeouts":         "network.tcp.timeouts",
}

// parseSNMP reads the header/value line-pair format shared by
// /proc/net/snmp, /proc/net/snmp6 and /proc/net/netstat: a line of column
// names prefixed by the table, immediately followed by a line of values
// under the same prefix.
func (s *Sample) parseSNMP(content string) {
	var hdr []string
	var hdrTable string
	for _, line := range strings.Split(content, "\n") {
		table, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		if _, isNum := num(f[0]); !isNum {
			hdr, hdrTable = f, table
			continue
		}
		if hdrTable != table || hdr == nil {
			continue
		}
		for i, name := range hdr {
			metric, want := snmpKeys[table+":"+name]
			if !want {
				continue
			}
			s.setField(metric, "", f, i)
		}
	}
}

// parseSockstat reads /proc/net/sockstat's "TCP: inuse 20 orphan 0 tw 3
// alloc 30 mem 5" lines as key/value pairs.
func (s *Sample) parseSockstat(content string) {
	want := map[string]string{
		"TCP:inuse":  "network.sockstat.tcp.inuse",
		"TCP:orphan": "network.sockstat.tcp.orphan",
		"TCP:tw":     "network.sockstat.tcp.tw",
		"TCP:alloc":  "network.sockstat.tcp.alloc",
		"UDP:inuse":  "network.sockstat.udp.inuse",
	}
	for _, line := range strings.Split(content, "\n") {
		proto, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		for i := 0; i+1 < len(f); i += 2 {
			if metric, ok := want[proto+":"+f[i]]; ok {
				s.setField(metric, "", f, i+1)
			}
		}
	}
}

// parseSoftnet reads /proc/net/softnet_stat, one line per CPU, all columns
// in hex with no header. Columns 0/1/2 are packets processed, packets
// dropped, and times the softirq budget was exhausted. They are summed
// across CPUs because that is what the whole-machine metric means, and
// because a single-queue NIC pins all of this to one CPU -- reading only
// the first line would report zero on a machine that is dropping.
func (s *Sample) parseSoftnet(content string) {
	var processed, dropped, squeeze float64
	any := false
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		p, okP := hexNum(f[0])
		d, okD := hexNum(f[1])
		q, okQ := hexNum(f[2])
		if !okP || !okD || !okQ {
			continue
		}
		processed, dropped, squeeze = processed+p, dropped+d, squeeze+q
		any = true
	}
	if !any {
		return
	}
	s.set("network.softnet.processed", "", processed)
	s.set("network.softnet.dropped", "", dropped)
	s.set("network.softnet.time_squeeze", "", squeeze)
}

func hexNum(s string) (float64, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 16, 64)
	if err != nil {
		return 0, false
	}
	return float64(v), true
}

// maxSocketLines caps the /proc/net/tcp{,6} scan. This is the only family
// in the catalog with no counter file behind it -- the connection-state
// counts have to be tallied one socket at a time -- so on a busy proxy the
// cost is unbounded and the file is changing while it is read. Past the
// cap nothing is emitted: "unknown" leaves the state unevaluated, whereas
// a partial count reads as a real, low number and would actively assert
// that a machine drowning in CLOSE_WAIT is fine.
const maxSocketLines = 50000

// tcpStates maps the hex st field of /proc/net/tcp to catalog leaves.
var tcpStates = map[string]string{
	"01": "established",
	"03": "syn_recv",
	"06": "time_wait",
	"08": "close_wait",
	"0A": "listen",
}

// parseTCPConn tallies connection states across /proc/net/tcp and
// /proc/net/tcp6, which are two views of one socket table and must be
// summed. All five leaves are emitted when the scan completes, including
// the zeros: a machine with no CLOSE_WAIT sockets genuinely has none, and
// reporting that as unknown would leave a healthy answer unstated.
func (s *Sample) parseTCPConn(contents ...string) {
	counts := map[string]float64{}
	for _, leaf := range tcpStates {
		counts[leaf] = 0
	}
	scanned := 0
	for _, content := range contents {
		for _, line := range strings.Split(content, "\n") {
			f := strings.Fields(line)
			// Field 3 is st; field 0 is the "N:" slot index, which the header
			// line does not have, so the header is skipped by the parse below.
			if len(f) < 4 || !strings.HasSuffix(f[0], ":") {
				continue
			}
			scanned++
			if scanned > maxSocketLines {
				return
			}
			if leaf, ok := tcpStates[strings.ToUpper(f[3])]; ok {
				counts[leaf]++
			}
		}
	}
	if scanned == 0 {
		return
	}
	for leaf, v := range counts {
		s.set("network.tcpconn."+leaf, "", v)
	}
}
