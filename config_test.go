package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `ssh_key: /root/.ssh/id_ed25519
remote_host: backup@example.com
remote_dest: /data/backups
max_age_days: 14
max_incrementals: 5
encryption_key: age1testkey

volumes:
  - name: root
    src: /@
    snapdir: /.snapshots/btrfs-backup
  - name: home
    src: /@home
    snapdir: /home/.snapshots/btrfs-backup
`

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.SSHKey != "/root/.ssh/id_ed25519" {
		t.Errorf("expected SSHKey '/root/.ssh/id_ed25519', got '%s'", cfg.SSHKey)
	}

	if cfg.RemoteHost != "backup@example.com" {
		t.Errorf("expected RemoteHost 'backup@example.com', got '%s'", cfg.RemoteHost)
	}

	if cfg.RemoteDest != "/data/backups" {
		t.Errorf("expected RemoteDest '/data/backups', got '%s'", cfg.RemoteDest)
	}

	if cfg.MaxAgeDays != 14 {
		t.Errorf("expected MaxAgeDays 14, got %d", cfg.MaxAgeDays)
	}

	if cfg.MaxIncrementals != 5 {
		t.Errorf("expected MaxIncrementals 5, got %d", cfg.MaxIncrementals)
	}

	if cfg.EncryptionKey != "age1testkey" {
		t.Errorf("expected EncryptionKey 'age1testkey', got '%s'", cfg.EncryptionKey)
	}

	if len(cfg.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(cfg.Volumes))
	}

	if cfg.Volumes[0].Name != "root" {
		t.Errorf("expected volume[0].Name 'root', got '%s'", cfg.Volumes[0].Name)
	}

	if cfg.Volumes[0].Src != "/@" {
		t.Errorf("expected volume[0].Src '/@', got '%s'", cfg.Volumes[0].Src)
	}

	if cfg.Volumes[0].SnapDir != "/.snapshots/btrfs-backup" {
		t.Errorf("expected volume[0].SnapDir '/.snapshots/btrfs-backup', got '%s'", cfg.Volumes[0].SnapDir)
	}

	if cfg.Volumes[1].Name != "home" {
		t.Errorf("expected volume[1].Name 'home', got '%s'", cfg.Volumes[1].Name)
	}
}

func TestLoadConfigDefaultMaxAgeDays(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `remote_host: backup@example.com
remote_dest: /data/backups

volumes:
  - name: root
    src: /@
    snapdir: /.snapshots
`

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.MaxAgeDays != 7 {
		t.Errorf("expected default MaxAgeDays 7, got %d", cfg.MaxAgeDays)
	}
}

func TestLoadConfigTrimsEncryptionKey(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `remote_host: backup@example.com
remote_dest: /data/backups
encryption_key: "  age1testkey  
"

volumes:
  - name: root
    src: /@
    snapdir: /.snapshots
`

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.EncryptionKey != "age1testkey" {
		t.Errorf("expected EncryptionKey to be trimmed to 'age1testkey', got '%s'", cfg.EncryptionKey)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := loadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected loadConfig to fail with missing file, got nil error")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	invalidYAML := `this is not valid: yaml: content:
  - malformed
    - structure
`

	if err := os.WriteFile(configPath, []byte(invalidYAML), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := loadConfig(configPath)
	if err == nil {
		t.Fatal("expected loadConfig to fail with invalid YAML, got nil error")
	}
}

func TestLoadConfigEmptyVolumes(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `remote_host: backup@example.com
remote_dest: /data/backups
volumes: []
`

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if len(cfg.Volumes) != 0 {
		t.Errorf("expected 0 volumes, got %d", len(cfg.Volumes))
	}
}

func TestLoadConfigMinimalValid(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `remote_host: backup@example.com
remote_dest: /backups
volumes:
  - name: test
    src: /test
    snapdir: /snapshots
`

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.SSHKey != "" {
		t.Errorf("expected empty SSHKey, got '%s'", cfg.SSHKey)
	}

	if cfg.MaxIncrementals != 0 {
		t.Errorf("expected MaxIncrementals 0, got %d", cfg.MaxIncrementals)
	}

	if cfg.EncryptionKey != "" {
		t.Errorf("expected empty EncryptionKey, got '%s'", cfg.EncryptionKey)
	}
}
