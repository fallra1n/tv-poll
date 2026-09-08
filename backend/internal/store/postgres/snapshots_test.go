package postgres

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestMergeSnapshot_FirstSnapshotPassesThrough(t *testing.T) {
	prev := previousSnapshot{optionCounts: map[string]int64{}}
	next := SnapshotInput{
		PollID:              uuid.New(),
		TotalAccepted:       10,
		RejectedDuplicate:   2,
		RejectedRateLimited: 1,
		OptionCounts:        map[int]int64{1: 6, 2: 4},
	}

	got := mergeSnapshot(prev, next)

	if got.totalAccepted != 10 || got.rejectedDuplicate != 2 || got.rejectedRateLimited != 1 {
		t.Fatalf("unexpected merged totals: %+v", got)
	}
	want := map[string]int64{"1": 6, "2": 4}
	if !reflect.DeepEqual(got.optionCounts, want) {
		t.Errorf("optionCounts = %v, want %v", got.optionCounts, want)
	}
}

// TestMergeSnapshot_RegressionIsFloored is the direct proof of review
// finding B3: if the freshly-read Redis value is *lower* than the
// previous snapshot (a Redis restart/eviction lost data between two
// ticks), the merged value must not go backwards — a timeseries chart
// showing votes disappearing is a worse failure than briefly stale data.
func TestMergeSnapshot_RegressionIsFloored(t *testing.T) {
	prev := previousSnapshot{
		totalAccepted:       100,
		rejectedDuplicate:   5,
		rejectedRateLimited: 3,
		optionCounts:        map[string]int64{"1": 60, "2": 40},
	}
	next := SnapshotInput{
		PollID:              uuid.New(),
		TotalAccepted:       7, // Redis lost data — this looks like a regression
		RejectedDuplicate:   0,
		RejectedRateLimited: 0,
		OptionCounts:        map[int]int64{1: 5, 2: 2},
	}

	got := mergeSnapshot(prev, next)

	if got.totalAccepted != 100 {
		t.Errorf("totalAccepted = %d, want 100 (floored at the previous value)", got.totalAccepted)
	}
	if got.rejectedDuplicate != 5 {
		t.Errorf("rejectedDuplicate = %d, want 5", got.rejectedDuplicate)
	}
	if got.rejectedRateLimited != 3 {
		t.Errorf("rejectedRateLimited = %d, want 3", got.rejectedRateLimited)
	}
	want := map[string]int64{"1": 60, "2": 40}
	if !reflect.DeepEqual(got.optionCounts, want) {
		t.Errorf("optionCounts = %v, want %v (each option floored independently)", got.optionCounts, want)
	}
}

func TestMergeSnapshot_GrowthPassesThrough(t *testing.T) {
	prev := previousSnapshot{
		totalAccepted: 10,
		optionCounts:  map[string]int64{"1": 10},
	}
	next := SnapshotInput{
		TotalAccepted: 25,
		OptionCounts:  map[int]int64{1: 25},
	}

	got := mergeSnapshot(prev, next)

	if got.totalAccepted != 25 {
		t.Errorf("totalAccepted = %d, want 25 (normal growth, not floored)", got.totalAccepted)
	}
	if got.optionCounts["1"] != 25 {
		t.Errorf("optionCounts[1] = %d, want 25", got.optionCounts["1"])
	}
}

// TestMergeSnapshot_OptionMissingFromReadCarriesForward covers an
// option that existed in the previous snapshot but isn't present in
// this tick's Redis read (shouldn't happen in practice — Redis doesn't
// forget hash fields on its own — but the merge must not silently drop
// it if it somehow does).
func TestMergeSnapshot_OptionMissingFromReadCarriesForward(t *testing.T) {
	prev := previousSnapshot{
		optionCounts: map[string]int64{"1": 10, "2": 5},
	}
	next := SnapshotInput{
		OptionCounts: map[int]int64{1: 12}, // option 2 absent from this read
	}

	got := mergeSnapshot(prev, next)

	want := map[string]int64{"1": 12, "2": 5}
	if !reflect.DeepEqual(got.optionCounts, want) {
		t.Errorf("optionCounts = %v, want %v", got.optionCounts, want)
	}
}
