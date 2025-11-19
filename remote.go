package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type remoteBackup struct {
	Name      string
	Timestamp time.Time
	Kind      string
}

func remoteFileSuffix(cfg *Config) string {
	if cfg.EncryptionKey != "" {
		return ".btrfs.age"
	}
	return ".btrfs"
}

func checkRemoteAccess(ctx context.Context, cfg *Config) error {
	remoteCmd := fmt.Sprintf("test -d %s || mkdir -p %s",
		shellEscape(cfg.RemoteDest),
		shellEscape(cfg.RemoteDest))

	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(cfg, remoteCmd)...)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to access remote host %s: %w (check SSH connectivity and permissions)", cfg.RemoteHost, err)
	}

	if verbose {
		fmt.Printf("→ Remote host %s is accessible\n", cfg.RemoteHost)
	}

	return nil
}

func sendSnapshot(ctx context.Context, cfg *Config, newSnap, oldSnap, outfile string, full bool) (checksum string, err error) {
	ok := false

	tmpFile := outfile + ".tmp"

	// Use tee to write file and compute checksum in parallel during transfer
	remoteWriteCommandSshArgs := buildSSHArgs(cfg, fmt.Sprintf("tee %s | sha256sum", shellEscape(filepath.Join(cfg.RemoteDest, tmpFile))))

	defer func(success *bool) {
		if *success || dryRun {
			return
		}

		cleanupCmd := exec.Command(
			"ssh",
			buildSSHArgs(cfg, fmt.Sprintf("rm -f %s", shellEscape(filepath.Join(cfg.RemoteDest, tmpFile))))...,
		)

		if err := cleanupCmd.Run(); err != nil {
			errLog.Printf("Error during cleanup of remote temp file: %v", err)
		} else if verbose {
			fmt.Printf("→ Cleaned up remote temp file: %s\n", tmpFile)
		}

	}(&ok)

	var sendArgs []string
	if full {
		sendArgs = []string{"send", newSnap}
	} else {
		sendArgs = []string{"send", "-p", oldSnap, newSnap}
	}

	if verbose {
		fmt.Printf(
			"→ [%s] Sending snapshot %s → %s:%s\n",
			map[bool]string{true: "age encrypt", false: "plain"}[cfg.EncryptionKey != ""],
			newSnap,
			cfg.RemoteHost,
			filepath.Join(cfg.RemoteDest, outfile),
		)
	}

	if dryRun {
		if veryVerbose {
			var builder strings.Builder
			builder.WriteString(fmt.Sprintf("btrfs %s", strings.Join(sendArgs, " ")))
			if cfg.EncryptionKey != "" {
				builder.WriteString(fmt.Sprintf(" | age -r %s", cfg.EncryptionKey))
			}
			builder.WriteString(fmt.Sprintf(" | ssh %s", strings.Join(remoteWriteCommandSshArgs, " ")))
			fmt.Printf("[DRY-RUN] %s\n", builder.String())
		}
		return "", nil
	}

	sendCmd := exec.CommandContext(ctx, "btrfs", sendArgs...)
	sendCmd.Stderr = io.Discard
	stdout, err := sendCmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	var stream io.Reader = stdout
	var encryptCmd *exec.Cmd
	if cfg.EncryptionKey != "" {
		encryptCmd = exec.CommandContext(ctx, "age", "-r", cfg.EncryptionKey)
		encryptCmd.Stdin = stream
		encryptCmd.Stderr = os.Stderr
		outPipe, err := encryptCmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		stream = outPipe
	}

	hasher := sha256.New()
	sshCmd := exec.CommandContext(ctx, "ssh", remoteWriteCommandSshArgs...)
	sshCmd.Stderr = os.Stderr

	sshStdout, err := sshCmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	var reader io.Reader
	var progressWriter *ProgressWriter
	if progress {
		progressWriter = NewProgressWriter(os.Stderr, "Transfer")
		reader = io.TeeReader(stream, io.MultiWriter(hasher, progressWriter))
	} else {
		reader = io.TeeReader(stream, hasher)
	}

	sshCmd.Stdin = reader

	if err := sendCmd.Start(); err != nil {
		return "", fmt.Errorf("btrfs send start failed: %w", err)
	}
	if encryptCmd != nil {
		if err := encryptCmd.Start(); err != nil {
			return "", fmt.Errorf("age start failed: %w", err)
		}
	}

	if err := sshCmd.Start(); err != nil {
		_ = sendCmd.Wait()
		if encryptCmd != nil {
			_ = encryptCmd.Wait()
		}
		return "", fmt.Errorf("ssh start failed: %w", err)
	}

	remoteChecksumOutput, err := io.ReadAll(sshStdout)
	if err != nil {
		return "", fmt.Errorf("failed to read remote checksum: %w", err)
	}

	if err := sshCmd.Wait(); err != nil {
		_ = sendCmd.Wait()
		if encryptCmd != nil {
			_ = encryptCmd.Wait()
		}
		return "", fmt.Errorf("ssh failed: %w", err)
	}

	sendErr := sendCmd.Wait()
	var encryptErr error
	if encryptCmd != nil {
		encryptErr = encryptCmd.Wait()
	}

	if encryptErr != nil {
		return "", fmt.Errorf("age failed: %w", encryptErr)
	}
	if sendErr != nil {
		return "", fmt.Errorf("btrfs send failed: %w", sendErr)
	}

	if progressWriter != nil {
		progressWriter.Finish()
	}

	localChecksum := fmt.Sprintf("%x", hasher.Sum(nil))

	remoteChecksumFields := strings.Fields(strings.TrimSpace(string(remoteChecksumOutput)))
	if len(remoteChecksumFields) == 0 {
		return "", fmt.Errorf("unable to parse remote checksum output: %q", string(remoteChecksumOutput))
	}

	remoteChecksum := remoteChecksumFields[0]
	if !strings.EqualFold(remoteChecksum, localChecksum) {
		return "", fmt.Errorf("checksum mismatch: local=%s remote=%s", localChecksum, remoteChecksum)
	}

	if verbose {
		fmt.Printf("→ Checksum validation passed\n")
	}

	ok = true
	return localChecksum, nil
}

