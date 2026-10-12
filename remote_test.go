package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHPrepareAcceptsExistingDirectory(t *testing.T) {
	withoutTerminal(t)
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{RemoteHost: "remote", Dest: remoteDir}

	if err := newDestination(cfg).Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
}

func TestSSHPrepareCreatesDirectoryWhenConfirmed(t *testing.T) {
	withPromptAnswers(t, "y")
	_, remoteDir := setupTestEnv(t)

	dest := filepath.Join(remoteDir, "nested", "backups")
	cfg := &Config{RemoteHost: "remote", Dest: dest}

	var err error
	captureStdout(t, func() { err = newDestination(cfg).Prepare(context.Background()) })
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	info, statErr := os.Stat(dest)
	if statErr != nil {
		t.Fatalf("stat remote dest: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatal("expected remote dest to be a directory")
	}
}

func TestSSHPrepareLeavesDirectoryAloneWhenDeclined(t *testing.T) {
	withPromptAnswers(t, "n")
	_, remoteDir := setupTestEnv(t)

	dest := filepath.Join(remoteDir, "backups")
	cfg := &Config{RemoteHost: "remote", Dest: dest}

	var err error
	captureStdout(t, func() { err = newDestination(cfg).Prepare(context.Background()) })
	if err == nil {
		t.Fatal("expected Prepare to fail when the user declines")
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected %s not to be created, stat returned %v", dest, statErr)
	}
}

func TestSSHPrepareWithoutTerminalFails(t *testing.T) {
	withoutTerminal(t)
	_, remoteDir := setupTestEnv(t)

	dest := filepath.Join(remoteDir, "backups")
	cfg := &Config{RemoteHost: "remote", Dest: dest}

	err := newDestination(cfg).Prepare(context.Background())
	if err == nil {
		t.Fatal("expected Prepare to fail without a terminal")
	}
	if !strings.Contains(err.Error(), "from a terminal") {
		t.Fatalf("expected the error to explain how to create it, got %v", err)
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected %s not to be created, stat returned %v", dest, statErr)
	}
}

func TestSSHPrepareRejectsNonDirectory(t *testing.T) {
	withoutTerminal(t)
	_, remoteDir := setupTestEnv(t)

	dest := filepath.Join(remoteDir, "not-a-dir")
	if err := os.WriteFile(dest, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	cfg := &Config{RemoteHost: "remote", Dest: dest}

	err := newDestination(cfg).Prepare(context.Background())
	if err == nil {
		t.Fatal("expected Prepare to fail when the remote dest is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected 'not a directory' error, got %v", err)
	}
}

// The remote shell's umask is not ours to control, so every command that
// creates a file sets its own. Otherwise a backup lands world-readable on the
// backup host, which is how it behaved before.
func TestSSHWritesArePrivate(t *testing.T) {
	withPromptAnswers(t, "y")
	_, remoteDir := setupTestEnv(t)

	dest := filepath.Join(remoteDir, "backups")
	cfg := &Config{RemoteHost: "remote", Dest: dest}
	remote := newDestination(cfg)

	var err error
	captureStdout(t, func() { err = remote.Prepare(context.Background()) })
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if mode := statMode(t, dest); mode != 0o700 {
		t.Errorf("remote destination mode = %04o, want 0700", mode)
	}

	outfile := "volume-full.btrfs"
	checksum, err := remote.Receive(context.Background(), outfile, strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	if mode := statMode(t, filepath.Join(dest, outfile+".tmp")); mode != 0o600 {
		t.Errorf("in-flight backup mode = %04o, want 0600", mode)
	}

	if err := remote.Finalise(context.Background(), outfile, checksum); err != nil {
		t.Fatalf("Finalise: %v", err)
	}

	// mv keeps the mode the tee set, so the finished backup stays private.
	if mode := statMode(t, filepath.Join(dest, outfile)); mode != 0o600 {
		t.Errorf("finished backup mode = %04o, want 0600", mode)
	}
	if mode := statMode(t, filepath.Join(dest, outfile+".sha256")); mode != 0o600 {
		t.Errorf("checksum sidecar mode = %04o, want 0600", mode)
	}
}

func TestSSHFinaliseRenamesWithoutChecksum(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	outfile := "volume-full.btrfs"
	tmpPath := filepath.Join(remoteDir, outfile+".tmp")
	finalPath := filepath.Join(remoteDir, outfile)
	if err := os.WriteFile(tmpPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	if err := newDestination(cfg).Finalise(context.Background(), outfile, ""); err != nil {
		t.Fatalf("Finalise: %v", err)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected tmp file to be gone, stat err: %v", err)
	}

	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("unexpected final payload: %q", string(data))
	}

	if _, err := os.Stat(finalPath + ".sha256"); !os.IsNotExist(err) {
		t.Fatalf("expected no checksum file when checksum empty, stat err: %v", err)
	}
}

func TestSSHFinaliseWithChecksum(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	outfile := "volume-inc.btrfs"
	tmpPath := filepath.Join(remoteDir, outfile+".tmp")
	if err := os.WriteFile(tmpPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte("content")))

	if err := newDestination(cfg).Finalise(context.Background(), outfile, checksum); err != nil {
		t.Fatalf("Finalise: %v", err)
	}

	finalPath := filepath.Join(remoteDir, outfile)
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("reading final file: %v", err)
	}

	finalData, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("reading final data: %v", err)
	}
	if string(finalData) != "content" {
		t.Fatalf("unexpected final payload: %q", string(finalData))
	}

	checksumFile := finalPath + ".sha256"
	data, err := os.ReadFile(checksumFile)
	if err != nil {
		t.Fatalf("reading checksum file: %v", err)
	}

	expected := fmt.Sprintf("%s  %s\n", checksum, outfile)
	if string(data) != expected {
		t.Fatalf("unexpected checksum file contents: want %q, got %q", expected, string(data))
	}
}

