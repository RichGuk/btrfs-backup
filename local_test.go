package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localTestDest(t *testing.T) (*Config, destination, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating dest dir: %v", err)
	}

	cfg := &Config{Dest: dir}

	return cfg, newDestination(cfg), dir
}

func TestNewDestinationPicksLocalWithoutRemoteHost(t *testing.T) {
	t.Parallel()

	if _, ok := newDestination(&Config{Dest: "/backups"}).(*localDestination); !ok {
		t.Error("expected a local destination when remote_host is unset")
	}

	if _, ok := newDestination(&Config{RemoteHost: "host", Dest: "/backups"}).(*sshDestination); !ok {
		t.Error("expected an ssh destination when remote_host is set")
	}
}

func TestLocalPrepareCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "backups")
	dest := &localDestination{dir: dir}

	if err := dest.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dest dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected dest to be a directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dest dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected write check file to be cleaned up, got %v", entries)
	}
}

func TestLocalPrepareRejectsNonDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	err := (&localDestination{dir: path}).Prepare(context.Background())
	if err == nil {
		t.Fatal("expected Prepare to fail when dest is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected 'not a directory' error, got %v", err)
	}
}

func TestLocalPrepareRejectsUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("creating readonly dir: %v", err)
	}

	if err := (&localDestination{dir: dir}).Prepare(context.Background()); err == nil {
		t.Fatal("expected Prepare to fail on an unwritable dest")
	}
}

// The point of the local read-back is that the returned checksum describes the
// file on disk, so verify it against an independent hash of that file.
func TestLocalReceiveHashesTheStoredFile(t *testing.T) {
	_, dest, dir := localTestDest(t)

	payload := []byte("local backup payload")

	checksum, err := dest.Receive(context.Background(), "vol.btrfs", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	tmpPath := filepath.Join(dir, "vol.btrfs.tmp")
	stored, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("reading tmp file: %v", err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("stored payload mismatch: want %q, got %q", payload, stored)
	}

	want := fmt.Sprintf("%x", sha256.Sum256(payload))
	if checksum != want {
		t.Fatalf("unexpected checksum: want %s, got %s", want, checksum)
	}
}

func TestLocalReceiveTruncatesStaleTempFile(t *testing.T) {
	_, dest, dir := localTestDest(t)

	tmpPath := filepath.Join(dir, "vol.btrfs.tmp")
	if err := os.WriteFile(tmpPath, []byte("leftover from an earlier run"), 0o644); err != nil {
		t.Fatalf("writing stale tmp file: %v", err)
	}

	if _, err := dest.Receive(context.Background(), "vol.btrfs", strings.NewReader("new")); err != nil {
		t.Fatalf("Receive: %v", err)
	}

	stored, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("reading tmp file: %v", err)
	}
	if string(stored) != "new" {
		t.Fatalf("expected stale bytes to be truncated, got %q", stored)
	}
}

func TestLocalFinaliseRenamesWithoutChecksum(t *testing.T) {
	_, dest, dir := localTestDest(t)

	outfile := "volume-full.btrfs"
	tmpPath := filepath.Join(dir, outfile+".tmp")
	if err := os.WriteFile(tmpPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	if err := dest.Finalise(context.Background(), outfile, ""); err != nil {
		t.Fatalf("Finalise: %v", err)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected tmp file to be gone, stat err: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, outfile))
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("unexpected final payload: %q", data)
	}

	if _, err := os.Stat(filepath.Join(dir, outfile+".sha256")); !os.IsNotExist(err) {
		t.Fatalf("expected no checksum file when checksum empty, stat err: %v", err)
	}
}

