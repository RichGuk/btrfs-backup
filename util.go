package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const snapshotTimestampFormat = "2006-01-02_15-04-05"

var snapshotTimestampRegexp = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})`)

func extractSnapshotTimestamp(path string) (time.Time, error) {
	base := filepath.Base(path)
	match := snapshotTimestampRegexp.FindStringSubmatch(base)
	if len(match) < 2 {
		return time.Time{}, fmt.Errorf("unable to extract timestamp from %s", base)
	}

	t, err := time.Parse(snapshotTimestampFormat, match[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to parse timestamp %s: %w", match[1], err)
	}

	return t, nil
}

func buildSSHArgs(cfg *Config, remoteCmd string, extraOpts ...string) []string {
	sshArgs := []string{}
	if cfg.SSHKey != "" {
		sshArgs = append(sshArgs, "-i", cfg.SSHKey)
	}
	sshArgs = append(sshArgs, extraOpts...)
	sshArgs = append(sshArgs, cfg.RemoteHost, remoteCmd)

	return sshArgs
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
