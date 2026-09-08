package domain

import (
	"testing"
	"time"
)

func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

func TestAllowedTransition(t *testing.T) {
	allowed := map[[2]PollState]bool{
		{PollDraft, PollScheduled}: true,
		{PollDraft, PollOpen}:      false,
		{PollDraft, PollClosed}:    false,
		{PollDraft, PollDraft}:     false,

		{PollScheduled, PollOpen}:      true,
		{PollScheduled, PollClosed}:    true,
		{PollScheduled, PollDraft}:     false,
		{PollScheduled, PollScheduled}: false,

		{PollOpen, PollClosed}:    true,
		{PollOpen, PollDraft}:     false,
		{PollOpen, PollScheduled}: false,
		{PollOpen, PollOpen}:      false,

		{PollClosed, PollDraft}:     false,
		{PollClosed, PollScheduled}: false,
		{PollClosed, PollOpen}:      false,
		{PollClosed, PollClosed}:    false,
	}

	for pair, want := range allowed {
		from, to := pair[0], pair[1]
		if got := AllowedTransition(from, to); got != want {
			t.Errorf("AllowedTransition(%s, %s) = %v, want %v", from, to, got, want)
		}
	}
}

func TestTransitionRequest_Validate(t *testing.T) {
	now := parseTime(t, "2026-01-01T00:00:00Z")
	future := parseTime(t, "2026-01-01T01:00:00Z")
	past := parseTime(t, "2025-12-31T23:00:00Z")

	cases := []struct {
		name    string
		req     TransitionRequest
		wantErr bool
	}{
		{"scheduled with future scheduled_at", TransitionRequest{To: PollScheduled, ScheduledAt: &future}, false},
		{"scheduled without scheduled_at", TransitionRequest{To: PollScheduled}, true},
		{"scheduled with past scheduled_at", TransitionRequest{To: PollScheduled, ScheduledAt: &past}, true},
		{"open", TransitionRequest{To: PollOpen}, false},
		{"closed", TransitionRequest{To: PollClosed}, false},
		{"draft is not a valid target", TransitionRequest{To: PollDraft}, true},
		{"unknown state", TransitionRequest{To: "bogus"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate(now)
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestTransitionError_Message(t *testing.T) {
	err := &TransitionError{From: PollOpen, To: PollDraft}
	want := `cannot transition from "open" to "draft"`
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
