package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShellEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", "''"},
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"with'quote", `'with'\''quote'`},
		{"multi'ple'quotes", `'multi'\''ple'\''quotes'`},
		{"/path/to/file", "'/path/to/file'"},
		{"$VAR", "'$VAR'"},
		{"`cmd`", "'`cmd`'"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellEscape(tt.input)
			if got != tt.want {
				t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildSSHArgs(t *testing.T) {
	t.Parallel()

	t.Run("without SSH key", func(t *testing.T) {
		cfg := &Config{
			RemoteHost: "user@host",
		}
		args := buildSSHArgs(cfg, "ls -la")
		want := []string{"user@host", "ls -la"}
		if len(args) != len(want) {
			t.Fatalf("got %d args, want %d", len(args), len(want))
		}
		for i := range args {
			if args[i] != want[i] {
				t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
			}
		}
	})

	t.Run("with SSH key", func(t *testing.T) {
		cfg := &Config{
			RemoteHost: "user@host",
			SSHKey:     "/path/to/key",
		}
		args := buildSSHArgs(cfg, "ls -la")
		want := []string{"-i", "/path/to/key", "user@host", "ls -la"}
		if len(args) != len(want) {
			t.Fatalf("got %d args, want %d", len(args), len(want))
		}
		for i := range args {
			if args[i] != want[i] {
				t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
			}
		}
	})

	t.Run("with extra options", func(t *testing.T) {
		cfg := &Config{
			RemoteHost: "user@host",
		}
		args := buildSSHArgs(cfg, "ls -la", "-p", "22")
		want := []string{"-p", "22", "user@host", "ls -la"}
		if len(args) != len(want) {
			t.Fatalf("got %d args, want %d", len(args), len(want))
		}
		for i := range args {
			if args[i] != want[i] {
				t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
			}
		}
	})
}

func TestRemoteFileSuffix(t *testing.T) {
	t.Parallel()

	t.Run("without encryption", func(t *testing.T) {
		cfg := &Config{}
		got := remoteFileSuffix(cfg)
		if got != ".btrfs" {
			t.Errorf("got %q, want .btrfs", got)
		}
	})

	t.Run("with encryption", func(t *testing.T) {
		cfg := &Config{
			EncryptionKey: "age-key",
		}
		got := remoteFileSuffix(cfg)
		if got != ".btrfs.age" {
			t.Errorf("got %q, want .btrfs.age", got)
		}
	})
}

func TestExtractSnapshotTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path    string
		wantErr bool
		wantTS  string
	}{
		{
			path:    "/snapshots/btrfs-backup-2024-05-12_11-30-45",
			wantErr: false,
			wantTS:  "2024-05-12 11:30:45 +0000 UTC",
		},
		{
			path:    "btrfs-backup-2025-10-18_09-15-30",
			wantErr: false,
			wantTS:  "2025-10-18 09:15:30 +0000 UTC",
		},
		{
			path:    "/snapshots/invalid-name",
			wantErr: true,
		},
		{
			path:    "no-timestamp-here",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := extractSnapshotTimestamp(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tt.wantTS {
				t.Errorf("got %v, want %v", got, tt.wantTS)
			}
		})
	}
}

func TestRemoteBackupForTimestamp(t *testing.T) {
	t.Parallel()

	ts1 := time.Date(2024, 5, 10, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 5, 11, 10, 0, 0, 0, time.UTC)
	ts3 := time.Date(2024, 5, 12, 10, 0, 0, 0, time.UTC)

	backups := []remoteBackup{
		{Name: "vol-2024-05-10_10-00-00.full.btrfs", Timestamp: ts1, Kind: "full"},
		{Name: "vol-2024-05-11_10-00-00.inc.btrfs", Timestamp: ts2, Kind: "inc"},
	}

	if !remoteBackupForTimestamp(backups, ts1) {
		t.Error("expected ts1 to be found")
	}
	if !remoteBackupForTimestamp(backups, ts2) {
		t.Error("expected ts2 to be found")
	}
	if remoteBackupForTimestamp(backups, ts3) {
		t.Error("expected ts3 to not be found")
	}
}

