<p align="center">
  <strong>Sistemo</strong><br>
  Self-hosted Linux microVMs for your own hardware. Powered by <a href="https://firecracker-microvm.github.io/">Firecracker</a>.
</p>

<p align="center">
  <a href="https://github.com/davidestf/sistemo/actions/workflows/ci.yml"><img src="https://github.com/davidestf/sistemo/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/davidestf/sistemo/releases/latest"><img src="https://img.shields.io/github/v/release/davidestf/sistemo" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License"></a>
  <a href="https://docs.sistemo.io"><img src="https://img.shields.io/badge/docs-docs.sistemo.io-green" alt="Docs"></a>
</p>

---

**Sistemo** turns your Linux machine into a lightweight VM host. One binary, one command, real VMs — each with its own kernel, systemd, and network stack. No QEMU, no libvirt, no YAML. Deploy a Debian VM in 10 seconds.

## Real VMs, not containers

| | Sistemo | Docker | Proxmox |
|---|---|---|---|
| Isolation | Real VM (KVM) | Shared kernel | Real VM (KVM) |
| Setup | One command | One command | ISO install |
| Binary | ~15 MB, zero deps | Daemon + runtime | Full OS |
| Boot | < 10 seconds | Seconds | Minutes |
| Use case | Dev, homelab, sandboxes | Containers | Production VMs |

## Quick start

```bash
curl -sSL https://get.sistemo.io | sh
sudo sistemo up
sistemo machine deploy debian      # or: sistemo vm deploy debian
sistemo machine ssh debian
```

That's it. Real Debian machine, SSH access, full `apt` + `systemctl`. Running on your hardware via [Firecracker](https://firecracker-microvm.github.io/) microVMs.

## What you can do

```bash
# Deploy from the registry (debian, ubuntu, almalinux)
sistemo machine deploy debian
sistemo machine deploy ubuntu --name dev --vcpus 4 --memory 2G

# Boot from an existing volume (skip image entirely)
sistemo machine deploy --volume web-root --name web2

# Build from any Docker image (openssh-server auto-installed)
sudo sistemo image build node:20
sistemo machine deploy node --name api

# Deploy from a URL or local file
sistemo machine deploy https://example.com/custom.rootfs.ext4
sistemo machine deploy ./my-image.rootfs.ext4
```

Images are cached locally in `~/.sistemo/images/` — first deploy downloads, every deploy after is instant.

### More examples

```bash
# Expose nginx to your network
sistemo machine deploy debian --name web --expose 80
sistemo machine ssh web
apt install -y nginx && systemctl start nginx
# http://your-machine:80 is live

# Isolated network: app + database talk to each other, nothing else can reach them
sistemo network create production
sistemo machine deploy debian --name app --network production --expose 3000
sistemo machine deploy debian --name postgres --network production

# Persistent storage that survives machine delete
sistemo volume create 5G --name pgdata
sistemo machine deploy debian --name db --attach=pgdata

# Resize a volume
sistemo volume resize mydata 10GB

# Attach/detach volumes on a stopped machine
sistemo machine volume attach myvm mydata
sistemo machine volume detach myvm mydata

# Delete a machine but keep its root volume
sistemo machine delete myvm --preserve-storage

# Diagnose your setup
sudo sistemo doctor
```

## Features

- **One binary** -- CLI + daemon, ~15 MB, zero dependencies beyond Linux + KVM
- **SSH + browser terminal** -- `sistemo machine ssh` or open `http://localhost:7777/dashboard/` in your browser
- **Named networks** -- Isolated machine groups with `--network production`
- **Port expose** -- `--expose 80` or `--expose 8080:3000`
- **Custom images** -- Build from any Docker image: `sistemo image build nginx:latest`
- **Persistent volumes** -- Create, resize, attach/detach; every machine's rootfs is also tracked as a volume
- **Systemd service** -- `sistemo service install` survives reboots
- **Health check** -- `sistemo doctor` diagnoses your entire setup
- **Audit log** -- `sistemo history` shows every operation
- **JSON output** -- `sistemo machine list -o json` for scripting and automation
- **vm alias** -- `sistemo vm` works everywhere `sistemo machine` does
- **Command aliases** -- `ls` for list, `rm` for delete, `show` for status
- **Shell completions** -- `sistemo completion bash|zsh|fish`
- **Config validation** -- Bad config? Clear error with fix suggestion
- **x86_64 + ARM64** -- Intel, AMD, Raspberry Pi 5, Hetzner CAX, Graviton
- **Security** -- nftables firewall, seccomp syscall filtering, per-machine cgroup limits, network namespace isolation

## Web Dashboard

Open `http://localhost:7777/dashboard/` after starting the daemon.

