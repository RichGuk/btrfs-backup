# btrfs-backup

**⚠️ Alpha Software Alert**: This is very much alpha software built for my
personal backup needs. I'm sharing it in case it's useful to others, but it's
definitely tailored to my specific setup and workflow. Use at your own risk!

Also, this is part of my journey learning Go, so expect some rough edges and
"I'm still figuring this out" moments. Lots of vibing. Feedback welcome, but no
promises on feature requests. 😊

## What It Does

`btrfs-backup` creates read-only BTRFS snapshots of your subvolumes and writes
them either to a remote host via SSH or to a directory on the local machine. It
handles both full and incremental backups, with optional age encryption, SHA256
checksum verification, and automatic cleanup of old backups.

Locally, I only ever keep one snapshot. On the remote server, you can configure
your retention. I keep a week's worth of incremental backups and do a full backup
once a week. I never keep more than this, and the tool is built around this
concept.

Think of it as my opinionated take on "how do I back up my BTRFS system to a
remote server without thinking about it too much."

## Why This Exists

I wanted:
- Automated BTRFS backups to a remote server
- To back up to a non-BTRFS file system, so needed to save raw images
- No need to maintain hourly snapshots locally (I use snapper for that)
- Incremental backups to save bandwidth and storage
- Encryption support (because sometimes the target is offsite)
- A learning project to get better at Go

There are other tools out there, but I wanted something simple that fit my
specific workflow, and building it myself seemed like a good way to learn Go.

## Installation

### Prerequisites

