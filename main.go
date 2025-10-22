package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/fatih/color"
)

var (
	configPath  string
	verbose     bool
	veryVerbose bool
	dryRun      bool
	progress    bool
	force       bool
)

func main() {
	var vv bool
	flag.StringVar(&configPath, "config", "/etc/btrfs-backup.yaml", "Path to config file")
	flag.BoolVar(&verbose, "v", false, "Enable verbose logging")
	flag.BoolVar(&vv, "vv", false, "Enable very verbose logging (includes dry-run commands)")
	flag.BoolVar(&dryRun, "n", false, "Dry run mode (no changes made)")
	flag.BoolVar(&progress, "p", false, "Show transfer progress")
	flag.BoolVar(&progress, "progress", false, "Show transfer progress")
	flag.BoolVar(&force, "f", false, "Force full backup")
	flag.BoolVar(&force, "force", false, "Force full backup")
	flag.Parse()

	if vv {
		verbose = true
		veryVerbose = true
	}

	if dryRun {
		verbose = true
	}

	lockFile, err := os.OpenFile("/var/run/btrfs-backup.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		errLog.Printf("Error opening lock file: %v", err)
		os.Exit(1)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		errLog.Printf("Another instance of btrfs-backup is already running")
		os.Exit(1)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	cfg, err := loadConfig(configPath)
	if err != nil {
		errLog.Printf("Error loading config: %v", err)
		os.Exit(1)
	}

	currentTime := time.Now()

	for _, vol := range cfg.Volumes {
		if !dryRun {
			if err := checkBtrfsAccess(&vol); err != nil {
				errLog.Printf("Error accessing btrfs subvolume: %v", err)
				errLog.Println("Make sure the source path is a valid btrfs subvolume and that you have the necessary permissions.")
				os.Exit(1)
			}
		}
	}

	for _, vol := range cfg.Volumes {
		if verbose {
			fmt.Printf(color.YellowString("Processing volume: %s (src: %s, snapdir: %s)\n"), vol.Name, vol.Src, vol.SnapDir)
		}

		oldSnap, _ := latestSnapshot(vol.SnapDir)

		if oldSnap != "" && verbose {
			fmt.Printf("→ Found previous snapshot: %s\n", oldSnap)
		}

		fullSnapshot := false
		if force {
			fullSnapshot = true
			if verbose {
				fmt.Printf("→ Forcing full backup for %s\n", vol.Name)
			}
		} else if needsFullBackup(cfg, &vol, oldSnap, currentTime) {
			fullSnapshot = true
			if verbose {
				fmt.Printf("→ Doing full backup for %s\n", vol.Name)
			}
		} else if verbose {
			fmt.Printf("→ Doing incremental backup for %s\n", vol.Name)
		}

		suffix := "inc"
		if fullSnapshot {
			suffix = "full"
		}
		outfile := fmt.Sprintf("%s-%s.%s%s", vol.Name, currentTime.Format("2006-01-02_15-04-05"), suffix, remoteFileSuffix(cfg))

		if remoteBackupExists(cfg, outfile) {
			color.Red("⚠️ Backup file %s already exists on remote, skipping volume %s\n", outfile, vol.Name)

			if verbose || dryRun {
				fmt.Print("\n\n")
			}
			continue
		}

		newSnap, err := createSnapshot(vol.Src, vol.SnapDir, currentTime)
		if err != nil {
			errLog.Printf("Error creating snapshot: %v", err)
			os.Exit(1)
		}

		checksum, err := sendSnapshot(cfg, newSnap, oldSnap, outfile, fullSnapshot)
		if err != nil {
			errLog.Printf("Error sending snapshot: %v", err)
			os.Exit(1)
		}

		if err := moveTmpFile(cfg, outfile, checksum); err != nil {
			errLog.Printf("Error finalizing remote file: %v", err)
			os.Exit(1)
		}

		if verbose && checksum != "" {
			fmt.Printf("→ SHA256: %s\n", checksum)
		}

		var newBackupForCleanup *remoteBackup
		if dryRun {
			kind := "inc"
			if fullSnapshot {
				kind = "full"
			}
			newBackupForCleanup = &remoteBackup{
				Name:      outfile,
				Timestamp: currentTime,
				Kind:      kind,
			}
		}
		if err := cleanupOldBackups(cfg, &vol, newBackupForCleanup); err != nil {
			errLog.Printf("Error cleaning up old backups: %v", err)
		}

		if oldSnap != "" && oldSnap != newSnap {
			deleteOldSnapshot(oldSnap)
		}

		if verbose {
			fmt.Printf(color.GreenString("Finished processing: %s"), vol.Name)
		}

		if verbose || dryRun {
			fmt.Print("\n\n")
		}
	}
}