![Sistemo Dashboard — Machines](https://sistemo.io/blog/v060/dashboard-vms.png)

- **Deploy machines** from registry, Docker images, or URLs — all from the browser
- **Terminal** — full xterm.js SSH terminal embedded in the dashboard
- **Manage everything** — machines, images, volumes, networks, ports
- **Build Docker images** — type `nginx:latest`, click Build & Deploy
- **Activity history** — full audit log with filters
- **System health** — host CPU, RAM, disk, Firecracker status
- **Authentication** — admin account with JWT sessions (Portainer-style setup)
- **Localhost bypass** — CLI works without auth on the same machine

First visit creates your admin account. After that, login is required for remote access.

## Requirements

- **Linux** (kernel 5.10+) with **KVM** enabled
- CPU with hardware virtualization (Intel VT-x, AMD-V, or ARM64)
- 4 GB+ RAM, 20 GB+ free disk

Works on bare metal, VPS with nested virtualization, and Raspberry Pi 5.

> Sistemo runs on Linux only. It needs `/dev/kvm`.

## Install

```bash
curl -sSL https://get.sistemo.io | sh
```

Or from [GitHub releases](https://github.com/davidestf/sistemo/releases):

```bash
curl -sSLO https://github.com/davidestf/sistemo/releases/latest/download/sistemo-linux-amd64
chmod +x sistemo-linux-amd64
sudo mv sistemo-linux-amd64 /usr/local/bin/sistemo
sistemo install
```

## Commands

```
sistemo up                                    Start the daemon
sistemo doctor                                Check installation health
sistemo history                               Show operation history

# "machine" is the canonical command, "vm" is a permanent alias.
# Both work identically: sistemo machine deploy = sistemo vm deploy

sistemo machine deploy <image> [flags]        Create a machine
  --name NAME                                   Machine name
  --vcpus N  --memory SIZE  --storage SIZE      Resources
  --expose PORT                                 Expose port (host:machine or just port)
  --network NAME                                Join a named network
  --attach VOLUME                               Attach persistent volume
  --volume VOLUME                               Boot from an existing volume
sistemo machine list                          List machines
sistemo machine list -o json                  JSON output for scripting
sistemo machine ssh <name>                    SSH into a machine
sistemo machine exec <name> <command>         Run a command
sistemo machine restart|stop|start <name>     Lifecycle
sistemo machine delete <name>                 Remove a machine
sistemo machine delete <name> -y              Skip confirmation prompt
  --preserve-storage                            Keep root volume on delete
sistemo machine status <name>                 Show details
sistemo machine expose <name> --port P        Expose port at runtime
sistemo machine unexpose <name> --port P      Remove port expose
sistemo machine volume attach <m> <volume>    Attach volume to stopped machine
sistemo machine volume detach <m> <volume>    Detach volume from stopped machine

sistemo network create <name>                 Create isolated network
sistemo network list                          List networks
sistemo network delete <name>                 Delete network

sistemo volume create <size> [--name=N]       Create persistent volume
sistemo volume list                           List volumes
sistemo volume delete <name>                  Delete a volume
sistemo volume resize <name> <size>           Resize a volume
sistemo image build <docker-image>            Build rootfs from Docker
sistemo image list                            List available images
sistemo service install                       Run as systemd service
sistemo admin reset-password                  Reset dashboard admin password
sistemo config                                Show configuration
sistemo completion bash|zsh|fish              Shell completions

# Aliases: ls=list, rm=delete, show=status, vm=machine
sistemo vm deploy debian                      # same as sistemo machine deploy
sistemo machine ls                            # alias for list
sistemo machine rm <name> -y                  # alias for delete, skip confirm
sistemo machine show <name>                   # alias for status

# Global flags
--output json / -o json                       # JSON output (works on list/status)
--yes / -y                                    # Skip confirmation prompts
```

## Configuration

`~/.sistemo/config.yml`:

```yaml
# All settings are optional — these are example overrides, not defaults.
port: 9090                      # default: 7777
bridge_subnet: "10.50.0.0/16"  # default: 10.200.0.0/16
max_vcpus: 8                    # default: 64
default_bandwidth_mbps: 100     # default: 0 (unlimited)
```

Environment variables override YAML: `PORT=9090 sudo sistemo up`

Set `HOST_API_KEY` if exposing the daemon beyond localhost.

## Documentation

**[docs.sistemo.io](https://docs.sistemo.io)** — Full guides and reference:

[Quick start](https://docs.sistemo.io/quickstart/) | [Dashboard](https://docs.sistemo.io/dashboard/) | [Networking](https://docs.sistemo.io/networking/) | [Port expose](https://docs.sistemo.io/port-expose/) | [Volumes](https://docs.sistemo.io/storage/) | [Commands](https://docs.sistemo.io/commands/) | [Configuration](https://docs.sistemo.io/configuration/) | [Building images](https://docs.sistemo.io/building-images/) | [Troubleshooting](https://docs.sistemo.io/troubleshooting/)

## License

[Apache-2.0](LICENSE)
