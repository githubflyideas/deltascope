package native

import "testing"

const netDevFixture = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 5000      50    1    2    0     0          0         0     6000      60    3    4    0     5       0          0
`

func TestParseNetDev(t *testing.T) {
	s := newSample(zeroTime)
	s.parseNetDev(netDevFixture)

	mustVal(t, &s, "network.interface.in.bytes", "eth0", 5000)
	mustVal(t, &s, "network.interface.in.packets", "eth0", 50)
	mustVal(t, &s, "network.interface.in.errors", "eth0", 1)
	mustVal(t, &s, "network.interface.in.drops", "eth0", 2)
	mustVal(t, &s, "network.interface.out.bytes", "eth0", 6000)
	mustVal(t, &s, "network.interface.out.errors", "eth0", 3)
	mustVal(t, &s, "network.interface.out.drops", "eth0", 4)
	mustVal(t, &s, "network.interface.collisions", "eth0", 5)

	// lo is excluded, matching internal/pcp/units.go: local traffic would
	// dominate every bandwidth metric and hide the real interface.
	mustAbsent(t, &s, "network.interface.in.bytes", "lo")
	// The two header lines must not become an instance named "face".
	mustAbsent(t, &s, "network.interface.in.bytes", "face")
	mustAbsent(t, &s, "network.interface.in.bytes", "Inter-|   Receive")
}

const snmpFixture = `Ip: Forwarding DefaultTTL InReceives InHdrErrors InAddrErrors ForwDatagrams InUnknownProtos InDiscards InDelivers OutRequests OutDiscards OutNoRoutes ReasmTimeout ReasmReqds ReasmOKs ReasmFails FragOKs FragFails FragCreates
Ip: 1 64 1000 2 0 3 0 4 900 800 5 0 0 0 0 6 0 7 0
Icmp: InMsgs InErrors InCsumErrors InDestUnreachs OutMsgs
Icmp: 40 1 0 12 44
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 10 20 3 4 55 5000 4000 12 1 9 0
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors
Udp: 700 8 2 650 5 6 0
`

func TestParseSNMP(t *testing.T) {
	s := newSample(zeroTime)
	s.parseSNMP(snmpFixture)

	mustVal(t, &s, "network.ip.inreceives", "", 1000)
	mustVal(t, &s, "network.ip.inhdrerrors", "", 2)
	mustVal(t, &s, "network.ip.forwdatagrams", "", 3)
	mustVal(t, &s, "network.ip.indiscards", "", 4)
	mustVal(t, &s, "network.ip.outrequests", "", 800)
	mustVal(t, &s, "network.ip.outdiscards", "", 5)
	mustVal(t, &s, "network.ip.reasmfails", "", 6)
	mustVal(t, &s, "network.ip.fragfails", "", 7)

	mustVal(t, &s, "network.icmp.inmsgs", "", 40)
	mustVal(t, &s, "network.icmp.inerrors", "", 1)
	mustVal(t, &s, "network.icmp.indestunreachs", "", 12)
	mustVal(t, &s, "network.icmp.outmsgs", "", 44)

	// Column position is read from the header, not assumed: MaxConn is -1
	// here and ActiveOpens sits after it.
	mustVal(t, &s, "network.tcp.activeopens", "", 10)
	mustVal(t, &s, "network.tcp.passiveopens", "", 20)
	mustVal(t, &s, "network.tcp.attemptfails", "", 3)
	mustVal(t, &s, "network.tcp.currestab", "", 55)
	mustVal(t, &s, "network.tcp.retranssegs", "", 12)
	mustVal(t, &s, "network.tcp.outrsts", "", 9)

	mustVal(t, &s, "network.udp.indatagrams", "", 700)
	mustVal(t, &s, "network.udp.noports", "", 8)
	mustVal(t, &s, "network.udp.recvbuferrors", "", 5)
	mustVal(t, &s, "network.udp.sndbuferrors", "", 6)
}

const netstatFixture = `TcpExt: SyncookiesSent SyncookiesRecv SyncookiesFailed EmbryonicRsts PruneCalled ListenOverflows ListenDrops TCPRcvCollapsed DelayedACKs TCPTimeouts
TcpExt: 1 2 3 99 6 4 5 7 8 9
IpExt: InNoRoutes InTruncatedPkts
IpExt: 0 0
`

func TestParseNetstat(t *testing.T) {
	s := newSample(zeroTime)
	s.parseSNMP(netstatFixture)

	mustVal(t, &s, "network.tcp.syncookiessent", "", 1)
	mustVal(t, &s, "network.tcp.syncookiesrecv", "", 2)
	mustVal(t, &s, "network.tcp.syncookiesfailed", "", 3)
	mustVal(t, &s, "network.tcp.prunecalled", "", 6)
	mustVal(t, &s, "network.tcp.listenoverflows", "", 4)
	mustVal(t, &s, "network.tcp.listendrops", "", 5)
	mustVal(t, &s, "network.tcp.rcvcollapsed", "", 7)
	mustVal(t, &s, "network.tcp.delayedacks", "", 8)
	mustVal(t, &s, "network.tcp.timeouts", "", 9)
}

const sockstatFixture = `sockets: used 300
TCP: inuse 20 orphan 1 tw 30 alloc 40 mem 5
UDP: inuse 4 mem 2
UDPLITE: inuse 0
RAW: inuse 0
FRAG: inuse 0 memory 0
`

func TestParseSockstat(t *testing.T) {
	s := newSample(zeroTime)
	s.parseSockstat(sockstatFixture)

	mustVal(t, &s, "network.sockstat.tcp.inuse", "", 20)
	mustVal(t, &s, "network.sockstat.tcp.orphan", "", 1)
	mustVal(t, &s, "network.sockstat.tcp.tw", "", 30)
	mustVal(t, &s, "network.sockstat.tcp.alloc", "", 40)
	mustVal(t, &s, "network.sockstat.udp.inuse", "", 4)
}

// Columns are hex and unlabelled. Summing across CPUs matters: a
// single-queue NIC pins all softirq work to one CPU, so reading only the
// first line reports zero drops on a machine that is dropping.
const softnetFixture = `0000000a 00000002 00000003 00000000 00000000 00000000 00000000 00000000 00000000
00000014 00000004 00000005 00000000 00000000 00000000 00000000 00000000 00000000
`

func TestParseSoftnet(t *testing.T) {
	s := newSample(zeroTime)
	s.parseSoftnet(softnetFixture)

	mustVal(t, &s, "network.softnet.processed", "", 30)
	mustVal(t, &s, "network.softnet.dropped", "", 6)
	mustVal(t, &s, "network.softnet.time_squeeze", "", 8)
}
