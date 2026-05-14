package sysmetrics

type ProcessCPUSampler struct {
	lastProcessTicks uint64
	lastTotalTicks   uint64
	initialized      bool
}