func TestSSHExists(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	outfile := "volume-full.btrfs"
	path := filepath.Join(remoteDir, outfile)
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("writing remote file: %v", err)
	}

	if !newDestination(cfg).Exists(context.Background(), outfile) {
		t.Fatal("expected remote backup to exist")
	}

	if newDestination(cfg).Exists(context.Background(), "missing.btrfs") {
		t.Fatal("expected missing backup to return false")
	}
}

func TestSendSnapshotFull(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	btrfsLog := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", btrfsLog)

	newSnap := filepath.Join(t.TempDir(), "snap-full")
	payload := []byte("full snapshot data")
	if err := os.WriteFile(newSnap, payload, 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	outfile := "volume-full.btrfs"
	checksum, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, "", outfile, true)
	if err != nil {
		t.Fatalf("sendSnapshot full: %v", err)
	}

	wantHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	if checksum != wantHash {
		t.Fatalf("unexpected checksum: want %s, got %s", wantHash, checksum)
	}

	tmpFile := filepath.Join(remoteDir, outfile+".tmp")
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("reading remote tmp file: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("remote tmp file mismatch: want %q, got %q", string(payload), string(data))
	}

	logData, err := os.ReadFile(btrfsLog)
	if err != nil {
		t.Fatalf("reading btrfs log: %v", err)
	}
	if !strings.Contains(string(logData), fmt.Sprintf("send %s\n", newSnap)) {
		t.Fatalf("expected btrfs send log to contain new snapshot path, got %q", string(logData))
	}
}

