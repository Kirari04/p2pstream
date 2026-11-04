package tunnel

import (
	"context"
	"errors"
	"github.com/hashicorp/yamux"
	"io"
	"net/http"
	"strconv"
	"time"
)

const LaneRetirementHeader = "X-P2PStream-Lane-Retirement"
const LaneOwnerHeader = "X-P2PStream-Lane-Owner"
const LaneReplacesHeader = "X-P2PStream-Lane-Replaces"
const LaneGrowthHeader = "X-P2PStream-Lane-Grown"
const LaneRetirementIntervalHeader = "X-P2PStream-Lane-Retirement-Millis"
const DefaultLaneRetirementInterval = 30 * time.Second

// Optional HTTP upgrade extension. Existing Yamux DATA/credit/GoAway semantics
// are unchanged. Without an exact acknowledgment, a peer never overlaps lanes.
func ParseLaneRetirement(h http.Header) (enabled bool, id, replaces string, err error) {
	versions, ids, previous := h.Values(LaneRetirementHeader), h.Values(LaneOwnerHeader), h.Values(LaneReplacesHeader)
	intervals, growth := h.Values(LaneRetirementIntervalHeader), h.Values(LaneGrowthHeader)
	if len(versions) == 0 && len(ids) == 0 && len(previous) == 0 && len(intervals) == 0 && len(growth) == 0 {
		return
	}
	bad := errors.New("invalid lane retirement contract")
	if len(versions) != 1 || versions[0] != "1" || len(ids) != 1 || !validLaneOwnerID(ids[0]) || len(previous) > 1 {
		return false, "", "", bad
	}
	if len(previous) == 1 {
		replaces = previous[0]
		if !validLaneOwnerID(replaces) || replaces == ids[0] {
			return false, "", "", bad
		}
	}
	if len(intervals) > 1 || len(growth) > 1 {
		return false, "", "", bad
	}
	if len(intervals) == 1 {
		millis, parseErr := strconv.ParseInt(intervals[0], 10, 64)
		if parseErr != nil || millis < 100 || millis > 60000 {
			return false, "", "", bad
		}
	}
	if len(growth) == 1 && (growth[0] != "1" || replaces == "") {
		return false, "", "", bad
	}
	return true, ids[0], replaces, nil
}
func validLaneOwnerID(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// PrepareLaneRetirement uses one bounded, server-initiated stream before any
// application stream is exposed. Its metadata fits the fixed session envelope;
// its DATA uses the already-reserved aggregate pool. The peer proves it has
// installed the exact negotiated owner before routing can switch.
func PrepareLaneRetirement(session *yamux.Session, id string) (*yamux.Stream, error) {
	stream, err := session.OpenStream()
	if err != nil {
		return nil, err
	}
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	fail := func(err error) (*yamux.Stream, error) { stream.Reset(); return nil, err }
	if _, err := io.WriteString(stream, "lane-ready-v1:"+id+"\n"); err != nil {
		return fail(err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(stream, ack[:]); err != nil {
		return fail(err)
	}
	if ack[0] != 1 {
		return fail(errors.New("invalid lane readiness acknowledgment"))
	}
	return stream, nil
}

// Commit follows the atomic server routing switch and, for replacement, the
// predecessor's joined opening fence and old-wire acknowledgment. Until this byte
// arrives the agent cannot GoAway/cancel its old lane.
func CommitLaneRetirement(stream *yamux.Stream) error {
	_, err := stream.Write([]byte{2})
	stream.Close()
	return err
}

func AcceptLaneRetirement(ctx context.Context, session *yamux.Session, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = stream.Reset()
		}
	}()
	deadline, _ := ctx.Deadline()
	stream.SetDeadline(deadline)
	want := "lane-ready-v1:" + id + "\n"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(stream, got); err != nil {
		return err
	}
	if string(got) != want {
		return errors.New("lane readiness owner mismatch")
	}
	if _, err := stream.Write([]byte{1}); err != nil {
		return err
	}
	var commit [1]byte
	if _, err := io.ReadFull(stream, commit[:]); err != nil {
		return err
	}
	if commit[0] != 2 {
		return errors.New("invalid lane readiness commit")
	}
	err = stream.Close()
	complete = err == nil
	return err
}
