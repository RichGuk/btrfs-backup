package main

import (
	"crypto/sha256"
	"fmt"
	"io"
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

func sendSnapshot(cfg *Config, newSnap, oldSnap, outfile string, full bool) (checksum string, err error) {
	ok := false

	tmpFile := outfile + ".tmp"
	sshArgs := buildSSHArgs(cfg, fmt.Sprintf("cat > %s", shellEscape(filepath.Join(cfg.RemoteDest, tmpFile))))

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
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("btrfs %s", strings.Join(sendArgs, " ")))
		if cfg.EncryptionKey != "" {
			builder.WriteString(fmt.Sprintf(" | age -r %s", cfg.EncryptionKey))
		}
		builder.WriteString(fmt.Sprintf(" | ssh %s", strings.Join(sshArgs, " ")))
		fmt.Printf("[DRY-RUN] %s\n", builder.String())
		return "", nil
	}

	sendCmd := exec.Command("btrfs", sendArgs...)
	stdout, err := sendCmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	var stream io.Reader = stdout
	var encryptCmd *exec.Cmd
	if cfg.EncryptionKey != "" {
		encryptCmd = exec.Command("age", "-r", cfg.EncryptionKey)
		encryptCmd.Stdin = stream
		outPipe, err := encryptCmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		stream = outPipe
	}

	hasher := sha256.New()
	sshCmd := exec.Command("ssh", sshArgs...)
	sshCmd.Stdin = io.TeeReader(stream, hasher)

	if err := sendCmd.Start(); err != nil {
		return "", fmt.Errorf("btrfs send start failed: %w", err)
	}
	if encryptCmd != nil {
		if err := encryptCmd.Start(); err != nil {
			return "", fmt.Errorf("age start failed: %w", err)
		}
	}

	if err := sshCmd.Run(); err != nil {
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

	ok = true
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
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

func moveTmpFile(cfg *Config, outfile, checksum string) error {
	tmpFile := outfile + ".tmp"
	remoteCmd := fmt.Sprintf(
		"mv %s %s",
		shellEscape(filepath.Join(cfg.RemoteDest, tmpFile)),
		shellEscape(filepath.Join(cfg.RemoteDest, outfile)),
	)

	if dryRun {
		fmt.Printf("[DRY-RUN] ssh %s\n", strings.Join(buildSSHArgs(cfg, remoteCmd), " "))
	} else {
		sshCmd := exec.Command("ssh", buildSSHArgs(cfg, remoteCmd)...)
		sshCmd.Stdout = os.Stdout
		sshCmd.Stderr = os.Stderr

		if err := sshCmd.Run(); err != nil {
			return err
		}
	}

	if !dryRun && checksum != "" {
		if err := validateRemoteChecksum(cfg, outfile, checksum); err != nil {
			errLog.Printf("Checksum validation failed for %s: %v", outfile, err)

			// Best-effort cleanup so callers do not consider this a valid backup
			cleanupCmd := exec.Command(
				"ssh",
				buildSSHArgs(cfg, fmt.Sprintf("rm -f %s", shellEscape(filepath.Join(cfg.RemoteDest, outfile))))...,
			)
			_ = cleanupCmd.Run()

			return err
		} else if verbose {
			fmt.Printf("→ Checksum validation passed for %s\n", outfile)
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
		fmt.Printf("[DRY-RUN] ssh %s\n", strings.Join(buildSSHArgs(cfg, checksumCmd), " "))
		return nil
	}

	sshChecksumCmd := exec.Command("ssh", buildSSHArgs(cfg, checksumCmd)...)
	sshChecksumCmd.Stdout = os.Stdout
	sshChecksumCmd.Stderr = os.Stderr

	return sshChecksumCmd.Run()
}

func validateRemoteChecksum(cfg *Config, outfile, checksum string) error {
	remotePath := filepath.Join(cfg.RemoteDest, outfile)
	checksumCmd := fmt.Sprintf("sha256sum %s", shellEscape(remotePath))

	sshChecksumCmd := exec.Command("ssh", buildSSHArgs(cfg, checksumCmd)...)
	output, err := sshChecksumCmd.Output()
	if err != nil {
		return err
	}

	remoteChecksumFields := strings.Fields(strings.TrimSpace(string(output)))
	if len(remoteChecksumFields) == 0 {
		return fmt.Errorf("unable to parse remote checksum output: %q", string(output))
	}

	remoteChecksum := remoteChecksumFields[0]
	if !strings.EqualFold(remoteChecksum, checksum) {
		return fmt.Errorf("expected %s but remote reported %s", checksum, remoteChecksum)
	}

	return nil
}

func remoteBackupExists(cfg *Config, outfile string) bool {
	remotePath := shellEscape(filepath.Join(cfg.RemoteDest, outfile))
	lsCmd := exec.Command("ssh", buildSSHArgs(cfg, fmt.Sprintf("test -f %s && echo exists", remotePath))...)

	output, err := lsCmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "exists"
}
