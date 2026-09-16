package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// tmpSuffix marks an in-flight transfer. A backup only takes its real name once
// its bytes have been verified, so a partial transfer can never be mistaken for
// a usable backup.
const tmpSuffix = ".tmp"

type backup struct {
	Name      string
	Timestamp time.Time
	Kind      string
}

// destination is where finished backup images are stored. Implementations stage
// every write to name+tmpSuffix and only rename it in Finalise.
type destination interface {
	// Prepare makes sure the destination directory exists and is usable.
	Prepare(ctx context.Context) error
	// Exists reports whether a finished backup called name is already there.
	Exists(ctx context.Context, name string) bool
	// List returns the names of every entry in the destination directory.
	List(ctx context.Context) ([]string, error)
	// Receive writes stream to name+tmpSuffix and returns the SHA256 of the
	// bytes as they ended up at the destination, so the caller can compare it
	// against what it sent.
	Receive(ctx context.Context, name string, stream io.Reader) (checksum string, err error)
	// Discard removes the temp file left behind by a failed Receive. It runs
	// after cancellation, so it must not depend on the run's context.
	Discard(name string)
	// Finalise renames name+tmpSuffix to name and, when checksum is non-empty,
	// writes the name.sha256 sidecar.
	Finalise(ctx context.Context, name, checksum string) error
	// Remove deletes the named entries, ignoring ones that are already gone.
	Remove(ctx context.Context, names ...string) error
	// Path renders where name lives, for log output.
	Path(name string) string
	// ReceiveStage renders the Receive step for -vv dry-run previews. It
	// includes the shell operator joining it to the upstream pipeline, because
	// only the destination knows whether it is a pipe or a redirect.
	ReceiveStage(name string) string
}

func newDestination(cfg *Config) destination {
	if cfg.IsLocal() {
		return &localDestination{dir: cfg.Dest}
	}
	return &sshDestination{cfg: cfg}
}

func backupFileSuffix(cfg *Config) string {
	if cfg.EncryptionKey != "" {
		return ".btrfs.age"
	}
	return ".btrfs"
}

// sha256File hashes the file at path. Used to read a written backup back and
// confirm the stored bytes match what was sent.
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %s for verification: %w", path, err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("reading %s for verification: %w", path, err)
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

