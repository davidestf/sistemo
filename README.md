<p align="center">
  <strong>Sistemo</strong><br>
  Self-hosted Firecracker microVMs for your own hardware.
</p>

<p align="center">
  <a href="https://github.com/davidestf/sistemo/actions/workflows/ci.yml"><img src="https://github.com/davidestf/sistemo/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/davidestf/sistemo/releases/latest"><img src="https://img.shields.io/github/v/release/davidestf/sistemo" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License"></a>
  <a href="https://docs.sistemo.io"><img src="https://img.shields.io/badge/docs-docs.sistemo.io-green" alt="Docs"></a>
</p>

---

Run real [Firecracker](https://firecracker-microvm.github.io/) microVMs on your own hardware with one command. No cloud account, no credit card, no Docker.

```bash
curl -sSL https://get.sistemo.io | sh
sudo sistemo up
sistemo vm deploy debian
```

That's it. You have a Debian VM with a browser terminal at `http://localhost:8080`.

---

## Why Sistemo?

| | Sistemo | Proxmox | Docker |
|---|---|---|---|
| **Isolation** | Real VM (KVM) | Real VM (KVM) | Shared kernel |
| **Setup** | One command | ISO install | One command |
| **Resources** | Single binary, ~15 MB | Full OS | Daemon + images |
| **Boot time** | < 15 seconds | Minutes | Seconds |
| **Networking** | Bridge with unique IPs | Manual bridge setup | Docker network |
| **Use case** | Dev/homelab VMs | Production VMs | Containers |

Sistemo is for developers and homelabbers who want **real VMs** without the overhead of a full hypervisor.

## Features

- **Single binary** -- CLI + daemon in one ~15 MB file, zero dependencies beyond Linux + KVM
- **Bridge networking** -- VMs get unique IPs, VM-to-VM connectivity works out of the box
- **Port expose** -- Forward host ports to VMs: `--expose 80`
- **Browser terminal** -- xterm.js over WebSocket, SSH into any VM from your browser
- **Fast boot** -- Firecracker microVMs start in under 15 seconds
- **Systemd service** -- `sistemo service install` makes the daemon survive reboots
- **Config file** -- `~/.sistemo/config.yml` for persistent settings
- **Images** -- Deploy `debian`, `ubuntu`, or `alpine` with one command
- **Custom images** -- Build rootfs from any Docker image (`sistemo image build myimage`)
- **Persistent volumes** -- Create volumes, attach to VMs, data survives VM destroy
- **Auto-cleanup** -- Reconciler detects dead VMs and recovers resources
- **SQLite state** -- No Postgres, no Redis, one file at `~/.sistemo/sistemo.db`
- **x86_64 + ARM64** -- Runs on Intel, AMD, and ARM (Raspberry Pi 5, Hetzner CAX, Graviton)

## Requirements

- **Linux** (kernel 5.10+)
- **KVM** enabled (`ls /dev/kvm` should exist)
- **CPU** with hardware virtualization (Intel VT-x or AMD-V)
- 4 GB+ RAM, 20 GB+ free disk

Most bare metal servers and VPS with nested virtualization work. Raspberry Pi 5 works too.

> **Note:** Sistemo does not run on macOS, Windows, or inside Docker. It needs direct access to `/dev/kvm`.

## Install

### One-line install (recommended)

```bash
curl -sSL https://get.sistemo.io | sh
```

This downloads the binary, Firecracker, a guest kernel, and generates an SSH key. Everything goes into `~/.sistemo/`.

### From GitHub releases

```bash
# Download the binary
curl -sSLO https://github.com/davidestf/sistemo/releases/latest/download/sistemo-linux-amd64
chmod +x sistemo-linux-amd64
sudo mv sistemo-linux-amd64 /usr/local/bin/sistemo

# Run setup (downloads Firecracker + kernel, checks KVM)
sistemo install
```

### Build from source

```bash
git clone https://github.com/davidestf/sistemo.git
cd sistemo
CGO_ENABLED=0 go build -o sistemo ./cmd/sistemo
sudo mv sistemo /usr/local/bin/
sistemo install
```

Requires Go 1.22+.

## Quick Start

```bash
# 1. Start the daemon (needs root for VM networking)
sudo sistemo up

# 2. Deploy a VM (in another terminal)
sistemo vm deploy debian

# 3. Open a browser terminal
sistemo vm terminal debian
# Opens http://localhost:8080 with a live terminal to your VM

# 4. SSH into the VM
sistemo vm ssh debian
```

### Deploy with custom resources

```bash
sistemo vm deploy debian --vcpus 2 --memory 2G --storage 10G --name my-dev-box
```

### Deploy from a Docker image

```bash
# Build a rootfs from any Docker image (openssh-server auto-installed)
docker build -t my-image .
sudo sistemo image build my-image

# Deploy the built image
sistemo vm deploy my-image
```

### Persistent volumes

```bash
# Create a 1 GB volume
sistemo volume create 1024 --name mydata

# Deploy a VM with the volume attached
sistemo vm deploy debian --attach=mydata

# Volume persists even after VM is destroyed
sistemo vm destroy debian
sistemo volume list   # mydata is still there
```

## Commands

Use `sistemo --help` or `sistemo vm --help` for full usage.

```
sistemo up                              Start the daemon
sistemo install [--upgrade]             Setup ~/.sistemo (Firecracker, kernel, SSH key)
sistemo --version, sistemo -v           Print version

sistemo vm deploy <image> [flags]       Create and start a VM
  --vcpus N                             CPU cores (default: 2)
  --memory SIZE                         RAM (default: 512M)
  --storage SIZE                        Disk (default: 2G)
  --name NAME                           VM name
  --attach VOLUME                       Attach a persistent volume
  --expose PORT                         Expose port (hostPort:vmPort or just port)

sistemo vm list                         List all VMs
sistemo vm terminal <name|id>           Open browser terminal
sistemo vm start <name|id>              Start a stopped VM
sistemo vm stop <name|id>               Stop a running VM
sistemo vm destroy <name|id>            Destroy a VM
sistemo vm ssh <name|id>                SSH into a VM
sistemo vm exec <name|id> <command>     Run a command in a VM via SSH
sistemo vm logs <name|id>               Tail Firecracker logs
sistemo vm status <name|id>             Show VM details
sistemo vm expose <name|id> --port P    Expose a VM port on the host
sistemo vm unexpose <name|id> --port P  Remove a port expose rule

sistemo volume create <size_mb>        Create a persistent volume
  --name NAME                           Volume name
sistemo volume list                    List volumes
sistemo volume delete <name|id>        Delete a volume

sistemo image list                      List available images
sistemo image pull <name>               Download an image from the registry
sistemo image build <docker-image>      Build rootfs.ext4 from a Docker image
sistemo ssh-key                         Print the SSH public key

sistemo service install                 Install systemd service (survives reboots)
sistemo service uninstall               Remove systemd service
sistemo config                          Show effective configuration
```

## How It Works

```
┌──────────────────────────────────────────────────┐
│  sistemo binary (CLI + daemon)                   │
│                                                  │
│  CLI ──HTTP──▶ Daemon (:8080)                   │
│                  │                               │
│         ┌───────┼───────┐                        │
│         ▼       ▼       ▼                        │
│       VM 1    VM 2    VM 3                       │
│      ┌─────┐ ┌─────┐ ┌─────┐                   │
│      │netns│ │netns│ │netns│                    │
│      │ TAP │ │ TAP │ │ TAP │                    │
│      │ FC  │ │ FC  │ │ FC  │                    │
│      └──┬──┘ └──┬──┘ └──┬──┘                   │
│         └───────┼───────┘                        │
│          sistemo0 bridge (10.200.0.1/16)         │
│       VMs: 10.200.0.2, .3, .4, ...              │
│                                                  │
│  State: SQLite (~/.sistemo/sistemo.db)           │
│  VMs:   ~/.sistemo/vms/{id}/                    │
└──────────────────────────────────────────────────┘
```

Each VM runs in its own Linux network namespace connected to a shared bridge:
- A **veth pair** connecting the namespace to the `sistemo0` bridge
- A **TAP device** for Firecracker
- A **unique IP** from the 10.200.0.0/16 subnet
- **SMTP blocked** (ports 25, 465, 587) to prevent spam

VMs can communicate with each other via the bridge. Port forwarding uses iptables DNAT. The daemon manages the full VM lifecycle: create, start, stop, destroy, and auto-recovery of crashed VMs.

## Building Custom Images

Sistemo runs **ext4 rootfs images**, not Docker containers. `sistemo image build` automatically installs `openssh-server` via chroot if the image doesn't already have it, so any Docker image works.

```bash
# Any Docker image works — openssh-server is auto-installed during build
docker build -t my-custom-vm .
sudo sistemo image build my-custom-vm
sistemo vm deploy my-custom-vm --name dev
```

Supported package managers for auto-install: apt (Debian/Ubuntu), apk (Alpine), dnf (Fedora), yum (CentOS), pacman (Arch).

## Configuration

Create `~/.sistemo/config.yml` for persistent settings, or use environment variables (env vars override YAML):

| Setting | Env var | Default | Description |
|---------|---------|---------|-------------|
| `port` | `PORT` | 8080 | Daemon HTTP port |
| `host_interface` | `HOST_INTERFACE` | auto-detected | Network interface for NAT |
| — | `HOST_API_KEY` | (none) | API key if exposing daemon to network |
| — | `SISTEMO_REGISTRY_URL` | `registry.sistemo.io` | Image registry URL |

Run `sistemo config` to see the effective merged configuration.

## Security

- The daemon runs as **root** (required for network namespaces and KVM)
- The API listens on **localhost:8080** and is **unauthenticated** by default
- Set `HOST_API_KEY` if you need to expose the API beyond localhost
- SMTP ports are blocked inside VM network namespaces
- SSH keys are ed25519, auto-generated at `~/.sistemo/ssh/sistemo_key`
- VMs are isolated via Firecracker's jailer + KVM + network namespaces

## Documentation

Full documentation: **[docs.sistemo.io](https://docs.sistemo.io)**

- [Installation guide](https://docs.sistemo.io/installation/)
- [CLI reference](https://docs.sistemo.io/commands/)
- [Port expose](https://docs.sistemo.io/port-expose/)
- [Networking](https://docs.sistemo.io/networking/)
- [Systemd service](https://docs.sistemo.io/systemd/)
- [Configuration](https://docs.sistemo.io/configuration/)
- [Building custom images](https://docs.sistemo.io/building-images/)
- [Architecture](https://docs.sistemo.io/architecture/)

## Uninstall

```bash
# 1. Stop all VMs and the daemon
sistemo vm list                    # check running VMs
sistemo vm destroy <name>          # destroy each VM
sudo sistemo service uninstall     # if installed as systemd service

# 2. Remove everything
sudo rm -rf ~/.sistemo             # data, DB, images, volumes, SSH keys
sudo rm -f /usr/local/bin/sistemo  # binary
```

That's it. Sistemo is a single binary + one data directory.

## Contributing

Contributions are welcome! Please open an issue first to discuss what you'd like to change.

```bash
git clone https://github.com/davidestf/sistemo.git
cd sistemo
go test ./...
go build -o sistemo ./cmd/sistemo
```

## License

[Apache-2.0](LICENSE)
