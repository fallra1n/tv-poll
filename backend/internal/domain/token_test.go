package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestVoteToken_RoundTrip(t *testing.T) {
	pollID := uuid.New()
	expiresAt := time.Now().Add(5 * time.Minute).Round(time.Second)

	wire, err := IssueVoteToken(pollID, expiresAt, "secret")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	got, err := VerifyVoteToken(wire, pollID, time.Now(), []string{"secret"})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.PollID != pollID {
		t.Errorf("PollID = %v, want %v", got.PollID, pollID)
	}
	if !got.ExpiresAt.Equal(expiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expiresAt)
	}
	if got.NonceString() == "" {
		t.Error("expected a non-empty nonce")
	}
}

func TestVoteToken_DistinctNonces(t *testing.T) {
	pollID := uuid.New()
	expiresAt := time.Now().Add(time.Minute)

	a, err := IssueVoteToken(pollID, expiresAt, "secret")
	if err != nil {
		t.Fatal(err)
	}
	b, err := IssueVoteToken(pollID, expiresAt, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two tokens for the same poll/expiry must not be identical (nonce must be random)")
	}
}

func TestVoteToken_Expired(t *testing.T) {
	pollID := uuid.New()
	expiresAt := time.Now().Add(-time.Minute) // already in the past

	wire, err := IssueVoteToken(pollID, expiresAt, "secret")
	if err != nil {
		t.Fatal(err)
	}

	_, err = VerifyVoteToken(wire, pollID, time.Now(), []string{"secret"})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestVoteToken_WrongPoll(t *testing.T) {
	issuedFor := uuid.New()
	checkedAgainst := uuid.New()
	expiresAt := time.Now().Add(time.Minute)

	wire, err := IssueVoteToken(issuedFor, expiresAt, "secret")
	if err != nil {
		t.Fatal(err)
	}

	_, err = VerifyVoteToken(wire, checkedAgainst, time.Now(), []string{"secret"})
	if !errors.Is(err, ErrTokenWrongPoll) {
		t.Fatalf("expected ErrTokenWrongPoll, got %v", err)
	}
}

func TestVoteToken_TamperedSignature(t *testing.T) {
	pollID := uuid.New()
	expiresAt := time.Now().Add(time.Minute)

	wire, err := IssueVoteToken(pollID, expiresAt, "secret")
	if err != nil {
		t.Fatal(err)
	}

	tampered := wire[:len(wire)-1] + flipLastChar(wire[len(wire)-1:])
	_, err = VerifyVoteToken(tampered, pollID, time.Now(), []string{"secret"})
	if !errors.Is(err, ErrTokenSignature) && !errors.Is(err, ErrTokenMalformed) {
		t.Fatalf("expected ErrTokenSignature or ErrTokenMalformed, got %v", err)
	}
}

func TestVoteToken_WrongSecret(t *testing.T) {
	pollID := uuid.New()
	expiresAt := time.Now().Add(time.Minute)

	wire, err := IssueVoteToken(pollID, expiresAt, "secret-a")
	if err != nil {
		t.Fatal(err)
	}

	_, err = VerifyVoteToken(wire, pollID, time.Now(), []string{"secret-b"})
	if !errors.Is(err, ErrTokenSignature) {
		t.Fatalf("expected ErrTokenSignature, got %v", err)
	}
}

func TestVoteToken_SecretRotation(t *testing.T) {
	pollID := uuid.New()
	expiresAt := time.Now().Add(time.Minute)

	// Issued under the *previous* secret, verified with {current, previous}.
	wire, err := IssueVoteToken(pollID, expiresAt, "old-secret")
	if err != nil {
		t.Fatal(err)
	}

	got, err := VerifyVoteToken(wire, pollID, time.Now(), []string{"new-secret", "old-secret"})
	if err != nil {
		t.Fatalf("expected rotation to accept a token signed under the previous secret, got %v", err)
	}
	if got.PollID != pollID {
		t.Errorf("PollID = %v, want %v", got.PollID, pollID)
	}
}

func TestVoteToken_MalformedWire(t *testing.T) {
	pollID := uuid.New()
	cases := map[string]string{
		"no_dot":            "notoken",
		"bad_base64_prefix": "!!!.aaaa",
		"empty":             "",
		"wrong_payload_len": "YWFhYQ.YWFhYQ", // decodes fine, just far too short
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyVoteToken(wire, pollID, time.Now(), []string{"secret"})
			if !errors.Is(err, ErrTokenMalformed) {
				t.Fatalf("expected ErrTokenMalformed, got %v", err)
			}
		})
	}
}

func flipLastChar(s string) string {
	if s == "A" {
		return "B"
	}
	return "A"
}
