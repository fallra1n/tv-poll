package domain

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	tokenNonceSize   = 16
	tokenTagSize     = 16
	tokenPayloadSize = 16 /* poll_id */ + 8 /* expires_at */ + tokenNonceSize
)

var (
	ErrTokenMalformed = errors.New("malformed vote token")
	ErrTokenSignature = errors.New("invalid vote token signature")
	ErrTokenExpired   = errors.New("vote token expired")
	ErrTokenWrongPoll = errors.New("vote token issued for a different poll")
)

// VoteToken is the decoded, verified content of a vote token — see
// docs/ai/05-api-contract.md ("Закрытый вопрос: формат токена") and
// docs/ai/03-deduplication.md §2.3.
//
// Wire format: base64url(poll_id[16] | expires_at unix seconds[8] |
// nonce[16]) + "." + base64url(HMAC-SHA256(secret, payload)[:16]).
// expires_at is inside the signed payload — not a separate unsigned
// field and not a fixed offset from issuance — so it can be checked
// locally at vote time with no Redis read, and so it always equals
// poll.closes_at at the moment of issuance (a fixed offset would either
// outlive the window or expire before it, depending on when in the
// window the token was issued).
type VoteToken struct {
	PollID    uuid.UUID
	ExpiresAt time.Time
	Nonce     [tokenNonceSize]byte
}

// NonceString is the base64url encoding of Nonce — this, not the full
// token, is the Redis dedup key component (docs/ai/03-deduplication.md
// §2.1: shorter, and the signature/poll_id add nothing to uniqueness
// since the nonce alone is already 128 bits of randomness).
func (t VoteToken) NonceString() string {
	return base64.RawURLEncoding.EncodeToString(t.Nonce[:])
}

// IssueVoteToken creates a fresh, signed token for pollID, expiring at
// expiresAt (== poll.closes_at at issuance time — callers must not pass
// a fixed offset from now). secret is the current signing secret
// (VOTE_TOKEN_SECRET); verification separately accepts a previous
// secret too, so rotating it doesn't invalidate tokens issued
// mid-broadcast.
func IssueVoteToken(pollID uuid.UUID, expiresAt time.Time, secret string) (string, error) {
	var nonce [tokenNonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	payload := encodeTokenPayload(pollID, expiresAt, nonce)
	tag := signTokenPayload(payload, secret)

	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(tag), nil
}

// VerifyVoteToken parses and verifies a wire token. secrets is tried in
// order (typically {current, previous}, previous possibly empty); the
// first match wins. The signature is checked before any field of the
// decoded payload is trusted for a decision (expiry, poll_id) — an
// unauthenticated payload is attacker-controlled.
func VerifyVoteToken(wire string, pollID uuid.UUID, now time.Time, secrets []string) (VoteToken, error) {
	payloadPart, tagPart, ok := strings.Cut(wire, ".")
	if !ok {
		return VoteToken{}, ErrTokenMalformed
	}

	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil || len(payload) != tokenPayloadSize {
		return VoteToken{}, ErrTokenMalformed
	}
	tag, err := base64.RawURLEncoding.DecodeString(tagPart)
	if err != nil || len(tag) != tokenTagSize {
		return VoteToken{}, ErrTokenMalformed
	}

	if !verifyAnySecret(payload, tag, secrets) {
		return VoteToken{}, ErrTokenSignature
	}

	t := VoteToken{
		PollID:    uuid.UUID(payload[0:16]),
		ExpiresAt: time.Unix(int64(binary.BigEndian.Uint64(payload[16:24])), 0).UTC(),
		Nonce:     [tokenNonceSize]byte(payload[24:tokenPayloadSize]),
	}

	if t.PollID != pollID {
		return VoteToken{}, ErrTokenWrongPoll
	}
	if !t.ExpiresAt.After(now) {
		return VoteToken{}, ErrTokenExpired
	}
	return t, nil
}

func encodeTokenPayload(pollID uuid.UUID, expiresAt time.Time, nonce [tokenNonceSize]byte) []byte {
	buf := make([]byte, tokenPayloadSize)
	copy(buf[0:16], pollID[:])
	binary.BigEndian.PutUint64(buf[16:24], uint64(expiresAt.Unix()))
	copy(buf[24:tokenPayloadSize], nonce[:])
	return buf
}

func signTokenPayload(payload []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return mac.Sum(nil)[:tokenTagSize]
}

func verifyAnySecret(payload, tag []byte, secrets []string) bool {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if hmac.Equal(signTokenPayload(payload, secret), tag) {
			return true
		}
	}
	return false
}
