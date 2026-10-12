package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sshDestination stores backups in a directory on a remote host, reached by
// piping shell commands through ssh.
type sshDestination struct {
	cfg *Config
}

func (d *sshDestination) remotePath(name string) string {
	return filepath.Join(d.cfg.Dest, name)
}

func (d *sshDestination) Path(name string) string {
	return fmt.Sprintf("%s:%s", d.cfg.RemoteHost, d.remotePath(name))
}

func (d *sshDestination) ReceiveStage(name string) string {
	return fmt.Sprintf("| %s", d.preview(d.teeCommand(name)))
}

// preview renders an ssh invocation as copy-pasteable shell. The remote command
// is quoted so it is clear it runs on the far end, rather than reading as though
// its pipeline continues locally.
func (d *sshDestination) preview(remoteCmd string) string {
	args := buildSSHArgs(d.cfg, remoteCmd)
	args[len(args)-1] = shellEscape(remoteCmd)

	return fmt.Sprintf("ssh %s", strings.Join(args, " "))
}

func (d *sshDestination) teeCommand(name string) string {
	// tee writes the file and sha256sum hashes it in the same pass, so the
	// checksum reflects the bytes that actually reached the remote disk.
	//
	// umask 077 because otherwise tee creates the file under whatever umask the
	// remote login shell happens to have, typically leaving a world-readable
	// image of an entire filesystem sat on the backup host. The later mv keeps
	// the mode, so setting it here covers the finished backup too.
	return fmt.Sprintf("umask 077; tee %s | sha256sum", shellEscape(d.remotePath(name+tmpSuffix)))
}

// run executes remoteCmd on the remote host, discarding its output.
func (d *sshDestination) run(ctx context.Context, remoteCmd string) error {
	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(d.cfg, remoteCmd)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// dirState reports whether the destination directory on the remote host is a
// directory ("dir"), something else ("notdir") or absent ("missing"). Every
// outcome is reported on stdout so a non-zero exit means the SSH connection
// itself failed, not that the directory is missing.
func (d *sshDestination) dirState(ctx context.Context) (string, error) {
	dest := shellEscape(d.cfg.Dest)
	remoteCmd := fmt.Sprintf("if [ -d %[1]s ]; then echo dir; elif [ -e %[1]s ]; then echo notdir; else echo missing; fi", dest)

	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(d.cfg, remoteCmd)...)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func (d *sshDestination) Prepare(ctx context.Context) error {
	state, err := d.dirState(ctx)
	if err != nil {
		return fmt.Errorf("failed to access remote host %s: %w (check SSH connectivity and permissions)", d.cfg.RemoteHost, err)
	}

	switch state {
	case "notdir":
		return fmt.Errorf("remote destination %s exists but is not a directory", d.Path(""))
	case "missing":
		create, err := confirmMissingDir("Remote destination", d.Path(""))
		if err != nil {
			return err
		}
		if !create {
			return nil
		}

		if err := d.run(ctx, fmt.Sprintf("mkdir -p -m 700 %s", shellEscape(d.cfg.Dest))); err != nil {
			return fmt.Errorf("failed to create remote destination %s: %w", d.Path(""), err)
		}
		if verbose {
			fmt.Printf("→ Created remote destination %s\n", d.Path(""))
		}
	}

	if verbose {
		fmt.Printf("→ Remote host %s is accessible\n", d.cfg.RemoteHost)
	}

	return nil
}

func (d *sshDestination) Exists(ctx context.Context, name string) bool {
	remoteCmd := fmt.Sprintf("test -f %s && echo exists", shellEscape(d.remotePath(name)))
	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(d.cfg, remoteCmd)...)

	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "exists"
}

func (d *sshDestination) List(ctx context.Context) ([]string, error) {
	remoteCmd := fmt.Sprintf("cd %s && ls -1", shellEscape(d.cfg.Dest))
	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(d.cfg, remoteCmd)...)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing remote backups failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil, nil
	}

	return lines, nil
}

func (d *sshDestination) Receive(ctx context.Context, name string, stream io.Reader) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(d.cfg, d.teeCommand(name))...)
	cmd.Stdin = stream
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("ssh start failed: %w", err)
	}

	output, readErr := io.ReadAll(stdout)

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("ssh failed: %w", err)
	}
	if readErr != nil {
		return "", fmt.Errorf("failed to read remote checksum: %w", readErr)
	}

	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) == 0 {
		return "", fmt.Errorf("unable to parse remote checksum output: %q", string(output))
	}

	return fields[0], nil
}

func (d *sshDestination) Discard(name string) {
	tmpPath := d.remotePath(name + tmpSuffix)
	remoteCmd := fmt.Sprintf("rm -f %s", shellEscape(tmpPath))

	// Deliberately not context-aware: this runs after a cancelled transfer.
	cmd := exec.Command("ssh", buildSSHArgs(d.cfg, remoteCmd)...)

	if err := cmd.Run(); err != nil {
		errLog.Printf("Error during cleanup of remote temp file: %v", err)
	} else if verbose {
		fmt.Printf("→ Cleaned up remote temp file: %s\n", name+tmpSuffix)
	}
}

func (d *sshDestination) Finalise(ctx context.Context, name, checksum string) error {
	moveCmd := fmt.Sprintf(
		"mv %s %s",
		shellEscape(d.remotePath(name+tmpSuffix)),
		shellEscape(d.remotePath(name)),
	)

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] %s\n", d.preview(moveCmd))
		}
		return nil
	}

	if err := d.run(ctx, moveCmd); err != nil {
		return err
	}

	if checksum == "" {
		return nil
	}

	// Its own ssh call, so its own shell and its own umask: the one set in
	// teeCommand does not carry over here.
	checksumCmd := fmt.Sprintf(
		"umask 077; printf '%%s  %%s\\n' %s %s > %s",
		shellEscape(checksum),
		shellEscape(name),
		shellEscape(d.remotePath(name+".sha256")),
	)

	return d.run(ctx, checksumCmd)
}

func (d *sshDestination) Remove(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return nil
	}

	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, shellEscape(d.remotePath(name)))
	}

	remoteCmd := fmt.Sprintf("rm -f %s", strings.Join(paths, " "))

	if dryRun {
		if veryVerbose {
			fmt.Printf("[DRY-RUN] %s\n", d.preview(remoteCmd))
		}
		return nil
	}

	cmd := exec.CommandContext(ctx, "ssh", buildSSHArgs(d.cfg, remoteCmd)...)

	return cmd.Run()
}
