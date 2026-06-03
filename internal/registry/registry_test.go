package registry

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreUpsertPersistsAndReplacesRun(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runs.json"))
	ref := RunRef{
		Worker:           "jack@jack-rasp-five",
		Project:          "fixture",
		RunID:            "run-1",
		RemoteRunPath:    "/home/jack/.workyard/runs/fixture/run-1",
		RemoteSourcePath: "/home/jack/.workyard/runs/fixture/run-1/source",
	}
	if err := store.Upsert(ref); err != nil {
		t.Fatal(err)
	}
	first, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("expected one run, got %d", len(first))
	}
	if first[0].RegisteredAt.IsZero() || first[0].UpdatedAt.IsZero() {
		t.Fatalf("timestamps were not populated: %#v", first[0])
	}

	time.Sleep(time.Millisecond)
	ref.RemoteBinary = "/home/jack/.workyard/bin/workyard"
	if err := store.Upsert(ref); err != nil {
		t.Fatal(err)
	}
	next, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 {
		t.Fatalf("expected replacement, got %d entries", len(next))
	}
	if next[0].RemoteBinary != ref.RemoteBinary {
		t.Fatalf("remote binary was not updated: %#v", next[0])
	}
	if !next[0].RegisteredAt.Equal(first[0].RegisteredAt) {
		t.Fatalf("registeredAt changed across upsert")
	}
	if !next[0].UpdatedAt.After(first[0].UpdatedAt) {
		t.Fatalf("updatedAt did not advance")
	}
}

func TestStoreRejectsInvalidRun(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runs.json"))
	if err := store.Upsert(RunRef{Worker: "jack@pi", Project: "fixture\nx", RunID: "run-1"}); err == nil {
		t.Fatal("expected invalid project to be rejected")
	}
}

func TestStoreRemoveWorkerAndPrune(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runs.json"))
	old := RunRef{Worker: "jack@pi", Project: "fixture", RunID: "old"}
	fresh := RunRef{Worker: "jack@pi", Project: "fixture", RunID: "fresh"}
	other := RunRef{Worker: "other", Project: "fixture", RunID: "fresh"}
	for _, ref := range []RunRef{old, fresh, other} {
		if err := store.Upsert(ref); err != nil {
			t.Fatal(err)
		}
	}
	file, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	for i := range file.Runs {
		if file.Runs[i].RunID == "old" {
			file.Runs[i].UpdatedAt = time.Now().Add(-48 * time.Hour)
		} else {
			file.Runs[i].UpdatedAt = time.Now()
		}
	}
	if err := store.save(file); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Prune(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].RunID != "old" {
		t.Fatalf("unexpected pruned refs: %#v", removed)
	}
	count, err := store.RemoveWorker("jack@pi")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("removed %d, want 1", count)
	}
	runs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Worker != "other" {
		t.Fatalf("unexpected remaining runs: %#v", runs)
	}
}

func TestStoreWorkersSummarizesRuns(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runs.json"))
	for _, ref := range []RunRef{
		{Worker: "a", Project: "fixture", RunID: "one"},
		{Worker: "a", Project: "fixture", RunID: "two"},
		{Worker: "b", Project: "fixture", RunID: "one"},
	} {
		if err := store.Upsert(ref); err != nil {
			t.Fatal(err)
		}
	}
	workers, err := store.Workers()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 || workers[0].Worker != "a" || workers[0].RunCount != 2 || workers[1].Worker != "b" {
		t.Fatalf("unexpected workers: %#v", workers)
	}
}
