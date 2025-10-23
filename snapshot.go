package main

import (
	"context"
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
