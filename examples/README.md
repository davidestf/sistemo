# Example images for Sistemo

## Build from any Docker image

`sistemo image build` automatically installs `openssh-server` via chroot if the image doesn't have it. Any Docker image works:

```bash
# Build directly from Docker Hub
sudo sistemo image build nginx:latest
sudo sistemo image build alpine:latest
sudo sistemo image build node:20

# Or build a custom image
docker build -t myapp .
sudo sistemo image build myapp

# Deploy
sistemo vm deploy nginx
sistemo vm deploy myapp --name my-vm
```

## Debian with SSH (custom Dockerfile example)

If you prefer to control the SSH setup yourself, see `Dockerfile.debian-sshd`:

```bash
docker build -t sistemo-debian -f examples/Dockerfile.debian-sshd .
sudo sistemo image build sistemo-debian
sistemo vm deploy sistemo-debian
```

## How it works

The build script (`cmd/sistemo/build-rootfs.sh`, embedded in the binary) does:

1. Creates a container from the Docker image and exports the filesystem
2. Auto-installs `openssh-server` via chroot (detects apt/apk/dnf/yum/pacman)
3. Configures sshd (host keys, PermitRootLogin, disables password auth)
4. Injects the SSH public key and `/init` script
5. Creates an ext4 rootfs image

The `/init` script mounts proc/sys/dev, configures the VM network (10.0.0.2 on eth0), then execs systemd or a shell. The network is up before any services start, so SSH works immediately.
