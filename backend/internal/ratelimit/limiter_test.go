package ratelimit

import "testing"

func TestLimiter_AllowsUpToBurst(t *testing.T) {
	l := New(1, 5, 100)
	for i := 0; i < 5; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed within burst", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("request beyond burst should be denied")
	}
}

func TestLimiter_KeysAreIndependent(t *testing.T) {
	l := New(1, 1, 100)
	if !l.Allow("a") {
		t.Fatal("first request for key a should be allowed")
	}
	if l.Allow("a") {
		t.Fatal("second immediate request for key a should be denied")
	}
	if !l.Allow("b") {
		t.Fatal("key b has its own bucket and should be allowed")
	}
}

func TestLimiter_EvictionFailsOpen(t *testing.T) {
	// maxKeys=1: adding a second key evicts the first. Losing state on
	// eviction must reset that key to a fresh bucket, not deny it
	// outright — see package doc: "fails open toward real users".
	l := New(1, 1, 1)
	if !l.Allow("a") {
		t.Fatal("first request for key a should be allowed")
	}
	l.Allow("b") // evicts "a" from the LRU
	if !l.Allow("a") {
		t.Fatal("key a should get a fresh bucket after eviction, not stay denied")
	}
}