- BTRFS filesystem (obviously)
- SSH access to remote backup host
- `age` binary if using encryption ([https://age-encryption.org](https://age-encryption.org))
- Root access (needed for BTRFS operations)

### Building

```bash
git clone https://github.com/RichGuk/btrfs-backup.git
cd btrfs-backup
go build
sudo cp btrfs-backup /usr/local/bin/
```

## Configuration

Create `/etc/btrfs-backup.yaml` with your settings:

```yaml
# SSH configuration
ssh_key: /root/.ssh/id_ed25519
remote_host: backup@backup-server.example.com
dest: /data/backups

# Backup policy
max_age_days: 7          # Force full backup after this many days
max_incrementals: 5      # Force full backup after this many incrementals

# Optional encryption (recommended!)
encryption_key: age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p

# Volumes to backup
volumes:
  - name: root
    src: /                              # Source subvolume
    snapdir: /.snapshots/btrfs-backup   # Where to store local snapshots
  - name: home
    src: /home
    snapdir: /home/.snapshots/btrfs-backup
```

### Local Destinations

`dest` is just a directory. Leave `remote_host` and `ssh_key` out and it's
treated as a path on this machine — an external drive, a NAS mount, another
filesystem, whatever:

```yaml
dest: /mnt/backup-drive

max_age_days: 7
max_incrementals: 5

volumes:
  - name: root
    src: /
    snapdir: /.snapshots/btrfs-backup
```

Everything else behaves identically: same filenames, same `.sha256` sidecars,
same retention rules, same optional age encryption. The only difference is how
the verification checksum is produced. Over SSH, the remote end hashes the
stream as it lands, which proves the bytes survived the network. Locally there
is no network, so the finished file is read back off disk and hashed — the
re-read is cheap on local storage in a way it would never be over SSH.

Note that a local destination gets raw `btrfs send` images, exactly like a
remote one — it does **not** `btrfs receive` into a subvolume, even when the
destination is itself BTRFS. That's deliberate: storing images is what lets the
destination be an ext4 USB drive, a NAS mount, or anything else. If you want
real subvolumes on a BTRFS target, snapper and btrbk already do that well.

Two things to know:

- `dest` was called `remote_dest` before local destinations existed. That name
  still works, but set one or the other, not both.
- Setting `ssh_key` without `remote_host` is an error, rather than silently
  writing to a local path you didn't mean.

If `dest` doesn't exist, it's created (`mkdir -p`), and it's checked for
writability before any snapshot is sent.

### Generating an age Key

```bash
age-keygen -o backup-key.txt
# Use the public key (age1...) in your config
# Store the private key securely on your restore machine
```

## Usage

### Manual Backup

```bash
# Normal run
sudo btrfs-backup

# Dry run (implies verbose, shows what would happen)
sudo btrfs-backup -n

# Verbose output (detailed logging)
sudo btrfs-backup -v

# Very verbose dry run (includes command previews)
sudo btrfs-backup -vv -n

# Custom config location
sudo btrfs-backup -config /path/to/config.yaml
```

### Automated Backups with systemd

Create `/etc/systemd/system/btrfs-backup.service`:

```ini
[Unit]
Description=BTRFS Backup
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/btrfs-backup
```

Create `/etc/systemd/system/btrfs-backup.timer`:

```ini
[Unit]
Description=BTRFS Backup Timer

[Timer]
OnCalendar=daily
Persistent=true

[Install]
WantedBy=timers.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now btrfs-backup.timer
```

Check status:

```bash
sudo systemctl status btrfs-backup.timer
sudo journalctl -u btrfs-backup.service -f
```

## How It Works

### Full vs Incremental Backups

The tool decides whether to do a full or incremental backup based on:
- **Full backup needed if**:
  - No previous remote backups exist
  - Previous snapshot no longer exists locally
  - Last full backup is older than `max_age_days`
  - More than `max_incrementals` incremental backups since last full
- **Otherwise**: Creates an incremental backup from the previous snapshot

### Backup Cleanup Logic

To keep storage manageable while maintaining restore capability:
- Keeps the **latest full backup chain** (full backup + all its incrementals)
- Deletes everything older than the latest full backup
- Only runs cleanup after successfully creating and verifying a new full backup
- This approach assumes the verified full backup is reliable

### Backup Workflow

1. **Create snapshot**: `btrfs subvolume snapshot -r <src> <snapdir>/<timestamp>`
2. **Determine backup type**: Check if full or incremental is needed
3. **Send to the destination**:
   - `btrfs send` (with `-p` for incremental)
   - Optional `age` encryption
   - Stream over SSH, or write straight to a local directory
4. **Verify**: Calculate and verify SHA256 checksum
5. **Cleanup**: Delete old snapshots locally, old backups at the destination (if full backup)

Every transfer is staged as `<filename>.tmp` and only renamed to its real name
once the checksum matches, so an interrupted run can never leave behind a file
that looks like a usable backup.

## Backup Naming Convention

```
<volume>-<timestamp>.<type>.<ext>
```

Examples:
- `root-2024-05-12_11-30-45.full.btrfs`
- `root-2024-05-12_11-30-45.full.btrfs.age` (encrypted)
- `home-2024-05-13_03-00-00.inc.btrfs.age`
- `root-2024-05-14_03-00-00.inc.btrfs`

Checksums are stored as `<filename>.sha256`.

## Restoring Backups

Restoration is currently a manual process. On your restore machine:

1. **Decrypt if needed**:
   ```bash
   age -d -i backup-key.txt backup.btrfs.age > backup.btrfs
   ```

2. **Receive full backup**:
   ```bash
   sudo btrfs receive /mnt/restore < root-2024-05-12_11-30-45.full.btrfs
   ```

3. **Apply incrementals in order**:
   ```bash
   sudo btrfs receive /mnt/restore < root-2024-05-13_03-00-00.inc.btrfs
   sudo btrfs receive /mnt/restore < root-2024-05-14_03-00-00.inc.btrfs
   ```

## Testing

Run the test suite:

```bash
go test -v
```

## Limitations & Known Issues

- **No automatic restore**: You'll need to manually restore backups (for now)
- **SSH or local paths only**: No cloud storage backends
- **Linux only**: Requires BTRFS and Linux-specific syscalls
- **Root required**: Needs root for BTRFS operations and lock file location
- **Lock file path**: Hardcoded to `/var/run/btrfs-backup.lock`
- **No progress indicators**: Large backups just... happen. Be patient.
- **Alpha software**: Did I mention this is alpha? Because it is.

## Contributing

This is a personal project, so I'm not actively soliciting contributions. That
said, if you find bugs or have suggestions, feel free to open an issue.

If you fork it and make improvements, that's awesome! Let me know and I'll link
to your fork.

## License

MIT License - See LICENSE file for details.

Use at your own risk. No warranty. If this deletes all your data, that's on
you. (But seriously, test with non-critical data first!)
