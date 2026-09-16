package main

import (
	"context"
	"errors"
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

// ensureSnapDir makes sure vol.SnapDir is there to snapshot into. A snapshot
// directory is usually a subvolume of its own, so that it stays out of the
// snapshots taken of its parent, but a plain directory works too.
func ensureSnapDir(ctx context.Context, vol *Volume) error {
	info, err := os.Stat(vol.SnapDir)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("snapshot directory %s exists but is not a directory", vol.SnapDir)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("failed to access snapshot directory %s: %w", vol.SnapDir, err)
	}

	if dryRun {
		fmt.Printf("[DRY-RUN] Snapshot directory %s for volume %s does not exist, would offer to create it\n", vol.SnapDir, vol.Name)
		return nil
	}

	kind, err := askChoice(
		fmt.Sprintf("Snapshot directory %s for volume %s does not exist. Create it as:", vol.SnapDir, vol.Name),
		[]choice{
			{Key: "s", Label: "btrfs subvolume (recommended)"},
			{Key: "d", Label: "plain directory"},
			{Key: "n", Label: "do not create it"},
		},
		"s",
	)
	if errors.Is(err, errNotInteractive) {
		return fmt.Errorf(
			"snapshot directory %s does not exist: create it (btrfs subvolume create %s), or run btrfs-backup from a terminal to be asked",
			vol.SnapDir, vol.SnapDir,
		)
	}
	if err != nil {
		return err
	}

	switch kind {
	case "s":
		if err := createSubvolume(ctx, vol.SnapDir); err != nil {
			return err
		}
		if verbose {
			fmt.Printf("→ Created snapshot subvolume %s\n", vol.SnapDir)
		}
	case "d":
		if err := os.MkdirAll(vol.SnapDir, 0o755); err != nil {
			return fmt.Errorf("failed to create snapshot directory %s: %w", vol.SnapDir, err)
		}
		if verbose {
			fmt.Printf("→ Created snapshot directory %s\n", vol.SnapDir)
		}
	default:
		return fmt.Errorf("snapshot directory %s does not exist", vol.SnapDir)
	}

	return nil
}

// createSubvolume creates path as a btrfs subvolume, creating any missing
// parent directories first: btrfs only ever creates the leaf.
func createSubvolume(ctx context.Context, path string) error {
	if parent := filepath.Dir(path); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", parent, err)
		}
	}

	cmd := exec.CommandContext(ctx, "btrfs", "subvolume", "create", path)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create btrfs subvolume %s: %w (is %s on a btrfs filesystem?)", path, err, filepath.Dir(path))
	}

	return nil
}

func createSnapshot(ctx context.Context, src, snapDir string, currentTime time.Time) (string, error) {
	name := fmt.Sprintf("btrfs-backup-%s", currentTime.Format("2006-01-02_15-04-05"))
	path := filepath.Join(snapDir, name)

	createCmd := exec.CommandContext(ctx, "btrfs", "subvolume", "snapshot", "-r", src, path)
	createCmd.Stdout = io.Discard
	createCmd.Stderr = os.Stderr

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] %s\n", strings.Join(createCmd.Args, " "))
		}
		return path, nil
	}

	return path, createCmd.Run()
}

func checkBtrfsAccess(ctx context.Context, vol *Volume) error {
	cmd := exec.CommandContext(ctx, "btrfs", "subvolume", "list", vol.Src)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error accessing btrfs subvolume at %s: %v", vol.Src, err)
	}
	return nil
}

func deleteOldSnapshot(ctx context.Context, snapshot string) {
	delCmd := exec.CommandContext(ctx, "btrfs", "subvolume", "delete", snapshot)

	if verbose {
		fmt.Printf("→ Deleting old local snapshot: %s\n", snapshot)
	}

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] %s\n", strings.Join(delCmd.Args, " "))
		}
	} else {
		if err := delCmd.Run(); err != nil {
			errLog.Printf("Error deleting old snapshot: %v", err)
		}
	}
}
