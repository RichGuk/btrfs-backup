package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureStdout swaps os.Stdout for a pipe while fn runs, so tests can assert on
// output written with fmt.Printf.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}

	previous := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = previous }()

	// Drain concurrently, or a preview larger than the pipe buffer would block.
	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		captured <- buf.String()
	}()

	fn()

	writer.Close()
	output := <-captured
	reader.Close()

	return output
}

// withPromptAnswers makes prompts behave as though someone were sat at a
// terminal typing the given answers, one per question. These are package
// globals, so tests using it cannot run in parallel.
func withPromptAnswers(t *testing.T, answers ...string) {
	t.Helper()

	previousInteractive, previousReader := isInteractive, promptReader
	t.Cleanup(func() {
		isInteractive, promptReader = previousInteractive, previousReader
	})

	isInteractive = func() bool { return true }

	var typed strings.Builder
	for _, answer := range answers {
		typed.WriteString(answer + "\n")
	}
	promptReader = bufio.NewReader(strings.NewReader(typed.String()))
}

// withoutTerminal makes prompts behave as they do under cron: there is nobody
// to ask.
func withoutTerminal(t *testing.T) {
	t.Helper()

	previous := isInteractive
	t.Cleanup(func() { isInteractive = previous })

	isInteractive = func() bool { return false }
}

// withDryRun turns on dry-run mode for the duration of a test.
func withDryRun(t *testing.T) {
	t.Helper()

	previous := dryRun
	t.Cleanup(func() { dryRun = previous })

	dryRun = true
}

// mustParseTime parses a snapshot-format timestamp, so tests can pin "now"
// instead of depending on the wall clock.
func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()

	ts, err := time.Parse(snapshotTimestampFormat, value)
	if err != nil {
		t.Fatalf("parsing %q: %v", value, err)
	}
	return ts
}

func setupTestEnv(t *testing.T) (binDir, remoteDir string) {
	t.Helper()
	binDir, remoteDir = setupTestBins(t)
	t.Setenv("PATH", fmt.Sprintf("%s:%s", binDir, os.Getenv("PATH")))
	return binDir, remoteDir
}

func setupTestBins(t *testing.T) (binDir, remoteDir string) {
	t.Helper()

	tempRoot := t.TempDir()

	binDir = filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("creating bin dir: %v", err)
	}

	remoteDir = filepath.Join(tempRoot, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("creating remote dir: %v", err)
	}

	writeExecutable(t, binDir, "btrfs", btrfsStubScript)
	writeExecutable(t, binDir, "ssh", sshStubScript)
	writeExecutable(t, binDir, "age", ageStubScript)

	return binDir, remoteDir
}

func writeExecutable(t *testing.T, dir, name, script string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing script %s: %v", name, err)
	}
}

const btrfsStubScript = `#!/bin/sh
set -e
log="${BTRFS_LOG:-}"

case "$1" in
send)
	shift
	if [ "${BTRFS_FAIL_SEND:-0}" -ne 0 ]; then
		exit 1
	fi
	if [ "${1:-}" = "-p" ]; then
		old="$2"
		new="$3"
		if [ -n "$log" ]; then
			printf "send -p %s %s\n" "$old" "$new" >> "$log"
		fi
		cat "$new"
		exit 0
	fi

	new="$1"
	if [ -n "$log" ]; then
		printf "send %s\n" "$new" >> "$log"
	fi
	cat "$new"
	exit 0
	;;
subvolume)
	if [ "$2" = "snapshot" ]; then
		shift 2
		readonly=""
		if [ "${1:-}" = "-r" ]; then
			readonly="-r "
			shift
		fi
		src="$1"
		dest="$2"
		if [ -n "$log" ]; then
			printf "snapshot %s%s %s\n" "$readonly" "$src" "$dest" >> "$log"
		fi
		if [ "${BTRFS_FAIL_SNAPSHOT:-0}" -ne 0 ]; then
			exit 1
		fi
		rm -rf "$dest"
		mkdir -p "$dest"
		exit 0
	fi

	if [ "$2" = "create" ]; then
		target="$3"
		if [ -n "$log" ]; then
			printf "create %s\n" "$target" >> "$log"
		fi
		if [ "${BTRFS_FAIL_CREATE:-0}" -ne 0 ]; then
			exit 1
		fi
		mkdir -p "$target"
		exit 0
	fi

	if [ "$2" = "delete" ]; then
		target="$3"
		if [ -n "$log" ]; then
			printf "delete %s\n" "$target" >> "$log"
		fi
		if [ "${BTRFS_FAIL_DELETE:-0}" -ne 0 ]; then
			exit 1
		fi
		rm -rf "$target"
		exit 0
	fi

	if [ "$2" = "list" ]; then
		if [ "${BTRFS_FAIL_LIST:-0}" -ne 0 ]; then
			exit 1
		fi
		exit 0
	fi
	;;
esac

echo "unexpected btrfs invocation: $@" >&2
exit 1
`

const sshStubScript = `#!/bin/sh
set -e
log="${SSH_LOG:-}"
cmd="${@: -1}"

if [ -n "$log" ]; then
	printf "%s\n" "$cmd" >> "$log"
fi

if printf "%s" "$cmd" | grep -q "^tee .* | sha256sum"; then
	if [ "${SSH_FAIL_CAT:-0}" -ne 0 ]; then
		exit 1
	fi
	sh -c "$cmd"
	exit 0
fi

if printf "%s" "$cmd" | grep -q "^cat > "; then
	if [ "${SSH_FAIL_CAT:-0}" -ne 0 ]; then
		exit 1
	fi
	sh -c "$cmd"
	exit 0
fi

if ! sh -c "$cmd"; then
	exit $?
fi

exit 0
`

const ageStubScript = `#!/bin/sh
set -e
log="${AGE_LOG:-}"
if [ -n "$log" ]; then
	printf "age %s\n" "$*" >> "$log"
fi

if [ "${AGE_FAIL:-0}" -ne 0 ]; then
	exit 1
fi

if [ -n "${AGE_PREFIX:-}" ]; then
	printf "%s" "$AGE_PREFIX"
fi

cat
`
