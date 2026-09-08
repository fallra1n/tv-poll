package domain

import (
	"strings"
	"testing"
	"time"
)

func validInput() CreatePollInput {
	return CreatePollInput{
		Question:            "Who will win?",
		Options:             []string{"A", "B"},
		VotingWindowSeconds: 300,
	}
}

func TestValidate_OK(t *testing.T) {
	if err := validInput().Validate(time.Now()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_QuestionLength(t *testing.T) {
	cases := map[string]string{
		"empty":    "",
		"too_long": strings.Repeat("q", 501),
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			in.Question = q
			if err := in.Validate(time.Now()); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidate_OptionCount(t *testing.T) {
	cases := map[string][]string{
		"zero": {},
		"one":  {"only"},
		"51":   make([]string, 51),
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			in.Options = opts
			if err := in.Validate(time.Now()); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidate_OptionCount_Boundaries(t *testing.T) {
	in := validInput()
	in.Options = []string{"a", "b"} // exactly 2, minimum
	if err := in.Validate(time.Now()); err != nil {
		t.Fatalf("2 options should be valid, got %v", err)
	}

	fifty := make([]string, 50)
	for i := range fifty {
		fifty[i] = "opt"
	}
	in.Options = fifty // exactly 50, maximum
	if err := in.Validate(time.Now()); err != nil {
		t.Fatalf("50 options should be valid, got %v", err)
	}
}

func TestValidate_OptionLabelLength(t *testing.T) {
	in := validInput()
	in.Options = []string{"", "B"}
	if err := in.Validate(time.Now()); err == nil {
		t.Fatal("expected validation error for empty label")
	}

	in.Options = []string{strings.Repeat("x", 201), "B"}
	if err := in.Validate(time.Now()); err == nil {
		t.Fatal("expected validation error for over-long label")
	}
}

func TestValidate_VotingWindowTooShort(t *testing.T) {
	in := validInput()
	in.VotingWindowSeconds = 29
	if err := in.Validate(time.Now()); err == nil {
		t.Fatal("expected validation error")
	}

	in.VotingWindowSeconds = 30 // boundary: exactly the minimum is valid
	if err := in.Validate(time.Now()); err != nil {
		t.Fatalf("30s window should be valid, got %v", err)
	}
}

func TestValidate_ScheduledAtInPast(t *testing.T) {
	in := validInput()
	past := time.Now().Add(-time.Minute)
	in.ScheduledAt = &past
	if err := in.Validate(time.Now()); err == nil {
		t.Fatal("expected validation error for past scheduled_at")
	}
}

func TestValidate_ScheduledAtInFuture(t *testing.T) {
	in := validInput()
	future := time.Now().Add(time.Minute)
	in.ScheduledAt = &future
	if err := in.Validate(time.Now()); err != nil {
		t.Fatalf("future scheduled_at should be valid, got %v", err)
	}
}

func TestValidate_CollectsMultipleIssues(t *testing.T) {
	in := CreatePollInput{
		Question:            "",
		Options:             []string{"only"},
		VotingWindowSeconds: 1,
	}
	err := in.Validate(time.Now())
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if len(ve.Issues) != 3 {
		t.Fatalf("expected 3 issues (question, options, window), got %d: %v", len(ve.Issues), ve.Issues)
	}
}
