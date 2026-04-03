#!/usr/bin/env bash
# Build a Firecracker rootfs (ext4) from a Docker image.
# Progress markers (PROGRESS:N:msg) are parsed by the dashboard handler for live updates.
set -e

IMAGE="${1:?usage: $0 <docker-image> <public-key.pub> [output.ext4]}"
PUBKEY="${2:?usage: $0 <docker-image> <public-key.pub> [output.ext4]}"
OUTPUT="${3:-}"

if [ ! -f "$PUBKEY" ]; then
  echo "ERROR: Public key file not found: $PUBKEY" >&2
  echo "Run: sistemo ssh-key" >&2
  exit 1
fi

if [ -z "$OUTPUT" ]; then
  OUTPUT="$(echo "$IMAGE" | sed 's/[\/:]/-/g').rootfs.ext4"
fi

if [ "$(id -u)" != "0" ]; then
  echo "ERROR: Requires root (for mount/chroot). Run: sudo sistemo image build $IMAGE" >&2
  exit 1
fi

# --- Mount isolation ---
# Re-exec in a private mount namespace so our mount/umount operations
# (loop devices, chroot /proc /sys /dev) don't propagate to the host
# via shared mount propagation and nuke the system's /proc /sys /dev.
if [ -z "$_SISTEMO_MOUNT_ISOLATED" ]; then
  export _SISTEMO_MOUNT_ISOLATED=1
  exec unshare --mount --propagation private "$0" "$@"
fi

# --- Pre-flight checks ---

# Check Docker daemon is running
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: Docker daemon not running. Start Docker first." >&2
  exit 1
fi

# Accept both ROOTFS_SIZE_MB (from dashboard) and ROOTFS_MB (legacy)
ROOTFS_MB="${ROOTFS_SIZE_MB:-${ROOTFS_MB:-5120}}"
# Validate numeric to prevent bash arithmetic injection (e.g. ROOTFS_SIZE_MB='a[$(cmd)]')
if ! [[ "$ROOTFS_MB" =~ ^[0-9]+$ ]]; then
  echo "ERROR: ROOTFS_SIZE_MB must be a positive integer, got: $ROOTFS_MB" >&2
  exit 1
fi

# Use SISTEMO_BUILD_TMPDIR if set (disk-backed), else system default (/tmp)
if [ -n "$SISTEMO_BUILD_TMPDIR" ] && [ -d "$SISTEMO_BUILD_TMPDIR" ]; then
  TMPDIR=$(mktemp -d -p "$SISTEMO_BUILD_TMPDIR")
else
  TMPDIR=$(mktemp -d)
fi

# Check disk space: need ROOTFS_MB for the image + ~2x for temp extraction
OUTPUT_DIR="$(dirname "$(realpath -m "$OUTPUT")")"
needed_mb=$((ROOTFS_MB * 2))
available_mb=$(df -m "$TMPDIR" 2>/dev/null | awk 'NR==2 {print $4}')
if [ -n "$available_mb" ] && [ "$available_mb" -lt "$needed_mb" ]; then
  echo "ERROR: Need ${needed_mb}MB free in temp dir, only ${available_mb}MB available" >&2
  rm -rf "$TMPDIR"
  exit 1
fi
output_available_mb=$(df -m "$OUTPUT_DIR" 2>/dev/null | awk 'NR==2 {print $4}')
if [ -n "$output_available_mb" ] && [ "$output_available_mb" -lt "$ROOTFS_MB" ]; then
  echo "ERROR: Need ${ROOTFS_MB}MB free for output image, only ${output_available_mb}MB available in $OUTPUT_DIR" >&2
  rm -rf "$TMPDIR"
  exit 1
fi

# Track active mounts for robust cleanup
ACTIVE_MOUNTS=""
CID=""

