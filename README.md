# pudu

## Demo

![demo](./demo.gif)

A chaos engineering platform for SRE on-call training, built on [Firecracker](https://firecracker-microvm.github.io/) microVMs.

Pudu launches fleets of lightweight VMs, injects realistic production failures (disk full, memory leaks, network latency, process crashes, DNS hijacks), and scores trainees on how quickly they diagnose and resolve the incident.

## Prerequisites

| Requirement | Notes |
|---|---|
| Linux x86_64 | KVM-enabled host |
| `/dev/kvm` accessible | Run as root or add user to `kvm` group |
| Go 1.21+ | To build from source |

Install all dependencies (firecracker, cloud-image-utils, iproute2, iptables) with:

```bash
make deps
```

## Quickstart

You can use pudu to launch microVMs in parallel and access them through ssh or web terminal.

```bash
# 1. Build binaries, download kernel + Ubuntu 22.04 rootfs, install agent
make build    # no sudo — Go is not in root's PATH
make assets   # no sudo — downloads kernel + rootfs, installs agent into rootfs
N=2 make serve # launch fleet + web terminal
```

After running `make serve`, you can access the terminal at **http://localhost:8888** and investigate.

Also you should be able to:

```bash
root@172.16.0.2
```

## SSH Credentials
```bash
User: root
Password: root
```

## Scenarios/playground

### Easy scenario — disk full (1 VM, monolith)

```bash
# 1. Build binaries, download kernel + Ubuntu 22.04 rootfs, install agent
make build    # no sudo — Go is not in root's PATH
make assets   # no sudo — downloads kernel + rootfs, installs agent into rootfs

# 2. Launch the scenario (sets up TAP networking and starts VMs)
sudo make scenario SCENARIO=scenarios/monolith/disk-full.yaml
```

> Run `make build` and `make assets` without sudo every time you start fresh or rebuild.
> `sudo make scenario` skips the build step and only runs operations that need root.

Once running, open the browser terminal at **http://localhost:8888** and investigate:

```bash
df -h                    # disk is 100% full
ls -la /                 # spot the hidden .pudu-diskfill file
rm /.pudu-diskfill       # fix it
```

The scenario detects disk usage < 80% and prints your score.

### Medium scenario — DB connection cascade (3 VMs, microservices)

```bash
make build
sudo make scenario N=3 SCENARIO=scenarios/microservices/db-connection-exhaustion.yaml
```

Three VMs are launched: `api-0`, `api-1`, `db-0`. Faults inject in stages — a CPU spike on the DB at T=0, network latency at T=1m, and a memory leak on api-0 at T=2m. Use the browser terminal or SSH to diagnose the cascade and stop each fault via the agent API.

### Teardown

```bash
sudo make cleanup        # remove TAP devices
make clean               # remove all build artifacts and downloaded images
```

## Architecture

```
┌─────────────────────────────────────────────────┐
│  pudu (host)                                     │
│  ┌────────────┐  HTTP  ┌───────────────────────┐ │
│  │  scenario  │───────▶│  pudu-agent (in VM)   │ │
│  │  runner    │        │  :7777                │ │
│  └────────────┘        │  /fault/start         │ │
│       │                │  /fault/stop          │ │
│  ┌────▼───────┐        │  /metrics             │ │
│  │  Firecracker        │  /services            │ │
│  │  microVMs  │        └───────────────────────┘ │
│  └────────────┘                                  │
└─────────────────────────────────────────────────┘
```

Each VM gets a TAP interface on `172.16.N.0/30`:
- Host gateway: `172.16.N.1`
- VM IP: `172.16.N.2`

## Commands

### `pudu run` — Launch a single VM

```
pudu run [flags]

  --kernel          path to vmlinux kernel image (required)
  --rootfs          path to ext4 rootfs image (required)
  --firecracker-bin path to firecracker binary
  --vcpus           number of vCPUs (default: 1)
  --mem             memory in MiB (default: 512)
  --tap             TAP device name
  --mac             VM MAC address
  --count           number of VMs to launch in parallel
```

### `pudu serve` — Launch VMs + web terminal

Starts a fleet of VMs and a browser-based SSH terminal on `http://localhost:8888`.

```
pudu serve --kernel vmlinux.bin --rootfs rootfs.ext4 --count 3 --port 8888
```

### `pudu scenario run` — Run a training scenario

```
pudu scenario run [flags] <scenario.yaml>

  --kernel          path to vmlinux kernel image (required)
  --rootfs          path to ext4 rootfs image (required)
  --scale           tier scale overrides, e.g. web=2,db=1
  --dry-run         validate scenario without launching VMs
```

Example:
```bash
sudo pudu scenario run \
  --kernel vmlinux.bin \
  --rootfs rootfs.ext4 \
  scenarios/microservices/db-connection-exhaustion.yaml
```

## Scenario YAML format

Scenarios describe the VM topology, faults to inject, signals shown to the trainee, objectives to verify success, hints, and scoring.

```yaml
scenario:
  id: my-scenario-001
  title: "The Disk That Ate Everything"
  difficulty: easy          # easy | medium | hard | expert
  architecture: monolith    # monolith | microservices | both
  tags: [disk, storage]
  description: |
    Narrative shown to the trainee at scenario start.

environment:
  tiers:
    - name: app
      count: 1
      vcpus: 1
      mem_mb: 512
      services: [nginx, app-server]

faults:
  - id: disk-fill-app
    type: disk              # cpu | memory | disk | network | process | dns
    target:
      tier: app             # target a whole tier...
      # vm: app-0           # ...or a specific VM
      # select: random      # all | random | primary (default: all)
    params:
      path: /
    at: 0s                  # when to inject (from scenario start)
    duration: 10m           # auto-recover after this long (optional)

signals:
  alerts:
    - name: DiskSpaceLow
      severity: critical    # critical | warning | info
      fired_at: 0s
      message: "Disk usage above 95%"
  symptoms:
    - "nginx returns 502 Bad Gateway"

objectives:
  - id: disk-recovered
    description: Disk usage below 80% on app-0
    check:
      type: agent-metric    # http | agent-metric | process-running
      target:
        vm: app-0
      metric: disk_used_pct
      condition: "< 80"

hints:
  - text: "Check disk usage with: df -h"
  - text: "Look for large files: du -sh /* 2>/dev/null | sort -rh | head -20"

scoring:
  base: 100
  time_penalty_per_second: 0.05
  hint_penalty: 10
  perfect_window: 5m
```

### Fault types and parameters

| Type | Key params | Effect |
|---|---|---|
| `cpu` | `load` (e.g. `80%`) | Saturates CPUs to target percentage |
| `memory` | `rate` (e.g. `30mb/min`), `ceiling` (e.g. `85%`) | Simulates a memory leak |
| `disk` | `path` (default `/`) | Fills filesystem by writing a hidden file |
| `network` | `action` (delay/loss/corrupt), `latency`, `jitter`, `packet_loss` | Uses `tc netem` to inject network impairments |
| `process` | `service`, `action` (stop/restart/degrade), `restart` (true/false) | Manipulates systemd services |
| `dns` | `record`, `resolve_to` | Injects a spoofed entry into `/etc/hosts` |

### Objective check types

| Type | Fields | Passes when |
|---|---|---|
| `http` | `path`, `expected_status` | HTTP GET returns expected status code |
| `agent-metric` | `metric`, `condition` | Metric satisfies condition (e.g. `disk_used_pct < 80`) |
| `process-running` | `service` | systemd service is active |

Available metrics: `cpu_pct`, `mem_used_pct`, `disk_used_pct`, `disk_free_mb`, `load_avg_1`

## Included scenarios

| File | Difficulty | Description |
|---|---|---|
| `scenarios/monolith/disk-full.yaml` | Easy | Disk fills completely, app returns 500s |
| `scenarios/microservices/db-connection-exhaustion.yaml` | Medium | Three-stage cascade: DB CPU spike → network latency → API memory leak |

## Makefile targets

```bash
make build          # build pudu + pudu-agent
make assets         # download kernel + rootfs, install agent into rootfs
make net-up-multi   # create TAP devices for N VMs (default N=3)
make net-down-multi # remove TAP devices
make serve          # launch fleet + web terminal
make scenario       # run SCENARIO= file (default: monolith/disk-full)
make clean          # remove binaries, images, logs
```

## In-VM agent

`pudu-agent` runs inside each VM on port `7777` and handles fault injection commands from the scenario runner.

```bash
# Check agent health
curl http://172.16.0.2:7777/health

# Get current metrics
curl http://172.16.0.2:7777/metrics

# Inject a fault manually
curl -X POST http://172.16.0.2:7777/fault/start \
  -d '{"id":"test","type":"cpu","params":{"load":"80%","duration":"30s"}}'

# Stop a fault
curl -X POST http://172.16.0.2:7777/fault/stop \
  -d '{"id":"test"}'
```

## Networking

Each VM uses a dedicated `/30` subnet:

| VM index | Host gateway | VM IP |
|---|---|---|
| 0 | `172.16.0.1` | `172.16.0.2` |
| 1 | `172.16.1.1` | `172.16.1.2` |
| N | `172.16.N.1` | `172.16.N.2` |

SSH into any VM: `ssh root@172.16.N.2` (password: `root`)

## Cleanup

```bash
make cleanup    # tear down TAP networking
make clean      # remove all build artifacts and downloaded images
```