func TestLocalFinaliseWritesChecksumSidecar(t *testing.T) {
	_, dest, dir := localTestDest(t)

	outfile := "volume-inc.btrfs"
	if err := os.WriteFile(filepath.Join(dir, outfile+".tmp"), []byte("content"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte("content")))

	if err := dest.Finalise(context.Background(), outfile, checksum); err != nil {
		t.Fatalf("Finalise: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, outfile+".sha256"))
	if err != nil {
		t.Fatalf("reading checksum file: %v", err)
	}

	// Must match the sha256sum(1) format the SSH destination writes, so the
	// sidecars are interchangeable between destinations.
	expected := fmt.Sprintf("%s  %s\n", checksum, outfile)
	if string(data) != expected {
		t.Fatalf("unexpected checksum file contents: want %q, got %q", expected, data)
	}
}

func TestLocalFinaliseFailsWithoutTempFile(t *testing.T) {
	_, dest, _ := localTestDest(t)

	if err := dest.Finalise(context.Background(), "missing.btrfs", ""); err == nil {
		t.Fatal("expected Finalise to fail when the temp file is absent")
	}
}

func TestLocalExists(t *testing.T) {
	_, dest, dir := localTestDest(t)

	if err := os.WriteFile(filepath.Join(dir, "volume-full.btrfs"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("writing backup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "a-directory.btrfs"), 0o755); err != nil {
		t.Fatalf("creating directory: %v", err)
	}

	if !dest.Exists(context.Background(), "volume-full.btrfs") {
		t.Error("expected backup to exist")
	}
	if dest.Exists(context.Background(), "missing.btrfs") {
		t.Error("expected missing backup to return false")
	}
	if dest.Exists(context.Background(), "a-directory.btrfs") {
		t.Error("expected a directory not to count as an existing backup")
	}
}

func TestLocalList(t *testing.T) {
	_, dest, dir := localTestDest(t)

	names, err := dest.List(context.Background())
	if err != nil {
		t.Fatalf("List on empty dir: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected no entries, got %v", names)
	}

	for _, name := range []string{"a.btrfs", "b.btrfs"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	names, err = dest.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 entries, got %v", names)
	}
}

func TestLocalListMissingDirectoryErrors(t *testing.T) {
	dest := &localDestination{dir: filepath.Join(t.TempDir(), "nope")}

	if _, err := dest.List(context.Background()); err == nil {
		t.Fatal("expected List to fail on a missing directory")
	}
}

func TestLocalRemoveIgnoresMissingFiles(t *testing.T) {
	_, dest, dir := localTestDest(t)

	present := filepath.Join(dir, "present.btrfs")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	// Cleanup always asks for the .sha256 sidecar too, which may not exist.
	if err := dest.Remove(context.Background(), "present.btrfs", "present.btrfs.sha256"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(present); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, stat err: %v", err)
	}
}

func TestLocalDiscardRemovesTempFile(t *testing.T) {
	_, dest, dir := localTestDest(t)

	tmpPath := filepath.Join(dir, "volume.btrfs.tmp")
	if err := os.WriteFile(tmpPath, []byte("partial"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	dest.Discard("volume.btrfs")

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected tmp file to be removed, stat err: %v", err)
	}
}

func TestSendSnapshotLocalFull(t *testing.T) {
	setupTestEnv(t)

	tempDir := t.TempDir()
	btrfsLog := filepath.Join(tempDir, "btrfs.log")
	sshLog := filepath.Join(tempDir, "ssh.log")
	t.Setenv("BTRFS_LOG", btrfsLog)
	t.Setenv("SSH_LOG", sshLog)

	cfg, dest, dir := localTestDest(t)

	newSnap := filepath.Join(tempDir, "snap-full")
	payload := []byte("full snapshot data")
	if err := os.WriteFile(newSnap, payload, 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	outfile := "volume-full.btrfs"
	checksum, err := sendSnapshot(context.Background(), cfg, dest, newSnap, "", outfile, true)
	if err != nil {
		t.Fatalf("sendSnapshot: %v", err)
	}

	want := fmt.Sprintf("%x", sha256.Sum256(payload))
	if checksum != want {
		t.Fatalf("unexpected checksum: want %s, got %s", want, checksum)
	}

	data, err := os.ReadFile(filepath.Join(dir, outfile+".tmp"))
	if err != nil {
		t.Fatalf("reading tmp file: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("tmp file mismatch: want %q, got %q", payload, data)
	}

	logData, err := os.ReadFile(btrfsLog)
	if err != nil {
		t.Fatalf("reading btrfs log: %v", err)
	}
	if !strings.Contains(string(logData), fmt.Sprintf("send %s\n", newSnap)) {
		t.Fatalf("expected btrfs send log entry, got %q", logData)
	}

	if _, err := os.Stat(sshLog); !os.IsNotExist(err) {
		t.Fatalf("expected no ssh invocation for a local destination, stat err: %v", err)
	}
}

func TestSendSnapshotLocalIncrementalWithEncryption(t *testing.T) {
	setupTestEnv(t)

	tempDir := t.TempDir()
	ageLog := filepath.Join(tempDir, "age.log")
	t.Setenv("AGE_LOG", ageLog)
	t.Setenv("AGE_PREFIX", "age-prefix:")

	cfg, dest, dir := localTestDest(t)
	cfg.EncryptionKey = "age-recipient"

	oldSnap := filepath.Join(tempDir, "snap-old")
	if err := os.WriteFile(oldSnap, []byte("old snapshot placeholder"), 0o644); err != nil {
		t.Fatalf("writing old snapshot: %v", err)
	}

	newSnap := filepath.Join(tempDir, "snap-new")
	payload := []byte("incremental snapshot data")
	if err := os.WriteFile(newSnap, payload, 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	outfile := "volume-inc.btrfs.age"
	checksum, err := sendSnapshot(context.Background(), cfg, dest, newSnap, oldSnap, outfile, false)
	if err != nil {
		t.Fatalf("sendSnapshot: %v", err)
	}

	expectedPayload := append([]byte("age-prefix:"), payload...)
	want := fmt.Sprintf("%x", sha256.Sum256(expectedPayload))
	if checksum != want {
		t.Fatalf("unexpected checksum: want %s, got %s", want, checksum)
	}

	data, err := os.ReadFile(filepath.Join(dir, outfile+".tmp"))
	if err != nil {
		t.Fatalf("reading tmp file: %v", err)
	}
	if string(data) != string(expectedPayload) {
		t.Fatalf("tmp file mismatch: want %q, got %q", expectedPayload, data)
	}

	ageLogData, err := os.ReadFile(ageLog)
	if err != nil {
		t.Fatalf("reading age log: %v", err)
	}
	if !strings.Contains(string(ageLogData), "-r age-recipient") {
		t.Fatalf("expected age command to include recipient, got %q", ageLogData)
	}
}

func TestSendSnapshotLocalFailureCleansUpTempFile(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("BTRFS_FAIL_SEND", "1")

	cfg, dest, dir := localTestDest(t)

	newSnap := filepath.Join(t.TempDir(), "snap-fail")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	outfile := "volume-fail.btrfs"
	if _, err := sendSnapshot(context.Background(), cfg, dest, newSnap, "", outfile, true); err == nil {
		t.Fatal("expected sendSnapshot to fail")
	}

	if _, err := os.Stat(filepath.Join(dir, outfile+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("expected tmp file to be cleaned up, stat err: %v", err)
	}
}

func TestSendSnapshotLocalUnwritableDestination(t *testing.T) {
	setupTestEnv(t)

	cfg := &Config{Dest: filepath.Join(t.TempDir(), "missing")}
	dest := newDestination(cfg)

	newSnap := filepath.Join(t.TempDir(), "snap")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	_, err := sendSnapshot(context.Background(), cfg, dest, newSnap, "", "volume.btrfs", true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail when the destination is missing")
	}
	if !strings.Contains(err.Error(), "failed to create") {
		t.Fatalf("expected a create failure, got %v", err)
	}
}

func TestLocalCleanupOldBackups(t *testing.T) {
	cfg, dest, dir := localTestDest(t)
	vol := &Volume{Name: "root"}

	names := []string{
		"root-2024-01-01_10-00-00.full.btrfs",
		"root-2024-01-02_10-00-00.inc.btrfs",
		"root-2024-01-03_10-00-00.full.btrfs",
		"root-2024-01-04_10-00-00.inc.btrfs",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatalf("writing backup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".sha256"), []byte("abc  "+name), 0o644); err != nil {
			t.Fatalf("writing checksum: %v", err)
		}
	}

	if err := cleanupOldBackups(context.Background(), cfg, dest, vol, nil); err != nil {
		t.Fatalf("cleanupOldBackups: %v", err)
	}

	remaining, err := dest.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := map[string]bool{
		"root-2024-01-03_10-00-00.full.btrfs":        true,
		"root-2024-01-03_10-00-00.full.btrfs.sha256": true,
		"root-2024-01-04_10-00-00.inc.btrfs":         true,
		"root-2024-01-04_10-00-00.inc.btrfs.sha256":  true,
	}

	if len(remaining) != len(want) {
		t.Fatalf("expected %d entries, got %v", len(want), remaining)
	}
	for _, name := range remaining {
		if !want[name] {
			t.Errorf("unexpected surviving entry %q", name)
		}
	}
}

func TestLocalNeedsFullBackup(t *testing.T) {
	cfg, dest, dir := localTestDest(t)
	cfg.MaxAgeDays = 7
	cfg.MaxIncrementals = 5
	vol := &Volume{Name: "vol"}

	oldSnap := "/snapshots/btrfs-backup-2024-05-10_10-00-00"

	if !needsFullBackup(context.Background(), cfg, dest, vol, oldSnap, mustParseTime(t, "2024-05-11_10-00-00")) {
		t.Error("expected full backup when the destination is empty")
	}

	if err := os.WriteFile(filepath.Join(dir, "vol-2024-05-10_10-00-00.full.btrfs"), []byte("data"), 0o644); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	if needsFullBackup(context.Background(), cfg, dest, vol, oldSnap, mustParseTime(t, "2024-05-11_10-00-00")) {
		t.Error("expected an incremental to be fine with a recent matching full backup")
	}

	if !needsFullBackup(context.Background(), cfg, dest, vol, oldSnap, mustParseTime(t, "2024-05-20_10-00-00")) {
		t.Error("expected full backup once the last full is older than max_age_days")
	}
}

// Dry run must not touch the destination. These flags are package globals, so
// this test cannot run in parallel with the others.
func TestLocalDryRunMakesNoChanges(t *testing.T) {
	setupTestEnv(t)

	prevDryRun, prevVerbose, prevVeryVerbose := dryRun, verbose, veryVerbose
	dryRun, verbose, veryVerbose = true, false, false
	t.Cleanup(func() {
		dryRun, verbose, veryVerbose = prevDryRun, prevVerbose, prevVeryVerbose
	})

	cfg, dest, dir := localTestDest(t)

	newSnap := filepath.Join(t.TempDir(), "snap")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	outfile := "volume-full.btrfs"

	checksum, err := sendSnapshot(context.Background(), cfg, dest, newSnap, "", outfile, true)
	if err != nil {
		t.Fatalf("sendSnapshot: %v", err)
	}
	if checksum != "" {
		t.Errorf("expected no checksum in dry run, got %q", checksum)
	}

	if err := dest.Finalise(context.Background(), outfile, "abc123"); err != nil {
		t.Fatalf("Finalise: %v", err)
	}

	existing := filepath.Join(dir, "keep-me.btrfs")
	if err := os.WriteFile(existing, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	if err := dest.Remove(context.Background(), "keep-me.btrfs"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("expected dry run to leave the backup alone: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dest dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected dry run to leave only the pre-existing backup, got %v", entries)
	}
}

// The -vv dry-run preview is meant to be readable as shell, so each stage has
// to carry the operator that joins it to the upstream pipeline. A redirect must
// not be pasted after a pipe.
func TestReceiveStagePreviews(t *testing.T) {
	t.Parallel()

	local := newDestination(&Config{Dest: "/mnt/backups"})
	if got, want := local.ReceiveStage("vol.btrfs"), "> /mnt/backups/vol.btrfs.tmp"; got != want {
		t.Errorf("local ReceiveStage = %q, want %q", got, want)
	}
	if got, want := local.Path("vol.btrfs"), "/mnt/backups/vol.btrfs"; got != want {
		t.Errorf("local Path = %q, want %q", got, want)
	}

	remote := newDestination(&Config{RemoteHost: "user@host", Dest: "/data/backups"})
	if got, want := remote.Path("vol.btrfs"), "user@host:/data/backups/vol.btrfs"; got != want {
		t.Errorf("ssh Path = %q, want %q", got, want)
	}

	// The remote command must be quoted, or the preview reads as though
	// sha256sum runs locally.
	got, want := remote.ReceiveStage("vol.btrfs"), `| ssh user@host 'tee '\''/data/backups/vol.btrfs.tmp'\'' | sha256sum'`
	if got != want {
		t.Errorf("ssh ReceiveStage = %q, want %q", got, want)
	}
}

func TestDryRunPreviewPipelineIsValidShell(t *testing.T) {
	prevDryRun, prevVerbose, prevVeryVerbose := dryRun, verbose, veryVerbose
	dryRun, verbose, veryVerbose = true, true, true
	t.Cleanup(func() {
		dryRun, verbose, veryVerbose = prevDryRun, prevVerbose, prevVeryVerbose
	})

	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "local plain",
			cfg:  &Config{Dest: "/mnt/backups"},
			want: "btrfs send /snap > /mnt/backups/vol.btrfs.tmp",
		},
		{
			name: "local encrypted",
			cfg:  &Config{Dest: "/mnt/backups", EncryptionKey: "age1key"},
			want: "btrfs send /snap | age -r age1key > /mnt/backups/vol.btrfs.tmp",
		},
		{
			name: "ssh plain",
			cfg:  &Config{RemoteHost: "user@host", Dest: "/data"},
			want: `btrfs send /snap | ssh user@host 'tee '\''/data/vol.btrfs.tmp'\'' | sha256sum'`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureStdout(t, func() {
				if _, err := sendSnapshot(context.Background(), tc.cfg, newDestination(tc.cfg), "/snap", "", "vol.btrfs", true); err != nil {
					t.Fatalf("sendSnapshot: %v", err)
				}
			})

			want := "[DRY-RUN] " + tc.want
			if !strings.Contains(output, want) {
				t.Errorf("preview missing %q\ngot:\n%s", want, output)
			}
			// A redirect after a pipe truncates the file instead of writing it.
			if strings.Contains(output, "| >") {
				t.Errorf("preview pipes into a redirect: %s", output)
			}
		})
	}
}
