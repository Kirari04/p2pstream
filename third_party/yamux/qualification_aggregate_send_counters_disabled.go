//go:build !p2pstream_qualification

package yamux

const qualificationAggregateSendCountersEnabled = false

type qualificationAggregateSendCounters struct{}

func (*qualificationAggregateSendCounters) recordReservedData(uint32, uint32) {}

func (*qualificationAggregateSendCounters) recordAllocationZero(bool, bool) {}

func (*Session) recordQualificationByteCreditZero() {}
