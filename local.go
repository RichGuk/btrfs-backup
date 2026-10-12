package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// localDestination stores backups in a directory on this machine. It is used
// when the config sets a dest but no remote_host.
type localDestination struct {
	dir string
}

func (d *localDestination) Path(name string) string {
	return filepath.Join(d.dir, name)
}

func (d *localDestination) ReceiveStage(name string) string {
	return fmt.Sprintf("> %s", d.Path(name+tmpSuffix))
}

func (d *localDestination) Prepare(ctx context.Context) error {
	info, err := os.Stat(d.dir)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("destination %s exists but is not a directory", d.dir)
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		create, err := confirmMissingDir("Destination", d.dir)
		if err != nil {
			return err
		}
		if !create {
			// Dry run: nothing exists to probe for writability.
			return nil
		}
		if err := os.MkdirAll(d.dir, 0o700); err != nil {
			return fmt.Errorf("failed to create destination %s: %w", d.dir, err)
		}
		if verbose {
			fmt.Printf("→ Created destination %s\n", d.dir)
		}
	default:
		return fmt.Errorf("failed to access destination %s: %w", d.dir, err)
	}

	if dryRun {
		return nil
	}

	// Stat says nothing about whether we can write, and finding out at the end
	// of a multi-gigabyte send is too late.
	probe, err := os.CreateTemp(d.dir, ".btrfs-backup-write-check-*")
	if err != nil {
		return fmt.Errorf("destination %s is not writable: %w", d.dir, err)
	}
	probeName := probe.Name()
	probe.Close()
	if err := os.Remove(probeName); err != nil {
		errLog.Printf("Error removing write check file %s: %v", probeName, err)
	}

	if verbose {
		fmt.Printf("→ Local destination %s is writable\n", d.dir)
	}

	return nil
}

func (d *localDestination) Exists(ctx context.Context, name string) bool {
	info, err := os.Stat(d.Path(name))
	return err == nil && info.Mode().IsRegular()
}

func (d *localDestination) List(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("listing backups in %s failed: %w", d.dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names, nil
}

// Receive writes the stream to the temp file, then reads it back to hash it.
// Hashing on the way in would only confirm what we handed to the kernel; the
// re-read is what confirms the file on disk. It costs a sequential read, which
// is cheap locally in a way it never is over SSH.
func (d *localDestination) Receive(ctx context.Context, name string, stream io.Reader) (string, error) {
	tmpPath := d.Path(name + tmpSuffix)

	// 0600: a backup image is a readable copy of an entire filesystem, so it
	// should not be legible to every account on the machine holding it.
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("failed to create %s: %w", tmpPath, err)
	}

	if _, err := io.Copy(file, stream); err != nil {
		file.Close()
		return "", fmt.Errorf("failed to write %s: %w", tmpPath, err)
	}

	// Sync before hashing so the bytes are committed to the device rather than
	// sitting in a write-back buffer that could still fail.
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf("failed to sync %s: %w", tmpPath, err)
	}

	if err := file.Close(); err != nil {
		return "", fmt.Errorf("failed to close %s: %w", tmpPath, err)
	}

	return sha256File(tmpPath)
}

func (d *localDestination) Discard(name string) {
	tmpPath := d.Path(name + tmpSuffix)

	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errLog.Printf("Error during cleanup of temp file: %v", err)
	} else if verbose {
		fmt.Printf("→ Cleaned up temp file: %s\n", name+tmpSuffix)
	}
}

func (d *localDestination) Finalise(ctx context.Context, name, checksum string) error {
	tmpPath := d.Path(name + tmpSuffix)
	finalPath := d.Path(name)

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] mv %s %s\n", tmpPath, finalPath)
		}
		return nil
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("failed to rename %s: %w", tmpPath, err)
	}

	if checksum == "" {
		return nil
	}

	sidecar := fmt.Sprintf("%s  %s\n", checksum, name)
	if err := os.WriteFile(d.Path(name+".sha256"), []byte(sidecar), 0o600); err != nil {
		return fmt.Errorf("failed to write checksum file for %s: %w", name, err)
	}

	return nil
}

func (d *localDestination) Remove(ctx context.Context, names ...string) error {
	if dryRun {
		if veryVerbose {
			for _, name := range names {
				fmt.Printf("[DRY-RUN] rm -f %s\n", d.Path(name))
			}
		}
		return nil
	}

	for _, name := range names {
		if err := os.Remove(d.Path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to delete %s: %w", d.Path(name), err)
		}
	}

	return nil
}
