#!/bin/sh
# Sistemo VM init — injected into rootfs by sistemo build.
# The kernel ip= boot parameter configures eth0 with the VM's unique bridge IP
# before this script runs. We set up mounts, DNS, and either:
#   - Docker metadata mode: exec the image's ENTRYPOINT/CMD via tini (PID 1)
#   - Systemd mode: exec systemd (for OS images or images without metadata)
export PATH=/usr/sbin:/sbin:/usr/bin:/bin
# NOTE: Do NOT use "set -e" here. This is PID 1 — if ANY command returns
# non-zero, set -e kills the script, the kernel panics, and the VM dies.
# Busybox ash (Alpine) is especially aggressive with set -e in && chains.
# Each critical command handles its own errors explicitly.

# --- Early boot: mounts and device setup ---
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /var/run /var/log /tmp /run/systemd

# Create /dev/stdin, /dev/stdout, /dev/stderr symlinks.
# Docker images (nginx, apache, php-fpm, etc.) symlink log files to /dev/stdout and
# /dev/stderr for container logging. These symlinks don't exist in exported rootfs
# because Docker injects them at runtime. Without them, services fail on boot with
# "No such device or address".
ln -sf /proc/self/fd/0 /dev/stdin  2>/dev/null || true
ln -sf /proc/self/fd/1 /dev/stdout 2>/dev/null || true
ln -sf /proc/self/fd/2 /dev/stderr 2>/dev/null || true
ln -sf /proc/self/fd   /dev/fd     2>/dev/null || true
printf 'nameserver 8.8.8.8\nnameserver 1.1.1.1\n' > /etc/resolv.conf

# Remove policy-rc.d so services auto-start on apt-get install
rm -f /usr/sbin/policy-rc.d

# Disable slow/unnecessary services for faster boot.
for svc in \
  systemd-random-seed.service \
  systemd-timesyncd.service \
  systemd-update-utmp.service \
  apt-daily.timer \
  apt-daily-upgrade.timer \
  e2scrub_all.timer \
  e2scrub_reap.service \
  logrotate.timer \
  sysstat.service \
  fstrim.timer \
  man-db.timer; do
  [ -d /etc/systemd/system ] && ln -sf /dev/null "/etc/systemd/system/$svc" 2>/dev/null || true
done

# Wait for virtio-net device (kernel ip= already configured it, just ensure it's up)
IP=/usr/sbin/ip
[ -x "$IP" ] || IP=/sbin/ip
i=0; while [ $i -lt 25 ]; do
  [ -d /sys/class/net/eth0 ] && break
  sleep 1; i=$((i+1))
done
if [ -d /sys/class/net/eth0 ]; then
  $IP link set eth0 up 2>/dev/null || true
fi

# --- Application startup ---
#
# Mode detection:
#   1. Docker mode (real entrypoint): image has ENTRYPOINT that isn't just a shell
#   2. Docker mode (CMD only): image has CMD but no entrypoint AND no systemd
#   3. Systemd mode: OS images, or images with shell-only CMD that have systemd

SISTEMO_META="/etc/sistemo"
TINI="/etc/sistemo/sistemo-init"
BOOT_MODE="systemd"
HAS_SYSTEMD=0

if [ -x /lib/systemd/systemd ] || [ -x /usr/lib/systemd/systemd ]; then
  HAS_SYSTEMD=1
fi

if [ -f "$SISTEMO_META/.docker-image" ]; then
  if [ -s "$SISTEMO_META/entrypoint" ]; then
    EP=$(head -1 "$SISTEMO_META/entrypoint")
    case "$EP" in
      /bin/sh|/bin/bash|bash|sh)
        # Bare shell entrypoint — use systemd if available, else run the shell
        if [ "$HAS_SYSTEMD" = "0" ]; then
          BOOT_MODE="docker"
        fi
        ;;
      *)
        BOOT_MODE="docker"
        ;;
    esac
  elif [ -s "$SISTEMO_META/cmd" ]; then
    # CMD only, no entrypoint (e.g., alpine with CMD=/bin/sh)
    # Use systemd if available, otherwise run the CMD
    if [ "$HAS_SYSTEMD" = "0" ]; then
      BOOT_MODE="docker"
    fi
  fi
fi

