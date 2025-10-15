package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// latestSnapshot returns the path to the most recent snapshot in snapDir, or an empty string if none exist.
func latestSnapshot(snapDir string) (string, error) {
	entries, err := os.ReadDir(snapDir)
	if err != nil || len(entries) == 0 {
		return "", nil
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil
	}

	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return filepath.Join(snapDir, names[0]), nil
}

func createSnapshot(src, snapDir string, currentTime time.Time) (string, error) {
	name := fmt.Sprintf("btrfs-backup-%s", currentTime.Format("2006-01-02_15-04-05"))
	path := filepath.Join(snapDir, name)

	createCmd := exec.Command("btrfs", "subvolume", "snapshot", "-r", src, path)

	if dryRun {
		fmt.Printf("[DRY-RUN] %s\n", strings.Join(createCmd.Args, " "))
		return path, nil
	}

	return path, createCmd.Run()
}

func sendSnapshot(cfg *Config, newSnap, oldSnap, outfile string, full bool) error {
	tmpFile := outfile + ".tmp"

	sshArgs := buildSSHArgs(cfg, fmt.Sprintf("cat > %s", shellEscape(filepath.Join(cfg.RemoteDest, tmpFile))))

	var sendArgs []string
	if full {
		sendArgs = []string{"send", newSnap}
	} else {
		sendArgs = []string{"send", "-p", oldSnap, newSnap}
	}

	if verbose {
		// fmt.Printf("→ Sending snapshot: btrfs %s | ssh %s\n", strings.Join(sendArgs, " "), strings.Join(sshArgs, " "))
		fmt.Printf("→ Sending snapshot %s → %s:%s\n", newSnap, cfg.RemoteHost, filepath.Join(cfg.RemoteDest, outfile))
	}

	if dryRun {
		cmdLine := fmt.Sprintf("btrfs %s | ssh %s", strings.Join(sendArgs, " "), strings.Join(sshArgs, " "))
		fmt.Printf("[DRY-RUN] %s\n", cmdLine)

		return nil
	}

	sendCmd := exec.Command("btrfs", sendArgs...)
	sshCmd := exec.Command("ssh", sshArgs...)

	if verbose {
		sshCmd.Stdout = os.Stdout
		sshCmd.Stderr = os.Stderr
	}

	pipe, err := sendCmd.StdoutPipe()
	if err != nil {
		return err
	}
	sshCmd.Stdin = pipe

	if err := sendCmd.Start(); err != nil {
		return err
	}

	if err := sshCmd.Run(); err != nil {
		_ = sendCmd.Wait()

		cleanupCmd := exec.Command("ssh", buildSSHArgs(cfg, fmt.Sprintf("rm -f %s", shellEscape(filepath.Join(cfg.RemoteDest, tmpFile))))...)
		_ = cleanupCmd.Run()

		return fmt.Errorf("ssh error: %v", err)
	}

	if err := sendCmd.Wait(); err != nil {
		return fmt.Errorf("btrfs send error: %v", err)
	}
	return nil
}

func deleteOldSnapshot(snapshot string) {
	delCmd := exec.Command("btrfs", "subvolume", "delete", snapshot)

	if verbose {
		fmt.Printf("→ Deleting old local snapshot: %s\n", snapshot)
	}

	if dryRun {
		fmt.Printf("[DRY-RUN] %s\n", strings.Join(delCmd.Args, " "))
	} else {
		if err := delCmd.Run(); err != nil {
			errLog.Printf("Error deleting old snapshot: %v", err)
		}
	}
}

func moveTmpFile(cfg *Config, outfile string) error {
	tmpFile := outfile + ".tmp"
	remoteCmd := fmt.Sprintf(
		"mv %s %s",
		shellEscape(filepath.Join(cfg.RemoteDest, tmpFile)),
		shellEscape(filepath.Join(cfg.RemoteDest, outfile)),
	)

	if dryRun {
		fmt.Printf("[DRY-RUN] ssh %s\n", strings.Join(buildSSHArgs(cfg, remoteCmd), " "))
		return nil
	}

	sshCmd := exec.Command("ssh", buildSSHArgs(cfg, remoteCmd)...)
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr

	return sshCmd.Run()
}

func targetMissingFullbackup(cfg *Config, vol *Volume) bool {
	remoteBase := filepath.Join(cfg.RemoteDest, vol.Name)
	pattern := shellEscape(remoteBase) + "-*.full.btrfs"
	lsCmd := exec.Command("ssh", buildSSHArgs(cfg, fmt.Sprintf("ls %s", pattern))...)

	missingFullBackup := false

	output, err := lsCmd.Output()
	if err != nil {
		missingFullBackup = true
	} else {

		missingFullBackup = len(output) == 0
	}

	if verbose && missingFullBackup {
		errLog.Println("⚠️ Remote target missing full backup")
	}

	return missingFullBackup
}

func remoteMissingGap(cfg *Config, vol *Volume, oldSnap string) bool {
	base := filepath.Base(oldSnap)

	const prefix = "btrfs-backup-"
	if !strings.HasPrefix(base, prefix) {
		if verbose {
			errLog.Printf("⚠️ Snapshot name %s does not follow expected pattern", base)
		}
		return true
	}

	datePart := strings.TrimPrefix(base, prefix)

	// Check if remote has a matching .btrfs file for this timestamp
	remoteBase := filepath.Join(cfg.RemoteDest, vol.Name)
	pattern := shellEscape(remoteBase) + fmt.Sprintf("-%s.*.btrfs", datePart)
	lsCmd := exec.Command("ssh", buildSSHArgs(cfg, fmt.Sprintf("ls %s 2>/dev/null", pattern))...)

	output, err := lsCmd.Output()
	missingGap := err != nil || len(output) == 0

	if verbose && missingGap {
		errLog.Printf("⚠️ Remote target missing backup for snapshot timestamp %s", datePart)
	}

	return missingGap
}

func remoteBackupExists(cfg *Config, outfile string) bool {
	remotePath := shellEscape(filepath.Join(cfg.RemoteDest, outfile))
	lsCmd := exec.Command("ssh", buildSSHArgs(cfg, fmt.Sprintf("test -f %s && echo exists", remotePath))...)

	output, err := lsCmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "exists"
}