cleanup() {
  # Unmount in reverse order
  for m in $ACTIVE_MOUNTS; do
    umount "$m" 2>/dev/null || umount -l "$m" 2>/dev/null || true
  done
  # Remove Docker container
  if [ -n "$CID" ]; then
    docker rm -f "$CID" 2>/dev/null || true
  fi
  # Remove temp directory
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

tracked_mount() {
  local type_or_src="$1"
  local target="$2"
  shift 2
  if mount "$type_or_src" "$target" "$@" 2>/dev/null; then
    ACTIVE_MOUNTS="$target $ACTIVE_MOUNTS"
    return 0
  fi
  return 1
}

tracked_umount() {
  local target="$1"
  umount "$target" 2>/dev/null || umount -l "$target" 2>/dev/null || true
  ACTIVE_MOUNTS="${ACTIVE_MOUNTS//$target /}"
}

# --- Phase 1: Docker export ---

echo "PROGRESS:5:Pulling image..."
if ! docker pull "$IMAGE" 2>&1; then
  echo "ERROR: Failed to pull Docker image: $IMAGE" >&2
  exit 1
fi

echo "PROGRESS:15:Creating container..."
CID=$(docker create "$IMAGE" /bin/true 2>/dev/null) || {
  echo "ERROR: docker create failed for image: $IMAGE" >&2
  exit 1
}

echo "PROGRESS:25:Exporting filesystem..."
docker export "$CID" > "$TMPDIR/export.tar" || {
  echo "ERROR: docker export failed" >&2
  exit 1
}

echo "PROGRESS:40:Extracting..."
tar -xf "$TMPDIR/export.tar" -C "$TMPDIR" || {
  echo "ERROR: tar extraction failed" >&2
  exit 1
}
rm -f "$TMPDIR/export.tar"
docker rm -f "$CID" 2>/dev/null || true
CID=""

# --- Phase 2: SSH installation ---

echo "PROGRESS:50:Installing SSH server..."

install_ssh() {
  local root="$1"

  if [ -x "$root/usr/sbin/sshd" ]; then
    echo "openssh-server: already installed"
    return 0
  fi

  # Check if image has /bin/sh for chroot
  if [ ! -x "$root/bin/sh" ] && [ ! -L "$root/bin/sh" ]; then
    echo "WARNING:SSH_SKIP:Image has no /bin/sh — cannot install openssh-server" >&2
    echo "WARNING:Terminal and exec will not work. Add openssh-server to your Dockerfile." >&2
    return 1
  fi

  echo "Installing openssh-server..."

  # Mount /proc /sys /dev for chroot — tracked for cleanup
  tracked_mount -t proc proc "$root/proc"
  tracked_mount -t sysfs sys "$root/sys"
  tracked_mount --bind /dev "$root/dev"

  # Copy resolv.conf for DNS inside chroot
  cp /etc/resolv.conf "$root/etc/resolv.conf" 2>/dev/null || true

  local ok=0
  if [ -x "$root/usr/bin/apt-get" ] || [ -x "$root/usr/bin/apt" ]; then
    chroot "$root" /bin/sh -c "apt-get update -qq && apt-get install -y -qq openssh-server" 2>&1 && ok=1
  elif [ -x "$root/sbin/apk" ]; then
    chroot "$root" /bin/sh -c "apk add --no-cache openssh-server" 2>&1 && ok=1
  elif [ -x "$root/usr/bin/dnf" ]; then
    chroot "$root" /bin/sh -c "dnf install -y openssh-server" 2>&1 && ok=1
  elif [ -x "$root/usr/bin/yum" ]; then
    chroot "$root" /bin/sh -c "yum install -y openssh-server" 2>&1 && ok=1
  elif [ -x "$root/usr/bin/pacman" ]; then
    chroot "$root" /bin/sh -c "pacman -Sy --noconfirm openssh" 2>&1 && ok=1
  fi

  # Cleanup chroot mounts
  tracked_umount "$root/dev"
  tracked_umount "$root/sys"
  tracked_umount "$root/proc"

  if [ "$ok" = "1" ]; then
    echo "openssh-server: installed"
  else
    echo "WARNING:SSH_INSTALL_FAILED:Could not auto-install openssh-server" >&2
    echo "WARNING:No package manager found or install failed. Terminal may not work." >&2
    echo "WARNING:Add openssh-server to your Dockerfile to enable terminal access." >&2
    return 1
  fi
}

install_ssh "$TMPDIR" || true

# --- Phase 3: SSH configuration ---

echo "PROGRESS:60:Configuring SSH..."

if [ -d "$TMPDIR/etc/ssh" ]; then
  # Generate host keys if missing
  if [ ! -f "$TMPDIR/etc/ssh/ssh_host_ed25519_key" ]; then
    chroot "$TMPDIR" ssh-keygen -A 2>/dev/null || {
      echo "WARNING: ssh-keygen -A failed — host keys not generated" >&2
    }
  fi
  # Allow root login with key
  if [ -f "$TMPDIR/etc/ssh/sshd_config" ]; then
    sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' "$TMPDIR/etc/ssh/sshd_config"
    sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' "$TMPDIR/etc/ssh/sshd_config"
  fi
  mkdir -p "$TMPDIR/run/sshd"
fi

# Inject SSH public key
mkdir -p "$TMPDIR/root/.ssh"
touch "$TMPDIR/root/.ssh/authorized_keys"
chmod 700 "$TMPDIR/root/.ssh"
cat "$PUBKEY" >> "$TMPDIR/root/.ssh/authorized_keys"
chmod 600 "$TMPDIR/root/.ssh/authorized_keys"

# --- Phase 4: Init script injection ---

echo "PROGRESS:70:Injecting init script..."

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INIT_SRC="${SCRIPT_DIR}/vm-init.sh"
if [ ! -f "$INIT_SRC" ]; then
  echo "ERROR: vm-init.sh not found at $INIT_SRC" >&2
  exit 1
fi
cp "$INIT_SRC" "$TMPDIR/init"
chmod 755 "$TMPDIR/init"
mkdir -p "$TMPDIR/sbin"
rm -f "$TMPDIR/sbin/init"
ln -s /init "$TMPDIR/sbin/init"

# --- Phase 5: Fix Docker log symlinks ---

# Docker images symlink logs to /dev/stdout and /dev/stderr for container logging.
# These don't work reliably in VMs because services open files before /dev is fully
# initialized by systemd. Replace with real empty files.
find "$TMPDIR" -type l \( -lname '/dev/stderr' -o -lname '/dev/stdout' -o -lname '/dev/null' \) 2>/dev/null | while read -r link; do
  rm -f "$link"
  touch "$link"
done

# --- Phase 6: Create ext4 image ---

echo "PROGRESS:80:Creating ext4 image ($ROOTFS_MB MB)..."

# Use sparse file (instant creation, only allocates blocks on write)
dd if=/dev/zero of="$OUTPUT" bs=1M count=0 seek="$ROOTFS_MB" 2>/dev/null || {
  echo "ERROR: Failed to create rootfs image (disk full?)" >&2
  rm -f "$OUTPUT"
  exit 1
}

mke2fs -t ext4 -F "$OUTPUT" -q || {
  echo "ERROR: mke2fs failed" >&2
  rm -f "$OUTPUT"
  exit 1
}

MNT=$(mktemp -d)
mount -o loop "$OUTPUT" "$MNT" || {
  echo "ERROR: Failed to mount rootfs image" >&2
  rmdir "$MNT"
  rm -f "$OUTPUT"
  exit 1
}
ACTIVE_MOUNTS="$MNT $ACTIVE_MOUNTS"

echo "PROGRESS:90:Copying files to image..."
# Use glob instead of "cp -a dir/. dest/" — the latter copies the source directory's
# permissions (0700 from mktemp) onto the mount root, which breaks services like nginx
# that run as non-root and need to traverse /.
cp -a "$TMPDIR"/* "$MNT/" || {
  echo "ERROR: Failed to copy files to rootfs (disk full?)" >&2
  tracked_umount "$MNT"
  rmdir "$MNT" 2>/dev/null || true
  rm -f "$OUTPUT"
  exit 1
}
# Copy dotfiles (may legitimately have no matches — ignore errors)
cp -a "$TMPDIR"/.[!.]* "$MNT/" 2>/dev/null || true

tracked_umount "$MNT"
rmdir "$MNT" 2>/dev/null || true

echo "PROGRESS:100:Build complete"
echo "Done: $OUTPUT"