if [ "$BOOT_MODE" = "docker" ]; then
  echo "[sistemo-init] Docker metadata detected, starting in app mode"

  # 0. Mount devpts — required for SSH PTY allocation.
  #    In systemd mode, systemd handles this. In Docker mode we must do it ourselves.
  mount -t devpts devpts /dev/pts 2>/dev/null

  # 1. Set environment variables from image config
  if [ -s "$SISTEMO_META/env" ]; then
    while IFS= read -r line; do
      # Validate format: NAME=value (reject lines without = or with shell metacharacters in key)
      case "$line" in
        [a-zA-Z_]*=*) export "$line" || true ;;
        "") ;;
        *) echo "[sistemo-init] skipping invalid env line: $line" ;;
      esac
    done < "$SISTEMO_META/env"
  fi

  # 2. Set working directory
  if [ -s "$SISTEMO_META/workdir" ]; then
    WDIR=$(cat "$SISTEMO_META/workdir")
    if [ -n "$WDIR" ] && [ -d "$WDIR" ]; then
      cd "$WDIR"
      echo "[sistemo-init] workdir: $WDIR"
    fi
  fi

  # 3. Generate SSH host keys if missing (Alpine build can't generate them
  #    because /dev is unmounted during chroot ssh-keygen -A).
  #    We do this early so keys are ready for both shell and app paths.
  if [ -x /usr/sbin/sshd ] && [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
    ssh-keygen -A 2>/dev/null
    echo "[sistemo-init] generated SSH host keys"
  fi

  # 4. Build command from entrypoint + cmd
  set --
  if [ -s "$SISTEMO_META/entrypoint" ]; then
    while IFS= read -r arg; do
      [ -n "$arg" ] && set -- "$@" "$arg" || true
    done < "$SISTEMO_META/entrypoint"
  fi
  if [ -s "$SISTEMO_META/cmd" ]; then
    while IFS= read -r arg; do
      [ -n "$arg" ] && set -- "$@" "$arg" || true
    done < "$SISTEMO_META/cmd"
  fi

  # 5. Switch user if specified and exists in the image
  APP_USER=""
  if [ -s "$SISTEMO_META/user" ]; then
    APP_USER=$(cat "$SISTEMO_META/user")
    if [ -n "$APP_USER" ] && [ "$APP_USER" != "root" ] && [ "$APP_USER" != "0" ]; then
      if id "$APP_USER" >/dev/null 2>&1; then
        echo "[sistemo-init] running as user: $APP_USER"
      else
        echo "[sistemo-init] WARNING: user '$APP_USER' not found, running as root"
        APP_USER=""
      fi
    else
      APP_USER=""
    fi
  fi

  # 6. Detect if the command is an interactive shell (sh, bash, ash, etc.)
  #    Interactive shells exit immediately when stdin is not a terminal (Firecracker
  #    console is not interactive). For shell images (Alpine, BusyBox), we keep the
  #    VM alive by running sshd in the foreground instead.
  CMD_FIRST="$1"
  IS_SHELL=0
  case "$CMD_FIRST" in
    /bin/sh|/bin/bash|/bin/ash|sh|bash|ash) IS_SHELL=1 ;;
  esac

  if [ "$IS_SHELL" = "1" ]; then
    echo "[sistemo-init] shell image detected ($CMD_FIRST), running sshd as primary service"
    if [ -x /usr/sbin/sshd ]; then
      # Generate host keys if missing
      if [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
        ssh-keygen -A 2>/dev/null
        echo "[sistemo-init] generated SSH host keys"
      fi
      mkdir -p /run/sshd /var/empty
      # Ensure sshd privilege separation user exists
      if ! id sshd >/dev/null 2>&1; then
        echo "[sistemo-init] creating sshd user for privilege separation"
        adduser -D -H -s /sbin/nologin -g sshd sshd 2>/dev/null || \
          adduser -S -D -H -s /sbin/nologin -g sshd sshd 2>/dev/null || \
          echo "sshd:x:74:74:sshd:/var/empty:/sbin/nologin" >> /etc/passwd
      fi
      # Ensure root login is allowed with keys
      if [ -f /etc/ssh/sshd_config ]; then
        sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config 2>/dev/null
      fi
      echo "[sistemo-init] starting sshd in foreground"
      # Run sshd in foreground via tini (keeps PID 1 alive, user connects via terminal)
      if [ -x "$TINI" ]; then
        exec "$TINI" -- /usr/sbin/sshd -D -e -o "ListenAddress=0.0.0.0"
      fi
      exec /usr/sbin/sshd -D -e -o "ListenAddress=0.0.0.0"
    else
      # No sshd available — keep VM alive with a sleep loop
      echo "[sistemo-init] WARNING: no sshd installed, VM will stay running but terminal won't work"
      echo "[sistemo-init] Add openssh-server to your image for terminal access"
      while true; do sleep 3600; done
    fi
  fi

  # 7. Start sshd in background for app images (terminal/exec still works alongside the app).
  #    This is NOT done for shell images — step 6 already exec'd sshd in foreground above.
  if [ -x /usr/sbin/sshd ]; then
    mkdir -p /run/sshd
    /usr/sbin/sshd -o "ListenAddress=0.0.0.0" 2>/dev/null &
    echo "[sistemo-init] sshd started (background)"
  fi

  echo "[sistemo-init] exec: $*"

  # 8. Exec the application via tini (proper PID 1: signal forwarding + zombie reaping)
  #    When running as a different user, we use su with -- to prevent argument injection.
  #    Each argument is individually quoted to prevent shell metacharacter expansion.
  _quote_args() {
    local result=""
    for arg in "$@"; do
      # Escape single quotes within the argument, then wrap in single quotes
      arg="'$(printf '%s' "$arg" | sed "s/'/'\\\\''/g")'"
      result="$result $arg"
    done
    echo "$result"
  }

  if [ -x "$TINI" ]; then
    if [ -n "$APP_USER" ]; then
      exec "$TINI" -- su -s /bin/sh "$APP_USER" -c "exec $(_quote_args "$@")"
    fi
    exec "$TINI" -- "$@"
  else
    if [ -n "$APP_USER" ]; then
      exec su -s /bin/sh "$APP_USER" -c "exec $(_quote_args "$@")"
    fi
    exec "$@"
  fi
fi

# --- Systemd mode (OS images, no Docker metadata, or shell CMD with systemd) ---
echo "[sistemo-init] booting systemd"
exec /lib/systemd/systemd --log-target=console --log-level=warning 2>/dev/null || exec /usr/lib/systemd/systemd --log-target=console --log-level=warning