func TestSendSnapshotIncrementalWithEncryption(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	tempDir := t.TempDir()
	btrfsLog := filepath.Join(tempDir, "btrfs.log")
	ageLog := filepath.Join(tempDir, "age.log")

	t.Setenv("BTRFS_LOG", btrfsLog)
	t.Setenv("AGE_LOG", ageLog)
	t.Setenv("AGE_PREFIX", "age-prefix:")

	oldSnap := filepath.Join(tempDir, "snap-old")
	if err := os.WriteFile(oldSnap, []byte("old snapshot placeholder"), 0o644); err != nil {
		t.Fatalf("writing old snapshot: %v", err)
	}

	newSnap := filepath.Join(tempDir, "snap-new")
	payload := []byte("incremental snapshot data")
	if err := os.WriteFile(newSnap, payload, 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost:    "remote",
		Dest:          remoteDir,
		EncryptionKey: "age-recipient",
	}

	outfile := "volume-inc.btrfs.age"
	checksum, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, oldSnap, outfile, false)
	if err != nil {
		t.Fatalf("sendSnapshot incremental: %v", err)
	}

	expectedPayload := append([]byte("age-prefix:"), payload...)
	wantHash := fmt.Sprintf("%x", sha256.Sum256(expectedPayload))
	if checksum != wantHash {
		t.Fatalf("unexpected checksum: want %s, got %s", wantHash, checksum)
	}

	tmpFile := filepath.Join(remoteDir, outfile+".tmp")
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("reading remote tmp file: %v", err)
	}
	if string(data) != string(expectedPayload) {
		t.Fatalf("remote tmp file mismatch: want %q, got %q", string(expectedPayload), string(data))
	}

	btrfsLogData, err := os.ReadFile(btrfsLog)
	if err != nil {
		t.Fatalf("reading btrfs log: %v", err)
	}
	expectedLog := fmt.Sprintf("send -p %s %s\n", oldSnap, newSnap)
	if !strings.Contains(string(btrfsLogData), expectedLog) {
		t.Fatalf("expected incremental log entry %q, got %q", expectedLog, string(btrfsLogData))
	}

	ageLogData, err := os.ReadFile(ageLog)
	if err != nil {
		t.Fatalf("reading age log: %v", err)
	}
	if !strings.Contains(string(ageLogData), "-r age-recipient") {
		t.Fatalf("expected age command to include recipient, got %q", string(ageLogData))
	}
}

func TestSendSnapshotFailureCleansUpTempFile(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	tempDir := t.TempDir()
	btrfsLog := filepath.Join(tempDir, "btrfs.log")
	sshLog := filepath.Join(tempDir, "ssh.log")

	t.Setenv("BTRFS_LOG", btrfsLog)
	t.Setenv("SSH_LOG", sshLog)
	t.Setenv("SSH_FAIL_CAT", "1")

	newSnap := filepath.Join(tempDir, "snap-fail")
	payload := []byte("snapshot-failure-data")
	if err := os.WriteFile(newSnap, payload, 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	outfile := "volume-fail.btrfs"
	_, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail, got nil error")
	}
	if !strings.Contains(err.Error(), "ssh failed") {
		t.Fatalf("expected ssh failure error, got %v", err)
	}

	tmpFile := filepath.Join(remoteDir, outfile+".tmp")
	if _, statErr := os.Stat(tmpFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected remote tmp file to be cleaned up, stat err: %v", statErr)
	}

	sshLogData, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatalf("reading ssh log: %v", err)
	}
	if !strings.Contains(string(sshLogData), "rm -f") {
		t.Fatalf("expected cleanup rm command in ssh log, got %q", string(sshLogData))
	}
}

func TestSendSnapshotBtrfsSendStartFailure(t *testing.T) {
	setupTestEnv(t)

	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("creating remote dir: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	newSnap := "/nonexistent/snapshot"
	outfile := "volume-fail.btrfs"

	_, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to btrfs send start failure")
	}
}

func TestSendSnapshotBtrfsSendWaitFailure(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	tempDir := t.TempDir()
	t.Setenv("BTRFS_FAIL_SEND", "1")

	newSnap := filepath.Join(tempDir, "snap-fail")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}

	outfile := "volume-fail.btrfs"
	_, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to btrfs send wait failure")
	}
	if !strings.Contains(err.Error(), "btrfs send failed") {
		t.Fatalf("expected 'btrfs send failed' error, got %v", err)
	}
}

