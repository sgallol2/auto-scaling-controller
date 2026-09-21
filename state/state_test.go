package state

import (
	"path/filepath"
	"testing"
	"time"

	"autoscaler-controller/types"
)

func TestStoreSaveAndLoad(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "controller-state.json"))
	want := types.KnowledgeState{
		CurrentInstanceIDs: []string{"i-one", "i-two"},
		LastScaleAction:    time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		LastSignal:         types.INCREASE_CAPACITY,
		ConsecutiveCount:   2,
		RecentCPUReadings:  []float64{72.5, 75.0},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.LastScaleAction != want.LastScaleAction {
		t.Fatalf("LastScaleAction = %v, want %v", got.LastScaleAction, want.LastScaleAction)
	}
	if got.LastSignal != want.LastSignal || got.ConsecutiveCount != want.ConsecutiveCount {
		t.Fatalf("loaded decision state = %+v, want %+v", got, want)
	}
	if len(got.CurrentInstanceIDs) != len(want.CurrentInstanceIDs) || len(got.RecentCPUReadings) != len(want.RecentCPUReadings) {
		t.Fatalf("loaded slices = %+v, want %+v", got, want)
	}
}

func TestStoreLoadMissingFileReturnsEmptyState(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "missing.json"))

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.CurrentInstanceIDs != nil || got.ConsecutiveCount != 0 {
		t.Fatalf("state = %+v, want empty state", got)
	}
}
