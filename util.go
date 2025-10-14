package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

func checkBtrfsAccess(vol *Volume) error {
	cmd := exec.Command("btrfs", "subvolume", "list", vol.Src)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error accessing btrfs subvolume at %s: %v", vol.Src, err)
	}
	return nil
}

func snapshotAge(snapPath string) int {
	base := filepath.Base(snapPath)
	re := regexp.MustCompile(`(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})`)
	match := re.FindStringSubmatch(base)

	if len(match) < 2 {
		return 999
	}

	t, err := time.Parse("2006-01-02_15-04-05", match[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot parse snapshot name %s: %v\n", base, err)
		return 999
	}
	return int(time.Since(t).Hours() / 24)
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

func needsFullBackup(cfg *Config, vol *Volume, oldSnap string) bool {
	return oldSnap == "" ||
		snapshotAge(oldSnap) > cfg.MaxAgeDays ||
		targetMissingFullbackup(cfg, vol) ||
		remoteMissingGap(cfg, vol, oldSnap)
}
