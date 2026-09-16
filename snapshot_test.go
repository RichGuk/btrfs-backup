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

func TestEnsureSnapDirAcceptsExistingDirectory(t *testing.T) {
	withoutTerminal(t)

	vol := &Volume{Name: "root", SnapDir: t.TempDir()}

	if err := ensureSnapDir(context.Background(), vol); err != nil {
		t.Fatalf("ensureSnapDir: %v", err)
	}
}

func TestEnsureSnapDirCreatesSubvolume(t *testing.T) {
	setupTestEnv(t)
	withPromptAnswers(t, "s")

	logPath := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", logPath)

	snapDir := filepath.Join(t.TempDir(), "nested", "snapshots")
	vol := &Volume{Name: "root", SnapDir: snapDir}

	var err error
	captureStdout(t, func() { err = ensureSnapDir(context.Background(), vol) })
	if err != nil {
		t.Fatalf("ensureSnapDir: %v", err)
	}

	if _, statErr := os.Stat(snapDir); statErr != nil {
		t.Fatalf("expected snapshot dir to exist: %v", statErr)
	}

	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("reading btrfs log: %v", readErr)
	}
	if !strings.Contains(string(logData), fmt.Sprintf("create %s\n", snapDir)) {
		t.Fatalf("expected a subvolume create log entry, got %q", string(logData))
	}
}

// Enter picks the default, which is a subvolume.
func TestEnsureSnapDirDefaultsToSubvolume(t *testing.T) {
	setupTestEnv(t)
	withPromptAnswers(t, "")

	logPath := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", logPath)

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	vol := &Volume{Name: "root", SnapDir: snapDir}

	var err error
	captureStdout(t, func() { err = ensureSnapDir(context.Background(), vol) })
	if err != nil {
		t.Fatalf("ensureSnapDir: %v", err)
	}

	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("reading btrfs log: %v", readErr)
	}
	if !strings.Contains(string(logData), fmt.Sprintf("create %s\n", snapDir)) {
		t.Fatalf("expected a subvolume create log entry, got %q", string(logData))
	}
}

func TestEnsureSnapDirCreatesPlainDirectory(t *testing.T) {
	setupTestEnv(t)
	withPromptAnswers(t, "d")

	logPath := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", logPath)

	snapDir := filepath.Join(t.TempDir(), "nested", "snapshots")
	vol := &Volume{Name: "root", SnapDir: snapDir}

	var err error
	captureStdout(t, func() { err = ensureSnapDir(context.Background(), vol) })
	if err != nil {
		t.Fatalf("ensureSnapDir: %v", err)
	}

	info, statErr := os.Stat(snapDir)
	if statErr != nil {
		t.Fatalf("expected snapshot dir to exist: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatal("expected snapshot dir to be a directory")
	}

	if _, readErr := os.ReadFile(logPath); !os.IsNotExist(readErr) {
		t.Fatalf("expected btrfs not to be called, log read returned %v", readErr)
	}
}

func TestEnsureSnapDirDeclined(t *testing.T) {
	setupTestEnv(t)
	withPromptAnswers(t, "n")

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	vol := &Volume{Name: "root", SnapDir: snapDir}

	var err error
	captureStdout(t, func() { err = ensureSnapDir(context.Background(), vol) })
	if err == nil {
		t.Fatal("expected ensureSnapDir to fail when the user declines")
	}

	if _, statErr := os.Stat(snapDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected %s not to be created, stat returned %v", snapDir, statErr)
	}
}

func TestEnsureSnapDirWithoutTerminalFails(t *testing.T) {
	withoutTerminal(t)

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	vol := &Volume{Name: "root", SnapDir: snapDir}

	err := ensureSnapDir(context.Background(), vol)
	if err == nil {
		t.Fatal("expected ensureSnapDir to fail without a terminal")
	}
	if !strings.Contains(err.Error(), "btrfs subvolume create") {
		t.Fatalf("expected the error to say how to create it, got %v", err)
	}

	if _, statErr := os.Stat(snapDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected %s not to be created, stat returned %v", snapDir, statErr)
	}
}

func TestEnsureSnapDirRejectsNonDirectory(t *testing.T) {
	withoutTerminal(t)

	snapDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(snapDir, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	err := ensureSnapDir(context.Background(), &Volume{Name: "root", SnapDir: snapDir})
	if err == nil {
		t.Fatal("expected ensureSnapDir to fail when snapdir is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected 'not a directory' error, got %v", err)
	}
}

func TestEnsureSnapDirDryRunOnlyReports(t *testing.T) {
	withDryRun(t)
	withPromptAnswers(t, "s")

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	vol := &Volume{Name: "root", SnapDir: snapDir}

	var err error
	output := captureStdout(t, func() { err = ensureSnapDir(context.Background(), vol) })
	if err != nil {
		t.Fatalf("ensureSnapDir: %v", err)
	}
	if !strings.Contains(output, "[DRY-RUN]") {
		t.Fatalf("expected a dry-run notice, got %q", output)
	}

	if _, statErr := os.Stat(snapDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected %s not to be created, stat returned %v", snapDir, statErr)
	}
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
