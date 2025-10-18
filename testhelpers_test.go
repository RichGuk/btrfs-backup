package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func withDryRun(t *testing.T, val bool) {
	t.Helper()
	prev := dryRun
	dryRun = val
	t.Cleanup(func() { dryRun = prev })
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