func TestLatestRemoteFull(t *testing.T) {
	t.Parallel()

	ts1 := time.Date(2024, 5, 10, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 5, 11, 10, 0, 0, 0, time.UTC)
	ts3 := time.Date(2024, 5, 12, 10, 0, 0, 0, time.UTC)

	t.Run("returns latest full backup", func(t *testing.T) {
		backups := []remoteBackup{
			{Name: "vol-2024-05-10_10-00-00.full.btrfs", Timestamp: ts1, Kind: "full"},
			{Name: "vol-2024-05-11_10-00-00.inc.btrfs", Timestamp: ts2, Kind: "inc"},
			{Name: "vol-2024-05-12_10-00-00.full.btrfs", Timestamp: ts3, Kind: "full"},
		}

		got := latestRemoteFull(backups)
		if got == nil {
			t.Fatal("expected full backup, got nil")
		}
		if !got.Timestamp.Equal(ts3) {
			t.Errorf("got timestamp %v, want %v", got.Timestamp, ts3)
		}
	})

	t.Run("returns nil when no full backups", func(t *testing.T) {
		backups := []remoteBackup{
			{Name: "vol-2024-05-11_10-00-00.inc.btrfs", Timestamp: ts2, Kind: "inc"},
		}

		got := latestRemoteFull(backups)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

func TestCountIncrementalsSince(t *testing.T) {
	t.Parallel()

	ts1 := time.Date(2024, 5, 10, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 5, 11, 10, 0, 0, 0, time.UTC)
	ts3 := time.Date(2024, 5, 12, 10, 0, 0, 0, time.UTC)
	ts4 := time.Date(2024, 5, 13, 10, 0, 0, 0, time.UTC)

	backups := []remoteBackup{
		{Name: "vol-2024-05-10_10-00-00.full.btrfs", Timestamp: ts1, Kind: "full"},
		{Name: "vol-2024-05-11_10-00-00.inc.btrfs", Timestamp: ts2, Kind: "inc"},
		{Name: "vol-2024-05-12_10-00-00.inc.btrfs", Timestamp: ts3, Kind: "inc"},
		{Name: "vol-2024-05-13_10-00-00.inc.btrfs", Timestamp: ts4, Kind: "inc"},
	}

	got := countIncrementalsSince(backups, ts1)
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}

	got = countIncrementalsSince(backups, ts2)
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}

	got = countIncrementalsSince(backups, ts4)
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestListRemoteBackups(t *testing.T) {
	_, remoteDir := setupTestEnv(t)

	cfg := &Config{
		RemoteHost: "remote",
		RemoteDest: remoteDir,
	}

	vol := &Volume{
		Name: "testvol",
	}

	t.Run("empty directory", func(t *testing.T) {
		backups, err := listRemoteBackups(context.Background(), cfg, vol)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(backups) != 0 {
			t.Errorf("expected 0 backups, got %d", len(backups))
		}
	})

	t.Run("with backups", func(t *testing.T) {
		files := []string{
			"testvol-2024-05-10_10-00-00.full.btrfs",
			"testvol-2024-05-11_11-00-00.inc.btrfs",
			"testvol-2024-05-12_12-00-00.full.btrfs",
			"othervol-2024-05-10_10-00-00.full.btrfs",
			"testvol-invalid.btrfs",
		}

		for _, f := range files {
			if err := os.WriteFile(filepath.Join(remoteDir, f), []byte("data"), 0o644); err != nil {
				t.Fatalf("creating test file: %v", err)
			}
		}

		backups, err := listRemoteBackups(context.Background(), cfg, vol)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(backups) != 3 {
			t.Fatalf("expected 3 backups, got %d", len(backups))
		}

		if backups[0].Kind != "full" || !backups[0].Timestamp.Equal(time.Date(2024, 5, 10, 10, 0, 0, 0, time.UTC)) {
			t.Errorf("unexpected first backup: %+v", backups[0])
		}
		if backups[1].Kind != "inc" {
			t.Errorf("expected second backup to be inc, got %s", backups[1].Kind)
		}
		if backups[2].Kind != "full" {
			t.Errorf("expected third backup to be full, got %s", backups[2].Kind)
		}
	})
}

func TestNeedsFullBackup(t *testing.T) {
	t.Run("no old snapshot", func(t *testing.T) {
		cfg := &Config{}
		vol := &Volume{Name: "vol"}
		if !needsFullBackup(context.Background(), cfg, vol, "", time.Now()) {
			t.Error("expected full backup when no old snapshot")
		}
	})

	t.Run("no remote backups", func(t *testing.T) {
		_, remoteDir := setupTestEnv(t)
		cfg := &Config{
			RemoteHost: "remote",
			RemoteDest: remoteDir,
		}
		vol := &Volume{Name: "vol"}
		oldSnap := "/snapshots/btrfs-backup-2024-05-10_10-00-00"

		if !needsFullBackup(context.Background(), cfg, vol, oldSnap, time.Now()) {
			t.Error("expected full backup when no remote backups")
		}
	})

	t.Run("missing remote backup for old snapshot timestamp", func(t *testing.T) {
		_, remoteDir := setupTestEnv(t)
		cfg := &Config{
			RemoteHost: "remote",
			RemoteDest: remoteDir,
		}
		vol := &Volume{Name: "vol"}

		if err := os.WriteFile(filepath.Join(remoteDir, "vol-2024-05-09_10-00-00.full.btrfs"), []byte("data"), 0o644); err != nil {
			t.Fatalf("creating test file: %v", err)
		}

		oldSnap := "/snapshots/btrfs-backup-2024-05-10_10-00-00"

		if !needsFullBackup(context.Background(), cfg, vol, oldSnap, time.Now()) {
			t.Error("expected full backup when remote missing backup matching old snapshot timestamp")
		}
	})

	t.Run("only incrementals on remote, no full backup", func(t *testing.T) {
		_, remoteDir := setupTestEnv(t)
		cfg := &Config{
			RemoteHost: "remote",
			RemoteDest: remoteDir,
		}
		vol := &Volume{Name: "vol"}

		if err := os.WriteFile(filepath.Join(remoteDir, "vol-2024-05-10_10-00-00.inc.btrfs"), []byte("data"), 0o644); err != nil {
			t.Fatalf("creating test file: %v", err)
		}

		oldSnap := "/snapshots/btrfs-backup-2024-05-10_10-00-00"

		if !needsFullBackup(context.Background(), cfg, vol, oldSnap, time.Now()) {
			t.Error("expected full backup when remote has only incrementals, no full backup")
		}
	})

	t.Run("full backup too old", func(t *testing.T) {
		_, remoteDir := setupTestEnv(t)
		cfg := &Config{
			RemoteHost: "remote",
			RemoteDest: remoteDir,
			MaxAgeDays: 7,
		}
		vol := &Volume{Name: "vol"}

		oldTime := time.Now().Add(-8 * 24 * time.Hour)
		oldFileName := fmt.Sprintf("vol-%s.full.btrfs", oldTime.Format("2006-01-02_15-04-05"))
		if err := os.WriteFile(filepath.Join(remoteDir, oldFileName), []byte("data"), 0o644); err != nil {
			t.Fatalf("creating test file: %v", err)
		}

		oldSnap := fmt.Sprintf("/snapshots/btrfs-backup-%s", oldTime.Format("2006-01-02_15-04-05"))

		if !needsFullBackup(context.Background(), cfg, vol, oldSnap, time.Now()) {
			t.Error("expected full backup when last full too old")
		}
	})

	t.Run("too many incrementals", func(t *testing.T) {
		_, remoteDir := setupTestEnv(t)
		cfg := &Config{
			RemoteHost:      "remote",
			RemoteDest:      remoteDir,
			MaxIncrementals: 2,
		}
		vol := &Volume{Name: "vol"}

		baseTime := time.Now().Add(-24 * time.Hour)
		fullName := fmt.Sprintf("vol-%s.full.btrfs", baseTime.Format("2006-01-02_15-04-05"))
		if err := os.WriteFile(filepath.Join(remoteDir, fullName), []byte("data"), 0o644); err != nil {
			t.Fatalf("creating test file: %v", err)
		}

		for i := 1; i <= 3; i++ {
			incTime := baseTime.Add(time.Duration(i) * time.Hour)
			incName := fmt.Sprintf("vol-%s.inc.btrfs", incTime.Format("2006-01-02_15-04-05"))
			if err := os.WriteFile(filepath.Join(remoteDir, incName), []byte("data"), 0o644); err != nil {
				t.Fatalf("creating test file: %v", err)
			}
		}

		lastIncTime := baseTime.Add(3 * time.Hour)
		oldSnap := fmt.Sprintf("/snapshots/btrfs-backup-%s", lastIncTime.Format("2006-01-02_15-04-05"))

		if !needsFullBackup(context.Background(), cfg, vol, oldSnap, time.Now()) {
			t.Error("expected full backup when too many incrementals")
		}
	})

	t.Run("incremental is ok", func(t *testing.T) {
		_, remoteDir := setupTestEnv(t)
		cfg := &Config{
			RemoteHost:      "remote",
			RemoteDest:      remoteDir,
			MaxAgeDays:      7,
			MaxIncrementals: 5,
		}
		vol := &Volume{Name: "vol"}

		baseTime := time.Now().Add(-2 * 24 * time.Hour)
		fullName := fmt.Sprintf("vol-%s.full.btrfs", baseTime.Format("2006-01-02_15-04-05"))
		if err := os.WriteFile(filepath.Join(remoteDir, fullName), []byte("data"), 0o644); err != nil {
			t.Fatalf("creating test file: %v", err)
		}

		incTime := baseTime.Add(1 * time.Hour)
		incName := fmt.Sprintf("vol-%s.inc.btrfs", incTime.Format("2006-01-02_15-04-05"))
		if err := os.WriteFile(filepath.Join(remoteDir, incName), []byte("data"), 0o644); err != nil {
			t.Fatalf("creating test file: %v", err)
		}

		oldSnap := fmt.Sprintf("/snapshots/btrfs-backup-%s", incTime.Format("2006-01-02_15-04-05"))

		if needsFullBackup(context.Background(), cfg, vol, oldSnap, time.Now()) {
			t.Error("expected incremental backup to be ok")
		}
	})
}

func TestSnapshotTimestampRegexp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input     string
		wantMatch bool
		wantTS    string
	}{
		{"btrfs-backup-2024-05-12_11-30-45", true, "2024-05-12_11-30-45"},
		{"2025-10-18_09-15-30", true, "2025-10-18_09-15-30"},
		{"prefix-2024-05-12_11-30-45-suffix", true, "2024-05-12_11-30-45"},
		{"no-timestamp-here", false, ""},
		{"2024-05-12", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			match := snapshotTimestampRegexp.FindStringSubmatch(tt.input)
			if tt.wantMatch {
				if len(match) < 2 {
					t.Fatalf("expected match, got none")
				}
				if match[1] != tt.wantTS {
					t.Errorf("got %q, want %q", match[1], tt.wantTS)
				}
			} else {
				if len(match) > 0 {
					t.Errorf("expected no match, got %q", match[0])
				}
			}
		})
	}
}

func TestCheckBtrfsAccess(t *testing.T) {
	setupTestEnv(t)

	logPath := filepath.Join(t.TempDir(), "btrfs.log")
	t.Setenv("BTRFS_LOG", logPath)

	vol := &Volume{
		Name: "testvol",
		Src:  "/mnt/vol",
	}

	err := checkBtrfsAccess(context.Background(), vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err == nil {
		if string(logData) != "" {
			t.Logf("btrfs log: %s", string(logData))
		}
	}
}

func TestCheckBtrfsAccessError(t *testing.T) {
	setupTestEnv(t)

	t.Setenv("BTRFS_FAIL_LIST", "1")

	vol := &Volume{
		Name: "testvol",
		Src:  "/mnt/vol",
	}

	err := checkBtrfsAccess(context.Background(), vol)
	if err == nil {
		t.Fatal("expected error from checkBtrfsAccess")
	}
}
