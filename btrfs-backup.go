package main

import (
	"flag"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	SSHKey     string `yaml:"ssh_key"`
	RemoteHost string `yaml:"remote_host"`
	RemoteDest string `yaml:"remote_dest"`
	MaxAgeDays int    `yaml:"max_age_days"`
	Volumes    []struct {
		Name    string `yaml:"name"`
		Src     string `yaml:"src"`
		SnapDir string `yaml:"snapdir"`
	} `yaml:"volumes"`
}

var (
	configPath string
	verbose    bool
	dryRun     bool
)

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.MaxAgeDays == 0 {
		cfg.MaxAgeDays = 7
	}
	return &cfg, nil
}

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

	for _, vol := range cfg.Volumes {
		fmt.Printf("Volume: %s, Source: %s, Snapshot Dir: %s\n", vol.Name, vol.Src, vol.SnapDir)
	}
}
