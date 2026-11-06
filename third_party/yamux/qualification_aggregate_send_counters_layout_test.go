//go:build !p2pstream_qualification

package yamux

import (
	"testing"
	"unsafe"
)

func TestQualificationAggregateSendCountersDoNotChangeOrdinaryLayout(t *testing.T) {
	window := aggregateWindow{}
	t.Logf("ordinary aggregateWindow size=%d protocolMax=%d mu=%d sendWindow=%d", unsafe.Sizeof(window), unsafe.Offsetof(window.protocolMax), unsafe.Offsetof(window.mu), unsafe.Offsetof(window.sendWindow))
	if got := unsafe.Sizeof(qualificationAggregateSendCounters{}); got != 0 {
		t.Fatalf("disabled qualification counters size=%d, want zero", got)
	}
	if qualificationOffset, protocolOffset := unsafe.Offsetof(window.qualification), unsafe.Offsetof(window.protocolMax); qualificationOffset != protocolOffset {
		t.Fatalf("disabled counters offset=%d protocol offset=%d, want shared zero-size offset", qualificationOffset, protocolOffset)
	}
}
