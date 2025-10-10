package main

import (
	"gopkg.in/yaml.v3"
	"os"
)

type Volume struct {
	Name    string `yaml:"name"`
	Src     string `yaml:"src"`
	SnapDir string `yaml:"snapdir"`
}

type Config struct {
	SSHKey     string   `yaml:"ssh_key"`
	RemoteHost string   `yaml:"remote_host"`
	RemoteDest string   `yaml:"remote_dest"`
	MaxAgeDays int      `yaml:"max_age_days"`
	Volumes    []Volume `yaml:"volumes"`
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
	return &cfg, nil
}