// sendSnapshot streams a snapshot to dest, optionally through age, and verifies
// the checksum the destination reports against the one computed on the way out.
func sendSnapshot(ctx context.Context, cfg *Config, dest destination, newSnap, oldSnap, outfile string, full bool) (checksum string, err error) {
	ok := false

	defer func(success *bool) {
		if *success || dryRun {
			return
		}
		dest.Discard(outfile)
	}(&ok)

	var sendArgs []string
	if full {
		sendArgs = []string{"send", newSnap}
	} else {
		sendArgs = []string{"send", "-p", oldSnap, newSnap}
	}

	if verbose {
		fmt.Printf(
			"→ [%s] Sending snapshot %s → %s\n",
			map[bool]string{true: "age encrypt", false: "plain"}[cfg.EncryptionKey != ""],
			newSnap,
			dest.Path(outfile),
		)
	}

	if dryRun {
		if veryVerbose {
			var builder strings.Builder
			builder.WriteString(fmt.Sprintf("btrfs %s", strings.Join(sendArgs, " ")))
			if cfg.EncryptionKey != "" {
				builder.WriteString(fmt.Sprintf(" | age -r %s", cfg.EncryptionKey))
			}
			builder.WriteString(fmt.Sprintf(" %s", dest.ReceiveStage(outfile)))
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

	var reader io.Reader
	var progressWriter *ProgressWriter
	if progress {
		progressWriter = NewProgressWriter(os.Stderr, "Transfer")
		reader = io.TeeReader(stream, io.MultiWriter(hasher, progressWriter))
	} else {
		reader = io.TeeReader(stream, hasher)
	}

	if err := sendCmd.Start(); err != nil {
		return "", fmt.Errorf("btrfs send start failed: %w", err)
	}
	if encryptCmd != nil {
		if err := encryptCmd.Start(); err != nil {
			_ = sendCmd.Wait()
			return "", fmt.Errorf("age start failed: %w", err)
		}
	}

	destChecksum, receiveErr := dest.Receive(ctx, outfile, reader)

	sendErr := sendCmd.Wait()
	var encryptErr error
	if encryptCmd != nil {
		encryptErr = encryptCmd.Wait()
	}

	if receiveErr != nil {
		return "", receiveErr
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

	sentChecksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if !strings.EqualFold(destChecksum, sentChecksum) {
		return "", fmt.Errorf("checksum mismatch: sent=%s stored=%s", sentChecksum, destChecksum)
	}

	if verbose {
		fmt.Printf("→ Checksum validation passed\n")
	}

	ok = true
	return sentChecksum, nil
}

// listBackups returns the backups belonging to vol, oldest first.
func listBackups(ctx context.Context, cfg *Config, dest destination, vol *Volume) ([]backup, error) {
	names, err := dest.List(ctx)
	if err != nil {
		return nil, err
	}

	suffix := regexp.QuoteMeta(backupFileSuffix(cfg))
	namePattern := fmt.Sprintf(`^%s-(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})\.(full|inc)%s$`, regexp.QuoteMeta(vol.Name), suffix)
	re := regexp.MustCompile(namePattern)

	var backups []backup
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		match := re.FindStringSubmatch(name)
		if len(match) != 3 {
			continue
		}

		ts, err := time.Parse(snapshotTimestampFormat, match[1])
		if err != nil {
			continue
		}

		backups = append(backups, backup{
			Name:      name,
			Timestamp: ts,
			Kind:      match[2],
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.Before(backups[j].Timestamp)
	})

	return backups, nil
}

func backupForTimestamp(backups []backup, ts time.Time) bool {
	for _, b := range backups {
		if b.Timestamp.Equal(ts) {
			return true
		}
	}
	return false
}

func latestFullBackup(backups []backup) *backup {
	for i := len(backups) - 1; i >= 0; i-- {
		if backups[i].Kind == "full" {
			b := backups[i]
			return &b
		}
	}
	return nil
}

func countIncrementalsSince(backups []backup, since time.Time) int {
	count := 0
	for _, b := range backups {
		if b.Kind == "inc" && b.Timestamp.After(since) {
			count++
		}
	}
	return count
}

func needsFullBackup(ctx context.Context, cfg *Config, dest destination, vol *Volume, oldSnap string, currentTime time.Time) bool {
	if oldSnap == "" {
		return true
	}

	backups, err := listBackups(ctx, cfg, dest, vol)
	if err != nil {
		errLog.Printf("Error retrieving backups: %v", err)
		return true
	}

	if len(backups) == 0 {
		if verbose {
			errLog.Println("Destination has no backups")
		}
		return true
	}

	oldSnapTime, err := extractSnapshotTimestamp(oldSnap)
	if err != nil {
		return true
	}

	if !backupForTimestamp(backups, oldSnapTime) {
		if verbose {
			fmt.Printf("→ Destination missing backup for snapshot %s\n", oldSnapTime.Format(snapshotTimestampFormat))
		}
		return true
	}

	lastFull := latestFullBackup(backups)
	if lastFull == nil {
		if verbose {
			errLog.Println("Destination missing full backup")
		}
		return true
	}

	if cfg.MaxAgeDays > 0 {
		if currentTime.Sub(lastFull.Timestamp) >= time.Duration(cfg.MaxAgeDays)*24*time.Hour {
			if verbose {
				errLog.Printf("→ Last full backup is older than %d days", cfg.MaxAgeDays)
			}
			return true
		}
	}

	if cfg.MaxIncrementals > 0 {
		incCount := countIncrementalsSince(backups, lastFull.Timestamp)
		if incCount >= cfg.MaxIncrementals {
			if verbose {
				errLog.Printf("→ Destination has %d incrementals since last full (limit %d)", incCount, cfg.MaxIncrementals)
			}
			return true
		}
	}

	return false
}

func cleanupOldBackups(ctx context.Context, cfg *Config, dest destination, vol *Volume, newBackup *backup) error {
	backups, err := listBackups(ctx, cfg, dest, vol)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
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

	fullBackups := []backup{}
	for _, b := range backups {
		if b.Kind == "full" {
			fullBackups = append(fullBackups, b)
		}
	}

	if len(fullBackups) < 1 {
		return nil
	}

	lastFull := fullBackups[len(fullBackups)-1]

	var toDelete []backup
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

	var names []string
	for _, b := range toDelete {
		names = append(names, b.Name, b.Name+".sha256")
		if verbose {
			fmt.Printf("→ Deleting: %s\n", b.Name)
		}
	}

	if err := dest.Remove(ctx, names...); err != nil {
		return fmt.Errorf("failed to delete old backups: %w", err)
	}

	return nil
}
