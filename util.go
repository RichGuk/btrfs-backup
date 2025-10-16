package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const snapshotTimestampFormat = "2006-01-02_15-04-05"

var snapshotTimestampRegexp = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})`)

func remoteFileSuffix(cfg *Config) string {
	if cfg.EncryptionKey != "" {
		return ".btrfs.age"
	}
	return ".btrfs"
}

func checkBtrfsAccess(vol *Volume) error {
	cmd := exec.Command("btrfs", "subvolume", "list", vol.Src)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error accessing btrfs subvolume at %s: %v", vol.Src, err)
	}
	return nil
}

func extractSnapshotTimestamp(path string) (time.Time, error) {
	base := filepath.Base(path)
	match := snapshotTimestampRegexp.FindStringSubmatch(base)
	if len(match) < 2 {
		return time.Time{}, fmt.Errorf("unable to extract timestamp from %s", base)
	}

	t, err := time.Parse(snapshotTimestampFormat, match[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to parse timestamp %s: %w", match[1], err)
	}

	return t, nil
}

func buildSSHArgs(cfg *Config, remoteCmd string, extraOpts ...string) []string {
	sshArgs := []string{}
	if cfg.SSHKey != "" {
		sshArgs = append(sshArgs, "-i", cfg.SSHKey)
	}
	sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=no")
	sshArgs = append(sshArgs, extraOpts...)
	sshArgs = append(sshArgs, cfg.RemoteHost, remoteCmd)

	return sshArgs
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type remoteBackup struct {
	Name      string
	Timestamp time.Time
	Kind      string
}

func listRemoteBackups(cfg *Config, vol *Volume) ([]remoteBackup, error) {
	remoteCmd := fmt.Sprintf("cd %s && ls -1", shellEscape(cfg.RemoteDest))
	cmd := exec.Command("ssh", buildSSHArgs(cfg, remoteCmd)...)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing remote backups failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		lines = nil
	}

	suffix := regexp.QuoteMeta(remoteFileSuffix(cfg))
	namePattern := fmt.Sprintf(`^%s-(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})\.(full|inc)%s$`, regexp.QuoteMeta(vol.Name), suffix)
	re := regexp.MustCompile(namePattern)

	var backups []remoteBackup
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		match := re.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}

		ts, err := time.Parse(snapshotTimestampFormat, match[1])
		if err != nil {
			continue
		}

		backups = append(backups, remoteBackup{
			Name:      line,
			Timestamp: ts,
			Kind:      match[2],
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.Before(backups[j].Timestamp)
	})

	return backups, nil
}

func remoteBackupForTimestamp(backups []remoteBackup, ts time.Time) bool {
	for _, b := range backups {
		if b.Timestamp.Equal(ts) {
			return true
		}
	}
	return false
}

func latestRemoteFull(backups []remoteBackup) *remoteBackup {
	for i := len(backups) - 1; i >= 0; i-- {
		if backups[i].Kind == "full" {
			b := backups[i]
			return &b
		}
	}
	return nil
}

func countIncrementalsSince(backups []remoteBackup, since time.Time) int {
	count := 0
	for _, b := range backups {
		if b.Kind == "inc" && b.Timestamp.After(since) {
			count++
		}
	}
	return count
}

func needsFullBackup(cfg *Config, vol *Volume, oldSnap string, currentTime time.Time) bool {
	if oldSnap == "" {
		return true
	}

	remoteBackups, err := listRemoteBackups(cfg, vol)
	if err != nil {
		errLog.Printf("Error retrieving remote backups: %v", err)
		return true
	}

	if len(remoteBackups) == 0 {
		if verbose {
			errLog.Println("Remote target has no backups")
		}
		return true
	}

	oldSnapTime, err := extractSnapshotTimestamp(oldSnap)
	if err != nil {
		return true
	}

	if !remoteBackupForTimestamp(remoteBackups, oldSnapTime) {
		if verbose {
			fmt.Printf("→ Remote target missing backup for snapshot %s\n", oldSnapTime.Format(snapshotTimestampFormat))
		}
		return true
	}

	lastFull := latestRemoteFull(remoteBackups)
	if lastFull == nil {
		if verbose {
			errLog.Println("Remote target missing full backup")
		}
		return true
	}

	if cfg.MaxAgeDays > 0 {
		if currentTime.Sub(lastFull.Timestamp) >= time.Duration(cfg.MaxAgeDays)*24*time.Hour {
			if verbose {
				errLog.Printf("→ Last remote full backup is older than %d days", cfg.MaxAgeDays)
			}
			return true
		}
	}

	if cfg.MaxIncrementals > 0 {
		incCount := countIncrementalsSince(remoteBackups, lastFull.Timestamp)
		if incCount >= cfg.MaxIncrementals {
			if verbose {
				errLog.Printf("→ Remote has %d incrementals since last full (limit %d)", incCount, cfg.MaxIncrementals)
			}
			return true
		}
	}

	return false
}
