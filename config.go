package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Volume struct {
	Name    string `yaml:"name"`
	Src     string `yaml:"src"`
	SnapDir string `yaml:"snapdir"`
}

type Config struct {
	SSHKey     string `yaml:"ssh_key"`
	RemoteHost string `yaml:"remote_host"`

	// Dest is the directory backups are written to. With RemoteHost set it is a
	// path on that host, otherwise a path on this machine's filesystem.
	Dest string `yaml:"dest"`
	// RemoteDest is the original name for Dest, kept so existing configs work.
	RemoteDest string `yaml:"remote_dest"`

	MaxAgeDays      int      `yaml:"max_age_days"`
	MaxIncrementals int      `yaml:"max_incrementals"`
	EncryptionKey   string   `yaml:"encryption_key"`
	Volumes         []Volume `yaml:"volumes"`
}

// IsLocal reports whether backups are written to this machine's filesystem
// rather than shipped to a remote host over SSH.
func (c *Config) IsLocal() bool {
	return c.RemoteHost == ""
}

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
	cfg.EncryptionKey = strings.TrimSpace(cfg.EncryptionKey)
	cfg.RemoteHost = strings.TrimSpace(cfg.RemoteHost)
	cfg.Dest = strings.TrimSpace(cfg.Dest)
	cfg.RemoteDest = strings.TrimSpace(cfg.RemoteDest)

	if cfg.Dest != "" && cfg.RemoteDest != "" {
		return nil, fmt.Errorf("set either dest or remote_dest, not both")
	}
	if cfg.Dest == "" {
		cfg.Dest = cfg.RemoteDest
	}
	if cfg.Dest == "" {
		return nil, fmt.Errorf("no backup destination configured: set dest")
	}

	if cfg.IsLocal() && cfg.SSHKey != "" {
		return nil, fmt.Errorf("ssh_key is set but remote_host is not: remove ssh_key for a local destination")
	}

	return &cfg, nil
}
