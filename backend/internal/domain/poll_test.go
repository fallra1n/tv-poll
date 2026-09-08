package domain

import "testing"

func TestPollState_Valid(t *testing.T) {
	valid := []PollState{PollDraft, PollScheduled, PollOpen, PollClosed}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}

	invalid := []PollState{"", "DRAFT", "deleted", "open "}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}
