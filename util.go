package main

import (
	"fmt"
	"os"
	"os/exec"
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
	fi, err := os.Stat(snapPath)
	if err != nil {
		return 999
	}
	return int(time.Since(fi.ModTime()).Hours() / 24)
}

func buildSSHArgs(cfg *Config, remoteCmd string, extraOpts ...string) []string {
	sshArgs := []string{}
	if cfg.SSHKey != "" {
		sshArgs = append(sshArgs, "-i", cfg.SSHKey)
	}
	sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	sshArgs = append(sshArgs, extraOpts...)
	sshArgs = append(sshArgs, cfg.RemoteHost, remoteCmd)

	return sshArgs
}
