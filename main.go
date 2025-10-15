package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
)

var (
	configPath string
	verbose    bool
	dryRun     bool
)

func main() {
	flag.StringVar(&configPath, "config", "/etc/btrfs-backup.yaml", "Path to config file")
	flag.BoolVar(&verbose, "v", false, "Enable verbose logging")
	flag.BoolVar(&dryRun, "n", false, "Dry run mode (no changes made)")
	flag.Parse()

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	currentTime := time.Now()

	for _, vol := range cfg.Volumes {
		if !dryRun {
			if err := checkBtrfsAccess(&vol); err != nil {
				fmt.Fprintf(os.Stderr, "Error accessing btrfs subvolume: %v\n", err)
				fmt.Println("Make sure the source path is a valid btrfs subvolume and that you have the necessary permissions.")
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
			fmt.Printf("→ Found previous snapshot: %s (age %d days)\n", oldSnap, snapshotAge(oldSnap))
		}

		fullSnapshot := false
		if needsFullBackup(cfg, &vol, oldSnap) {
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
		outfile := fmt.Sprintf("%s-%s.%s.btrfs", vol.Name, currentTime.Format("2006-01-02_15-04-05"), suffix)

		if remoteBackupExists(cfg, outfile) {
			color.Red("⚠️ Backup file %s already exists on remote, skipping volume %s\n", outfile, vol.Name)

			if verbose || dryRun {
				fmt.Print("\n\n")
			}
			continue
		}

		newSnap, err := createSnapshot(vol.Src, vol.SnapDir, currentTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating snapshot: %v\n", err)
			os.Exit(1)
		}

		if err := sendSnapshot(cfg, newSnap, oldSnap, outfile, fullSnapshot); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending snapshot: %v\n", err)
			os.Exit(1)
		}

		if err := moveTmpFile(cfg, outfile); err != nil {
			fmt.Fprintf(os.Stderr, "Error finalizing remote file: %v\n", err)
			os.Exit(1)
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
