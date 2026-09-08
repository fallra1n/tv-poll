package domain

import (
	"strconv"
	"strings"
	"time"
)

// CreatePollInput is the validated shape of a poll-creation request —
// built by the HTTP layer from the wire request, then handed to the
// store. ScheduledAt == nil means the poll is created in draft.
type CreatePollInput struct {
	Question            string
	Options             []string
	ScheduledAt         *time.Time
	VotingWindowSeconds int
}

// ValidationError collects every problem found, not just the first —
// an admin filling out a poll form benefits from seeing all of them at
// once rather than fixing one field per request round-trip.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	return strings.Join(e.Issues, "; ")
}

const (
	minOptions = 2
	maxOptions = 50

	minQuestionLen = 1
	maxQuestionLen = 500

	minOptionLabelLen = 1
	maxOptionLabelLen = 200

	minVotingWindowSeconds = 30
)

// Validate checks a create-poll request against the same limits enforced
// by the polls/poll_options CHECK constraints (docs/ai/04-data-model.md
// §1), plus one rule the schema can't express: a scheduled_at in the past
// would make the poll born already past its computed closes_at.
func (in CreatePollInput) Validate(now time.Time) error {
	var issues []string

	if l := len(in.Question); l < minQuestionLen || l > maxQuestionLen {
		issues = append(issues, "question must be between 1 and 500 characters")
	}

	if l := len(in.Options); l < minOptions || l > maxOptions {
		issues = append(issues, "options must contain between 2 and 50 items")
	}
	for i, label := range in.Options {
		if l := len(label); l < minOptionLabelLen || l > maxOptionLabelLen {
			issues = append(issues, invalidOptionLabelMsg(i))
		}
	}

	if in.VotingWindowSeconds < minVotingWindowSeconds {
		issues = append(issues, "voting_window_seconds must be at least 30")
	}

	if in.ScheduledAt != nil && !in.ScheduledAt.After(now) {
		issues = append(issues, "scheduled_at must be in the future")
	}

	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: issues}
}

func invalidOptionLabelMsg(index int) string {
	return "option[" + strconv.Itoa(index) + "].label must be between 1 and 200 characters"
}
