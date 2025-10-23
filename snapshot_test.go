package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestSnapshot(t *testing.T) {
	t.Parallel()
	t.Run("missing or empty returns empty", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		got, err := latestSnapshot(missing)
		if err != nil {
			t.Fatalf("latestSnapshot missing: %v", err)
		}
		if got != "" {
			t.Fatalf("expected empty string for missing dir, got %q", got)
		}

		empty := t.TempDir()
		got, err = latestSnapshot(empty)
		if err != nil {
			t.Fatalf("latestSnapshot empty: %v", err)
		}
		if got != "" {
			t.Fatalf("expected empty string for empty dir, got %q", got)
		}
	})

	t.Run("returns newest directory", func(t *testing.T) {
		snapDir := t.TempDir()
		entries := []string{
			"btrfs-backup-2024-05-10_10-10-10",
			"btrfs-backup-2024-05-11_10-10-10",
			"btrfs-backup-2024-05-09_10-10-10",
		}

		for _, name := range entries {
			if err := os.Mkdir(filepath.Join(snapDir, name), 0o755); err != nil {
				t.Fatalf("creating snapshot dir: %v", err)
			}
		}

		if err := os.WriteFile(filepath.Join(snapDir, "not-a-dir"), []byte("ignore me"), 0o644); err != nil {
			t.Fatalf("writing file: %v", err)
		}

		got, err := latestSnapshot(snapDir)
		if err != nil {
			t.Fatalf("latestSnapshot: %v", err)
		}

		want := filepath.Join(snapDir, "btrfs-backup-2024-05-11_10-10-10")
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})
}

func TestCreateSnapshot(t *testing.T) {
	setupTestEnv(t)

	logPath := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", logPath)

	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("creating src dir: %v", err)
	}

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	if err := os.Mkdir(snapDir, 0o755); err != nil {
		t.Fatalf("creating snapshot dir: %v", err)
	}

	now := time.Date(2024, 5, 12, 11, 30, 45, 0, time.UTC)
	got, err := createSnapshot(context.Background(), srcDir, snapDir, now)
	if err != nil {
		t.Fatalf("createSnapshot: %v", err)
	}

	want := filepath.Join(snapDir, "btrfs-backup-2024-05-12_11-30-45")
	if got != want {
		t.Fatalf("expected snapshot path %q, got %q", want, got)
	}

	if _, err := os.Stat(got); err != nil {
		t.Fatalf("expected snapshot directory to exist: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading btrfs log: %v", err)
	}

	expectedLog := fmt.Sprintf("snapshot -r %s %s\n", srcDir, want)
	if !strings.Contains(string(logData), expectedLog) {
		t.Fatalf("expected log %q, got %q", expectedLog, string(logData))
	}
}

func TestCreateSnapshotError(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("BTRFS_FAIL_SNAPSHOT", "1")

	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("creating src dir: %v", err)
	}

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	if err := os.Mkdir(snapDir, 0o755); err != nil {
		t.Fatalf("creating snapshot dir: %v", err)
	}

	_, err := createSnapshot(context.Background(), srcDir, snapDir, time.Now())
	if err == nil {
		t.Fatal("expected createSnapshot to fail")
	}
}

func TestDeleteOldSnapshot(t *testing.T) {
	setupTestEnv(t)

	logPath := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", logPath)

	toDelete := filepath.Join(t.TempDir(), "old-snapshot")
	if err := os.Mkdir(toDelete, 0o755); err != nil {
		t.Fatalf("creating snapshot dir: %v", err)
	}

	deleteOldSnapshot(context.Background(), toDelete)

	if _, err := os.Stat(toDelete); !os.IsNotExist(err) {
		t.Fatalf("expected snapshot to be deleted, stat err: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading btrfs log: %v", err)
	}
	if !strings.Contains(string(logData), fmt.Sprintf("delete %s\n", toDelete)) {
		t.Fatalf("expected delete log entry, got %q", string(logData))
	}
}