func moveTmpFile(ctx context.Context, cfg *Config, outfile, checksum string) error {
	tmpFile := outfile + ".tmp"
	remoteCmd := fmt.Sprintf(
		"mv %s %s",
		shellEscape(filepath.Join(cfg.RemoteDest, tmpFile)),
		shellEscape(filepath.Join(cfg.RemoteDest, outfile)),
	)

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] ssh %s\n", strings.Join(buildSSHArgs(cfg, remoteCmd), " "))
		}
	} else {
		sshCmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(cfg, remoteCmd)...)
		sshCmd.Stdout = os.Stdout
		sshCmd.Stderr = os.Stderr

		if err := sshCmd.Run(); err != nil {
			return err
		}
	}

	if checksum == "" && !dryRun {
		return nil
	}

	checksumValue := checksum
	if checksumValue == "" {
		checksumValue = "<calculated-sha256>"
	}

	checksumFinal := filepath.Join(cfg.RemoteDest, outfile+".sha256")

	checksumCmd := fmt.Sprintf(
		"printf '%%s  %%s\\n' %s %s > %s",
		shellEscape(checksumValue),
		shellEscape(outfile),
		shellEscape(checksumFinal),
	)

	if dryRun {
		return nil
	}

	sshChecksumCmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(cfg, checksumCmd)...)
	sshChecksumCmd.Stdout = os.Stdout
	sshChecksumCmd.Stderr = os.Stderr

	return sshChecksumCmd.Run()
}

func remoteBackupExists(ctx context.Context, cfg *Config, outfile string) bool {
	remotePath := shellEscape(filepath.Join(cfg.RemoteDest, outfile))
	lsCmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(cfg, fmt.Sprintf("test -f %s && echo exists", remotePath))...)

	output, err := lsCmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "exists"
}

func listRemoteBackups(ctx context.Context, cfg *Config, vol *Volume) ([]remoteBackup, error) {
	remoteCmd := fmt.Sprintf("cd %s && ls -1", shellEscape(cfg.RemoteDest))
	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(cfg, remoteCmd)...)

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

func needsFullBackup(ctx context.Context, cfg *Config, vol *Volume, oldSnap string, currentTime time.Time) bool {
	if oldSnap == "" {
		return true
	}

	remoteBackups, err := listRemoteBackups(ctx, cfg, vol)
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
func cleanupOldBackups(ctx context.Context, cfg *Config, vol *Volume, newBackup *remoteBackup) error {
	backups, err := listRemoteBackups(ctx, cfg, vol)
	if err != nil {
		return fmt.Errorf("failed to list remote backups: %w", err)
	}

	if dryRun && newBackup != nil {
		backups = append(backups, *newBackup)
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Timestamp.Before(backups[j].Timestamp)
		})
	}

	if len(backups) < 2 {
		return nil
	}

	fullBackups := []remoteBackup{}
	for _, b := range backups {
		if b.Kind == "full" {
			fullBackups = append(fullBackups, b)
		}
	}

	if len(fullBackups) < 1 {
		return nil
	}

	lastFull := fullBackups[len(fullBackups)-1]

	var toDelete []remoteBackup
	for _, b := range backups {
		if b.Timestamp.Before(lastFull.Timestamp) {
			toDelete = append(toDelete, b)
		}
	}

	if len(toDelete) == 0 {
		return nil
	}

	if verbose {
		fmt.Printf("→ Cleaning up %d old backup(s) for %s (keeping latest full chain)\n", len(toDelete), vol.Name)
	}

	var rmArgs []string
	for _, b := range toDelete {
		backupPath := shellEscape(filepath.Join(cfg.RemoteDest, b.Name))
		checksumPath := shellEscape(filepath.Join(cfg.RemoteDest, b.Name+".sha256"))
		rmArgs = append(rmArgs, backupPath, checksumPath)
		if verbose {
			fmt.Printf("→ Deleting: %s\n", b.Name)
		}
	}

	remoteCmd := fmt.Sprintf("rm -f %s", strings.Join(rmArgs, " "))

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] ssh %s\n", strings.Join(buildSSHArgs(cfg, remoteCmd), " "))
		}
		return nil
	}

	sshCmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(cfg, remoteCmd)...)
	if err := sshCmd.Run(); err != nil {
		return fmt.Errorf("failed to delete old backups: %w", err)
	}

	return nil
}
