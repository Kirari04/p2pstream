//go:build !p2pstream_qualification || !linux

package tunnel

import "time"

func qualificationEarlyShareAttemptStarted() {}

func qualificationEarlyShareMode() (bool, bool)                                             { return false, false }
func (c *GrowingConn) recordQualificationEarlyShare(time.Time, BackedEarlyShareObservation) {}

func StartQualificationEarlyShareFixture(string) (func(bool, time.Time) error, error) {
	return func(bool, time.Time) error { return nil }, nil
}
