#!/usr/bin/env bash
# Build a Firecracker rootfs (ext4) from a Docker image.
set -e

IMAGE="${1:?usage: $0 <docker-image> <public-key.pub> [output.ext4]}"
PUBKEY="${2:?usage: $0 <docker-image> <public-key.pub> [output.ext4]}"
OUTPUT="${3:-}"

if [ ! -f "$PUBKEY" ]; then
  echo "Public key file not found: $PUBKEY" >&2
  echo "Run: sistemo ssh-key" >&2
  exit 1
fi

if [ -z "$OUTPUT" ]; then
  OUTPUT="$(echo "$IMAGE" | sed 's/:/-/g').rootfs.ext4"
fi

if [ "$(id -u)" != "0" ]; then
  echo "Requires root (for mount/chroot). Run: sudo sistemo image build $IMAGE" >&2
  exit 1
fi

ROOTFS_MB="${ROOTFS_MB:-2048}"
TMPDIR=$(mktemp -d)
trap "rm -rf '$TMPDIR'" EXIT

echo "Creating container from $IMAGE..."
CID=$(docker create "$IMAGE" /bin/true)
trap "docker rm -f '$CID' 2>/dev/null; rm -rf '$TMPDIR'" EXIT

echo "Exporting filesystem (this may take a few minutes)..."
docker export "$CID" > "$TMPDIR/export.tar"
echo "Extracting..."
tar -xf "$TMPDIR/export.tar" -C "$TMPDIR"
rm -f "$TMPDIR/export.tar"
docker rm -f "$CID" 2>/dev/null
trap "rm -rf '$TMPDIR'" EXIT

# Detect package manager and install openssh-server if missing
install_ssh() {
  local root="$1"

  if [ -x "$root/usr/sbin/sshd" ]; then
    echo "openssh-server: already installed"
    return 0
  fi

  echo "Installing openssh-server..."

  # Mount /proc /sys /dev for chroot
  mount -t proc proc "$root/proc" 2>/dev/null || true
  mount -t sysfs sys "$root/sys" 2>/dev/null || true
  mount --bind /dev "$root/dev" 2>/dev/null || true

  # Copy resolv.conf for DNS inside chroot
  cp /etc/resolv.conf "$root/etc/resolv.conf" 2>/dev/null || true

  local ok=0
  if [ -x "$root/usr/bin/apt-get" ] || [ -x "$root/usr/bin/apt" ]; then
    # Debian/Ubuntu
    chroot "$root" /bin/sh -c "apt-get update -qq && apt-get install -y -qq openssh-server >/dev/null 2>&1" && ok=1
  elif [ -x "$root/sbin/apk" ]; then
    # Alpine
    chroot "$root" /bin/sh -c "apk add --no-cache openssh-server >/dev/null 2>&1" && ok=1
  elif [ -x "$root/usr/bin/dnf" ]; then
    # Fedora/RHEL
    chroot "$root" /bin/sh -c "dnf install -y openssh-server >/dev/null 2>&1" && ok=1
  elif [ -x "$root/usr/bin/yum" ]; then
    # CentOS/older RHEL
    chroot "$root" /bin/sh -c "yum install -y openssh-server >/dev/null 2>&1" && ok=1
  elif [ -x "$root/usr/bin/pacman" ]; then
    # Arch
    chroot "$root" /bin/sh -c "pacman -Sy --noconfirm openssh >/dev/null 2>&1" && ok=1
  fi

  # Cleanup mounts
  umount "$root/dev" 2>/dev/null || true
  umount "$root/sys" 2>/dev/null || true
  umount "$root/proc" 2>/dev/null || true

  if [ "$ok" = "1" ]; then
    echo "openssh-server: installed"
  else
    echo "Warning: could not auto-install openssh-server. Web terminal may not work." >&2
    echo "  Add openssh-server to your Docker image manually." >&2
  fi
}

install_ssh "$TMPDIR"

# Configure sshd
if [ -d "$TMPDIR/etc/ssh" ]; then
  # Generate host keys if missing
  if [ ! -f "$TMPDIR/etc/ssh/ssh_host_ed25519_key" ]; then
    chroot "$TMPDIR" ssh-keygen -A 2>/dev/null || true
  fi
  # Allow root login with key
  if [ -f "$TMPDIR/etc/ssh/sshd_config" ]; then
    sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' "$TMPDIR/etc/ssh/sshd_config"
    sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' "$TMPDIR/etc/ssh/sshd_config"
  fi
  mkdir -p "$TMPDIR/run/sshd"
fi

echo "Injecting SSH public key..."
mkdir -p "$TMPDIR/root/.ssh"
touch "$TMPDIR/root/.ssh/authorized_keys"
chmod 700 "$TMPDIR/root/.ssh"
cat "$PUBKEY" >> "$TMPDIR/root/.ssh/authorized_keys"
chmod 600 "$TMPDIR/root/.ssh/authorized_keys"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INIT_SRC="${SCRIPT_DIR}/vm-init.sh"
if [ ! -f "$INIT_SRC" ]; then
  echo "Error: vm-init.sh not found at $INIT_SRC" >&2
  exit 1
fi
echo "Injecting /init..."
cp "$INIT_SRC" "$TMPDIR/init"
chmod 755 "$TMPDIR/init"
mkdir -p "$TMPDIR/sbin"
rm -f "$TMPDIR/sbin/init"
ln -s /init "$TMPDIR/sbin/init"

echo "Creating ext4 image ($ROOTFS_MB MB)..."
dd if=/dev/zero of="$OUTPUT" bs=1M count="$ROOTFS_MB" status=progress 2>/dev/null || dd if=/dev/zero of="$OUTPUT" bs=1M count="$ROOTFS_MB"
mke2fs -t ext4 -F "$OUTPUT" -q
MNT=$(mktemp -d)
mount -o loop "$OUTPUT" "$MNT"
cp -a "$TMPDIR"/* "$MNT/"
umount "$MNT"
rmdir "$MNT"

echo "Done: $OUTPUT"
