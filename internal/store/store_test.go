package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/subpathdev/heating-control/internal/controller"
)

func TestLoadWithoutAFile(t *testing.T) {
	records, err := New(filepath.Join(t.TempDir(), "state.json")).Load()
	if err != nil || records != nil {
		t.Fatalf("got %v, %v", records, err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	target := 20.5
	until := time.Unix(1000, 0).UTC()
	f := New(path)
	if err := f.Save(map[string]controller.RoomRecord{
		"office": {Target: &target, TargetDirty: true, AiringUntil: &until, RestoreMode: controller.ModeAuto, Restoring: true},
	}); err != nil {
		t.Fatal(err)
	}

	records, err := New(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	got := records["office"]
	if *got.Target != 20.5 || !got.TargetDirty || !got.AiringUntil.Equal(until) || got.RestoreMode != controller.ModeAuto || !got.Restoring {
		t.Fatalf("got %+v", got)
	}

	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("left %d files behind", len(files))
	}
}

func TestCheckReportsAFailedSave(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "missing", "state.json"))
	if err := f.Save(nil); err == nil {
		t.Fatal("want an error")
	}
	if err := f.Check(context.Background()); err == nil {
		t.Fatal("the health check doesn't report it")
	}
}

func TestLoadCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := New(path)
	if _, err := f.Load(); err == nil {
		t.Fatal("want an error")
	}
	if err := f.Check(context.Background()); err == nil {
		t.Fatal("the health check doesn't report it")
	}
	got, err := os.ReadFile(path + ".corrupt")
	if err != nil || string(got) != "{nope" {
		t.Fatalf("moved file = %q, %v", got, err)
	}

	// A later save neither touches it nor clears the report.
	if err := f.Save(map[string]controller.RoomRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatal(err)
	}
	if err := f.Check(context.Background()); err == nil {
		t.Fatal("the report was cleared")
	}
}