func TestSendSnapshotAgeStartFailure(t *testing.T) {
	setupTestEnv(t)

	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("creating remote dir: %v", err)
	}

	newSnap := filepath.Join(tempDir, "snap")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	t.Setenv("PATH", "/nonexistent")

	cfg := &Config{
		RemoteHost:    "remote",
		Dest:          remoteDir,
		EncryptionKey: "test-key",
	}

	t.Cleanup(func() {
		errLog.SetOutput(os.Stderr)
	})
	errLog.SetOutput(os.NewFile(0, os.DevNull))

	outfile := "volume-fail.btrfs.age"
	_, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to age start failure")
	}
}

func TestSendSnapshotAgeWaitFailure(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	tempDir := t.TempDir()
	t.Setenv("AGE_FAIL", "1")

	newSnap := filepath.Join(tempDir, "snap")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost:    "remote",
		Dest:          remoteDir,
		EncryptionKey: "test-key",
	}

	outfile := "volume-fail.btrfs.age"
	_, err := sendSnapshot(context.Background(), cfg, newDestination(cfg), newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to age wait failure")
	}
	if !strings.Contains(err.Error(), "age failed") {
		t.Fatalf("expected 'age failed' error, got %v", err)
	}
}

func TestCleanupOldBackups(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}
	vol := &Volume{Name: "root"}

	createTestBackup := func(name string) {
		path := filepath.Join(remoteDir, name)
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("creating test backup: %v", err)
		}
		checksumPath := path + ".sha256"
		if err := os.WriteFile(checksumPath, []byte("abc123  "+name), 0o644); err != nil {
			t.Fatalf("creating test checksum: %v", err)
		}
	}

	createTestBackup("root-2024-01-01_10-00-00.full.btrfs")
	createTestBackup("root-2024-01-02_10-00-00.inc.btrfs")
	createTestBackup("root-2024-01-03_10-00-00.inc.btrfs")
	createTestBackup("root-2024-01-04_10-00-00.full.btrfs")
	createTestBackup("root-2024-01-05_10-00-00.inc.btrfs")
	createTestBackup("root-2024-01-06_10-00-00.full.btrfs")

	if err := cleanupOldBackups(context.Background(), cfg, newDestination(cfg), vol, nil); err != nil {
		t.Fatalf("cleanupOldBackups: %v", err)
	}

	entries, err := os.ReadDir(remoteDir)
	if err != nil {
		t.Fatalf("reading remote dir: %v", err)
	}

	remaining := []string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sha256") {
			remaining = append(remaining, e.Name())
		}
	}

	expected := []string{
		"root-2024-01-06_10-00-00.full.btrfs",
	}

	if len(remaining) != len(expected) {
		t.Fatalf("expected %d remaining backups, got %d: %v", len(expected), len(remaining), remaining)
	}

	for i, name := range expected {
		if remaining[i] != name {
			t.Errorf("expected remaining[%d] = %q, got %q", i, name, remaining[i])
		}
	}
}

func TestCleanupOldBackupsNoFullBackups(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}
	vol := &Volume{Name: "root"}

	if err := cleanupOldBackups(context.Background(), cfg, newDestination(cfg), vol, nil); err != nil {
		t.Fatalf("cleanupOldBackups on empty dir: %v", err)
	}
}

func TestCleanupOldBackupsOnlyOneFull(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		Dest:       remoteDir,
	}
	vol := &Volume{Name: "root"}

	createTestBackup := func(name string) {
		path := filepath.Join(remoteDir, name)
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("creating test backup: %v", err)
		}
	}

	createTestBackup("root-2024-01-01_10-00-00.full.btrfs")
	createTestBackup("root-2024-01-02_10-00-00.inc.btrfs")

	if err := cleanupOldBackups(context.Background(), cfg, newDestination(cfg), vol, nil); err != nil {
		t.Fatalf("cleanupOldBackups: %v", err)
	}

	entries, err := os.ReadDir(remoteDir)
	if err != nil {
		t.Fatalf("reading remote dir: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 backups to remain (only 1 full), got %d", len(entries))
	}
}
