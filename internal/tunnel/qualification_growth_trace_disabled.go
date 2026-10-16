//go:build !p2pstream_qualification

package tunnel

import "time"

const qualificationGrowthTraceEnabled = false

type qualificationGrowthOutcome uint8

const (
	qualificationGrowthOutcomeNoProposal qualificationGrowthOutcome = iota
	qualificationGrowthOutcomeGrown
	qualificationGrowthOutcomeReservationDenied
	qualificationGrowthOutcomePublicationError
)

func (c *GrowingConn) recordQualificationGrowthSample(
	time.Time,
	time.Duration,
	uint32,
	uint32,
	qualificationGrowthOutcome,
) {
}
