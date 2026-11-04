package tunnel

import (
	"bytes"
	"context"
	"errors"
	"github.com/hashicorp/yamux"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestLaneRetirementContractStrictAndOptional(t *testing.T) {
	if enabled, _, _, err := ParseLaneRetirement(make(http.Header)); enabled || err != nil {
		t.Fatal("old peer rejected")
	}
	good := func() http.Header {
		h := make(http.Header)
		h.Set(LaneRetirementHeader, "1")
		h.Set(LaneOwnerHeader, "0123456789abcdef-new")
		h.Set(LaneReplacesHeader, "0123456789abcdef-old")
		return h
	}
	if enabled, id, previous, err := ParseLaneRetirement(good()); !enabled || id == previous || err != nil {
		t.Fatal("valid replacement rejected")
	}
	for name, mutate := range map[string]func(http.Header){
		"duplicate-version": func(h http.Header) { h.Add(LaneRetirementHeader, "1") },
		"unknown-version":   func(h http.Header) { h.Set(LaneRetirementHeader, "2") },
		"duplicate-owner":   func(h http.Header) { h.Add(LaneOwnerHeader, "0123456789abcdef-other") },
		"owner-too-short":   func(h http.Header) { h.Set(LaneOwnerHeader, "x") },
		"owner-injection":   func(h http.Header) { h.Set(LaneOwnerHeader, "0123456789abcdef\r\n") },
		"duplicate-old":     func(h http.Header) { h.Add(LaneReplacesHeader, "0123456789abcdef-old") },
		"same-owner":        func(h http.Header) { h.Set(LaneReplacesHeader, h.Get(LaneOwnerHeader)) },
		"missing-version":   func(h http.Header) { h.Del(LaneRetirementHeader) },
		"duplicate-interval": func(h http.Header) {
			h.Add(LaneRetirementIntervalHeader, "100")
			h.Add(LaneRetirementIntervalHeader, "200")
		},
		"short-interval":    func(h http.Header) { h.Set(LaneRetirementIntervalHeader, "99") },
		"overflow-interval": func(h http.Header) { h.Set(LaneRetirementIntervalHeader, "999999999999999999999") },
		"orphan-interval":   func(h http.Header) { clear(h); h.Set(LaneRetirementIntervalHeader, "100") },
		"duplicate-growth":  func(h http.Header) { h.Add(LaneGrowthHeader, "1"); h.Add(LaneGrowthHeader, "1") },
		"initial-growth":    func(h http.Header) { h.Del(LaneReplacesHeader); h.Set(LaneGrowthHeader, "1") },
	} {
		t.Run(name, func(t *testing.T) {
			h := good()
			mutate(h)
			if _, _, _, err := ParseLaneRetirement(h); err == nil {
				t.Fatal("malformed contract accepted")
			}
		})
	}
}

func TestLaneReadinessWaitsForPublicationCommit(t *testing.T) {
	a, b, _, _ := ownerTestPair(t)
	ready := make(chan *yamux.Stream, 1)
	failure := make(chan error, 1)
	go func() {
		stream, err := PrepareLaneRetirement(a, "0123456789abcdef-owner")
		if err != nil {
			failure <- err
			return
		}
		ready <- stream
	}()
	accepted := make(chan error, 1)
	go func() { accepted <- AcceptLaneRetirement(t.Context(), b, "0123456789abcdef-owner") }()
	var stream *yamux.Stream
	select {
	case stream = <-ready:
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("prepare did not acknowledge")
	}
	select {
	case err := <-accepted:
		t.Fatalf("agent considered uncommitted owner ready: %v", err)
	default:
	}
	if err := CommitLaneRetirement(stream); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("commit did not release agent")
	}
}

// Physical write completion is not consumption: FIN must leave the commit
// readable even when the accepting application is descheduled until after it.
func TestLaneReadinessCommitSurvivesDelayedConsumption(t *testing.T) {
	a, b, _, _ := ownerTestPair(t)
	sent := make(chan error, 1)
	go func() {
		stream, err := PrepareLaneRetirement(a, "0123456789abcdef-owner")
		if err == nil {
			err = CommitLaneRetirement(stream)
		}
		sent <- err
	}()
	stream, err := b.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	stream.SetDeadline(time.Now().Add(time.Second))
	want := []byte("lane-ready-v1:0123456789abcdef-owner\n")
	got := make([]byte, len(want))
	if _, err := io.ReadFull(stream, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("prepare %q %v", got, err)
	}
	if _, err := stream.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(stream)
	if err != nil || !bytes.Equal(got, []byte{2}) {
		t.Fatalf("buffered commit %v %v", got, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLaneReadinessLateCommitAfterDeadlineIsRejected(t *testing.T) {
	a, b, _, _ := ownerTestPair(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	accepted := make(chan error, 1)
	go func() { accepted <- AcceptLaneRetirement(ctx, b, "0123456789abcdef-owner") }()
	stream, err := PrepareLaneRetirement(a, "0123456789abcdef-owner")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("uncommitted timed-out owner became ready")
		}
	case <-time.After(time.Second):
		t.Fatal("readiness deadline did not join")
	}
	stream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrConnectionReset) {
		t.Fatalf("expired readiness not reset: %v", err)
	}
	if err := CommitLaneRetirement(stream); err == nil {
		t.Fatal("late commit succeeded after readiness disposal")
	}
	if a.IsClosed() || b.IsClosed() {
		t.Fatal("readiness disposal aborted unrelated session")
	}
}
