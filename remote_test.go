package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMoveTmpFileRenamesWithoutChecksum(t *testing.T) {
	_, remoteDir := setupTestEnv(t)
	withDryRun(t, false)

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-full.btrfs"
	tmpPath := filepath.Join(remoteDir, outfile+".tmp")
	finalPath := filepath.Join(remoteDir, outfile)
	if err := os.WriteFile(tmpPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	if err := moveTmpFile(cfg, outfile, ""); err != nil {
		t.Fatalf("moveTmpFile: %v", err)
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

func TestMoveTmpFileWithChecksum(t *testing.T) {
	_, remoteDir := setupTestEnv(t)
	withDryRun(t, false)

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-inc.btrfs"
	tmpPath := filepath.Join(remoteDir, outfile+".tmp")
	if err := os.WriteFile(tmpPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte("content")))

	if err := moveTmpFile(cfg, outfile, checksum); err != nil {
		t.Fatalf("moveTmpFile: %v", err)
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

func TestMoveTmpFileChecksumMismatch(t *testing.T) {
	_, remoteDir := setupTestEnv(t)
	withDryRun(t, false)

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-inc.btrfs"
	tmpPath := filepath.Join(remoteDir, outfile+".tmp")
	if err := os.WriteFile(tmpPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}

	t.Cleanup(func() {
		errLog.SetOutput(os.Stderr)
	})
	errLog.SetOutput(os.NewFile(0, os.DevNull))

	err := moveTmpFile(cfg, outfile, "deadbeef")
	if err == nil {
		t.Fatal("expected moveTmpFile to fail due to checksum mismatch")
	}

	finalPath := filepath.Join(remoteDir, outfile)
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected final file to be removed, stat err: %v", statErr)
	}

	if _, statErr := os.Stat(filepath.Join(remoteDir, outfile+".tmp")); !os.IsNotExist(statErr) {
		t.Fatalf("expected tmp file to be removed after rename, stat err: %v", statErr)
	}

	if _, statErr := os.Stat(filepath.Join(remoteDir, outfile+".sha256")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no checksum file to be created, stat err: %v", statErr)
	}
}

func TestValidateRemoteChecksum(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-full.btrfs"
	finalPath := filepath.Join(remoteDir, outfile)
	content := []byte("payload")
	if err := os.WriteFile(finalPath, content, 0o644); err != nil {
		t.Fatalf("writing final file: %v", err)
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256(content))
	if err := validateRemoteChecksum(cfg, outfile, checksum); err != nil {
		t.Fatalf("validateRemoteChecksum: %v", err)
	}

	t.Cleanup(func() {
		errLog.SetOutput(os.Stderr)
	})
	errLog.SetOutput(os.NewFile(0, os.DevNull))

	if err := validateRemoteChecksum(cfg, outfile, "deadbeef"); err == nil {
		t.Fatal("expected checksum validation failure")
	}
}

func TestRemoteBackupExists(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-full.btrfs"
	path := filepath.Join(remoteDir, outfile)
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("writing remote file: %v", err)
	}

	if !remoteBackupExists(cfg, outfile) {
		t.Fatal("expected remote backup to exist")
	}

	if remoteBackupExists(cfg, "missing.btrfs") {
		t.Fatal("expected missing backup to return false")
	}
}

func TestSendSnapshotFull(t *testing.T) {
	_, remoteDir := setupTestEnv(t)
	withDryRun(t, false)

	btrfsLog := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", btrfsLog)

	newSnap := filepath.Join(t.TempDir(), "snap-full")
	payload := []byte("full snapshot data")
	if err := os.WriteFile(newSnap, payload, 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-full.btrfs"
	checksum, err := sendSnapshot(cfg, newSnap, "", outfile, true)
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
	withDryRun(t, false)

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
		RemoteDest:    remoteDir,
		EncryptionKey: "age-recipient",
	}

	outfile := "volume-inc.btrfs.age"
	checksum, err := sendSnapshot(cfg, newSnap, oldSnap, outfile, false)
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
	withDryRun(t, false)

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
		RemoteDest: remoteDir,
	}

	outfile := "volume-fail.btrfs"
	_, err := sendSnapshot(cfg, newSnap, "", outfile, true)
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
	withDryRun(t, false)

	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("creating remote dir: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	newSnap := "/nonexistent/snapshot"
	outfile := "volume-fail.btrfs"

	_, err := sendSnapshot(cfg, newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to btrfs send start failure")
	}
}

func TestSendSnapshotBtrfsSendWaitFailure(t *testing.T) {
	_, remoteDir := setupTestEnv(t)
	withDryRun(t, false)

	tempDir := t.TempDir()
	t.Setenv("BTRFS_FAIL_SEND", "1")

	newSnap := filepath.Join(tempDir, "snap-fail")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	outfile := "volume-fail.btrfs"
	_, err := sendSnapshot(cfg, newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to btrfs send wait failure")
	}
	if !strings.Contains(err.Error(), "btrfs send failed") {
		t.Fatalf("expected 'btrfs send failed' error, got %v", err)
	}
}

func TestSendSnapshotAgeStartFailure(t *testing.T) {
	setupTestEnv(t)
	withDryRun(t, false)

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
		RemoteDest:    remoteDir,
		EncryptionKey: "test-key",
	}

	t.Cleanup(func() {
		errLog.SetOutput(os.Stderr)
	})
	errLog.SetOutput(os.NewFile(0, os.DevNull))

	outfile := "volume-fail.btrfs.age"
	_, err := sendSnapshot(cfg, newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to age start failure")
	}
}

func TestSendSnapshotAgeWaitFailure(t *testing.T) {
	_, remoteDir := setupTestEnv(t)
	withDryRun(t, false)

	tempDir := t.TempDir()
	t.Setenv("AGE_FAIL", "1")

	newSnap := filepath.Join(tempDir, "snap")
	if err := os.WriteFile(newSnap, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing new snapshot: %v", err)
	}

	cfg := &Config{
		RemoteHost:    "remote",
		RemoteDest:    remoteDir,
		EncryptionKey: "test-key",
	}

	outfile := "volume-fail.btrfs.age"
	_, err := sendSnapshot(cfg, newSnap, "", outfile, true)
	if err == nil {
		t.Fatal("expected sendSnapshot to fail due to age wait failure")
	}
	if !strings.Contains(err.Error(), "age failed") {
		t.Fatalf("expected 'age failed' error, got %v", err)
	}
}
