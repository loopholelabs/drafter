<img alt="Project icon" style="vertical-align: middle;" src="./docs/icon.svg" width="128" height="128" align="left">

# Drafter

Minimal VM primitive with live migration support.

<br/>

[![hydrun CI](https://github.com/loopholelabs/drafter/actions/workflows/hydrun.yaml/badge.svg)](https://github.com/loopholelabs/drafter/actions/workflows/hydrun.yaml)
![Go Version](https://img.shields.io/badge/go%20version-%3E=1.21-61CFDD.svg)
[![Go Reference](https://pkg.go.dev/badge/github.com/pojntfx/loopholelabs/drafter.svg)](https://pkg.go.dev/github.com/pojntfx/loopholelabs/drafter)

## Overview

Drafter is a fast and minimal VM manager with live migration support.

It enables you to ...

- **Snapshot, package, and distribute stateful VMs**: With an opinionated packaging format and simple developer tools, managing, packaging, and distributing VMs becomes as straightforward as working with containers.
- **Run OCI images as VMs**: In addition to running almost any Linux distribution (Alpine Linux, Fedora, Debian, Ubuntu etc.), Drafter can also run OCI images as VMs without the overhead of a nested Docker daemon or full CRI implementation. It uses a dynamic disk configuration system, an optional custom Buildroot-based OS to start the OCI image, and a familiar Docker-like networking configuration.
- **Easily live migrate VMs between heterogeneous nodes with no downtime**: Drafter leverages a [custom optimized Firecracker fork](https://github.com/loopholelabs/firecracker) and [patches to PVM](https://github.com/loopholelabs/linux-pvm-ci) to enable live migration of VMs between heterogeneous nodes/between data centers and cloud providers without hardware virtualization support, even across continents. With a [customizable hybrid pre- and post-copy strategy](https://pojntfx.github.io/networked-linux-memsync/main.pdf), migrations typically take below 100ms within the same datacenter and around 500ms for Europe ↔ North America migrations over the public internet, depending on the application.
- **Hook into suspend and resume lifecycle with agents**: Drafter uses a VSock- and [panrpc](https://github.com/pojntfx/panrpc)-based agent system to signal to guest applications before a suspend/resume event, allowing them to react accordingly.
- **Easily embed VMs inside your applications**: Drafter provides a powerful, context-aware [Go library](https://pkg.go.dev/github.com/pojntfx/loopholelabs/drafter) for all system components, including a NAT for guest-to-host networking, a forwarder for local port-forwarding/host-to-guest networking, an agent and liveness component for responding to snapshots and suspend/resume events inside the guest, a snapshotter for creating snapshots, a packager for packaging VM images, a runner for starting VM images locally, a registry for serving VM images over the network, a peer for starting and live migrating VMs over the network, and a terminator for backing up a VM.

## Installation

Drafter is available as static binaries on [GitHub releases](https://github.com/loopholelabs/drafter/releases). On Linux, you can install them like so:

<!-- TODO: Use the latest release URL here before this branch is merged -->

```shell
for BINARY in drafter-nat drafter-forwarder drafter-snapshotter drafter-packager drafter-runner drafter-registry drafter-peer drafter-terminator; do
    curl -L -o "/tmp/${BINARY}" "https://github.com/loopholelabs/drafter/releases/download/release-replace-r3map-with-silo/${BINARY}.linux-$(uname -m)"
    sudo install "/tmp/${BINARY}" /usr/local/bin
done
```

Drafter depends on our [custom optimized Firecracker fork](https://github.com/loopholelabs/firecracker) for live migration support. Our optional [patches to PVM](https://github.com/loopholelabs/linux-pvm-ci) also allow live migrations of VMs between heterogeneous nodes/between data centers and cloud providers without hardware virtualization support.

The Firecracker fork is available as static binaries on [GitHub releases](https://github.com/loopholelabs/firecracker/releases). On Linux, you can install them like so:

```shell
# Without PVM support
for BINARY in firecracker jailer; do
    curl -L -o "/tmp/${BINARY}" "https://github.com/loopholelabs/firecracker/releases/download/release-firecracker-v1.7-live-migration-and-msync/${BINARY}.linux-$(uname -m)"
    sudo install "/tmp/${BINARY}" /usr/local/bin
done

# With PVM support
for BINARY in firecracker jailer; do
    curl -L -o "/tmp/${BINARY}" "https://github.com/loopholelabs/firecracker/releases/download/release-firecracker-v1.7-live-migration-pvm-and-msync/${BINARY}.linux-$(uname -m)"
    sudo install "/tmp/${BINARY}" /usr/local/bin
done
```

PVM installation instructions depend on your operating system; refer to [loopholelabs/linux-pvm-ci#installation](https://github.com/loopholelabs/linux-pvm-ci?tab=readme-ov-file#installation) for more information.

## Reference

<details>
  <summary>Expand command reference</summary>

### NAT

```shell
$ drafter-nat --help
Usage of drafter-nat:
  -allow-incoming-traffic
        Whether to allow incoming traffic to the namespaces (at host-veth-internal-ip:port) (default true)
  -blocked-subnet-cidr string
        CIDR to block for the namespace (default "10.0.15.0/24")
  -host-interface string
        Host gateway interface (default "wlp0s20f3")
  -host-veth-cidr string
        CIDR for the veths outside the namespace (default "10.0.8.0/22")
  -namespace-interface string
        Name for the interface inside the namespace (default "tap0")
  -namespace-interface-gateway string
        Gateway for the interface inside the namespace (default "172.100.100.1")
  -namespace-interface-ip string
        IP for the interface inside the namespace (default "172.100.100.2")
  -namespace-interface-mac string
        MAC address for the interface inside the namespace (default "02:0e:d9:fd:68:3d")
  -namespace-interface-netmask uint
        Netmask for the interface inside the namespace (default 30)
  -namespace-prefix string
        Prefix for the namespace IDs (default "ark")
  -namespace-veth-cidr string
        CIDR for the veths inside the namespace (default "10.0.15.0/24")
```

### Forwarder

```shell
$ drafter-forwarder --help
Usage of drafter-forwarder:
  -host-veth-cidr string
        CIDR for the veths outside the namespace (default "10.0.8.0/22")
  -port-forwards string
        Port forwards configuration (wildcard IPs like 0.0.0.0 are not valid, be explict) (default "[{\"netns\":\"ark0\",\"internalPort\":\"6379\",\"protocol\":\"tcp\",\"externalAddr\":\"127.0.0.1:3333\"}]")
```

### Agent

```shell
$ drafter-agent --help
Usage of drafter-agent:
  -after-resume-cmd string
        Command to run after the VM has been resumed (leave empty to disable)
  -before-suspend-cmd string
        Command to run before the VM is suspended (leave empty to disable)
  -shell-cmd string
        Shell to use to run the before suspend and after resume commands (default "sh")
  -vsock-port uint
        VSock port (default 26)
  -vsock-timeout duration
        VSock dial timeout (default 1m0s)
```

### Liveness

```shell
$ drafter-liveness --help
Usage of drafter-liveness:
  -vsock-port int
        VSock port (default 25)
  -vsock-timeout duration
        VSock dial timeout (default 1m0s)
```

### Snapshotter

```shell
$ drafter-snapshotter --help
Usage of drafter-snapshotter:
  -agent-vsock-port int
        Agent VSock port (default 26)
  -boot-args string
        Boot/kernel arguments (default "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0")
  -cgroup-version int
        Cgroup version to use for Jailer (default 2)
  -chroot-base-dir chroot
        chroot base directory (default "out/vms")
  -cpu-count int
        CPU count (default 1)
  -cpu-template string
        Firecracker CPU template (see https://github.com/firecracker-microvm/firecracker/blob/main/docs/cpu_templates/cpu-templates.md#static-cpu-templates for the options) (default "None")
  -devices string
        Devices configuration (default "[{\"name\":\"state\",\"input\":\"\",\"output\":\"out/package/state.bin\"},{\"name\":\"memory\",\"input\":\"\",\"output\":\"out/package/memory.bin\"},{\"name\":\"kernel\",\"input\":\"out/blueprint/vmlinux\",\"output\":\"out/package/vmlinux\"},{\"name\":\"disk\",\"input\":\"out/blueprint/rootfs.ext4\",\"output\":\"out/package/rootfs.ext4\"},{\"name\":\"config\",\"input\":\"\",\"output\":\"out/package/config.json\"},{\"name\":\"oci\",\"input\":\"out/blueprint/oci.ext4\",\"output\":\"out/package/oci.ext4\"}]")
  -enable-input
        Whether to enable VM stdin
  -enable-output
        Whether to enable VM stdout and stderr (default true)
  -firecracker-bin string
        Firecracker binary (default "firecracker")
  -gid int
        Group ID for the Firecracker process
  -interface string
        Name of the interface in the network namespace to use (default "tap0")
  -jailer-bin string
        Jailer binary (from Firecracker) (default "jailer")
  -liveness-vsock-port int
        Liveness VSock port (default 25)
  -mac string
        MAC of the interface in the network namespace to use (default "02:0e:d9:fd:68:3d")
  -memory-size int
        Memory size (in MB) (default 1024)
  -netns string
        Network namespace to run Firecracker in (default "ark0")
  -numa-node int
        NUMA node to run Firecracker in
  -resume-timeout duration
        Maximum amount of time to wait for agent and liveness to resume (default 1m0s)
  -uid int
        User ID for the Firecracker process
```

### Packager

```shell
$ drafter-packager --help
Usage of drafter-packager:
  -devices string
        Devices configuration (default "[{\"name\":\"state\",\"path\":\"out/package/state.bin\"},{\"name\":\"memory\",\"path\":\"out/package/memory.bin\"},{\"name\":\"kernel\",\"path\":\"out/package/vmlinux\"},{\"name\":\"disk\",\"path\":\"out/package/rootfs.ext4\"},{\"name\":\"config\",\"path\":\"out/package/config.json\"},{\"name\":\"oci\",\"path\":\"out/blueprint/oci.ext4\"}]")
  -extract
        Whether to extract or archive
  -package-path string
        Path to package file (default "out/app.tar.zst")
```

### Runner

```shell
$ drafter-runner --help
Usage of drafter-runner:
  -cgroup-version int
        Cgroup version to use for Jailer (default 2)
  -chroot-base-dir chroot
        chroot base directory (default "out/vms")
  -devices string
        Devices configuration (default "[{\"name\":\"state\",\"path\":\"out/package/state.bin\"},{\"name\":\"memory\",\"path\":\"out/package/memory.bin\"},{\"name\":\"kernel\",\"path\":\"out/package/vmlinux\"},{\"name\":\"disk\",\"path\":\"out/package/rootfs.ext4\"},{\"name\":\"config\",\"path\":\"out/package/config.json\"},{\"name\":\"oci\",\"path\":\"out/blueprint/oci.ext4\"}]")
  -enable-input
        Whether to enable VM stdin
  -enable-output
        Whether to enable VM stdout and stderr (default true)
  -firecracker-bin string
        Firecracker binary (default "firecracker")
  -gid int
        Group ID for the Firecracker process
  -jailer-bin string
        Jailer binary (from Firecracker) (default "jailer")
  -netns string
        Network namespace to run Firecracker in (default "ark0")
  -numa-node int
        NUMA node to run Firecracker in
  -rescue-timeout duration
        Maximum amount of time to wait for rescue operations (default 5s)
  -resume-timeout duration
        Maximum amount of time to wait for agent and liveness to resume (default 1m0s)
  -uid int
        User ID for the Firecracker process
```

### Registry

```shell
$ drafter-registry --help
Usage of drafter-registry:
  -concurrency int
        Amount of concurrent workers to use in migrations (default 4096)
  -devices string
        Devices configuration (default "[{\"name\":\"state\",\"input\":\"out/package/state.bin\",\"blockSize\":65536},{\"name\":\"memory\",\"input\":\"out/package/memory.bin\",\"blockSize\":65536},{\"name\":\"kernel\",\"input\":\"out/package/vmlinux\",\"blockSize\":65536},{\"name\":\"disk\",\"input\":\"out/package/rootfs.ext4\",\"blockSize\":65536},{\"name\":\"config\",\"input\":\"out/package/config.json\",\"blockSize\":65536},{\"name\":\"oci\",\"input\":\"out/blueprint/oci.ext4\",\"blockSize\":65536}]")
  -laddr string
        Address to listen on (default ":1600")
```

### Peer

```shell
$ drafter-peer --help
Usage of drafter-peer:
  -cgroup-version int
        Cgroup version to use for Jailer (default 2)
  -chroot-base-dir chroot
        chroot base directory (default "out/vms")
  -concurrency int
        Amount of concurrent workers to use in migrations (default 4096)
  -devices string
        Devices configuration (default "[{\"name\":\"state\",\"base\":\"out/package/state.bin\",\"overlay\":\"out/overlay/state.bin\",\"state\":\"out/state/state.bin\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000},{\"name\":\"memory\",\"base\":\"out/package/memory.bin\",\"overlay\":\"out/overlay/memory.bin\",\"state\":\"out/state/memory.bin\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000},{\"name\":\"kernel\",\"base\":\"out/package/vmlinux\",\"overlay\":\"out/overlay/vmlinux\",\"state\":\"out/state/vmlinux\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000},{\"name\":\"disk\",\"base\":\"out/package/rootfs.ext4\",\"overlay\":\"out/overlay/rootfs.ext4\",\"state\":\"out/state/rootfs.ext4\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000},{\"name\":\"config\",\"base\":\"out/package/config.json\",\"overlay\":\"out/overlay/config.json\",\"state\":\"out/state/config.json\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000},{\"name\":\"oci\",\"base\":\"out/package/oci.ext4\",\"overlay\":\"out/overlay/oci.ext4\",\"state\":\"out/state/oci.ext4\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000}]")
  -enable-input
        Whether to enable VM stdin
  -enable-output
        Whether to enable VM stdout and stderr (default true)
  -firecracker-bin string
        Firecracker binary (default "firecracker")
  -gid int
        Group ID for the Firecracker process
  -jailer-bin string
        Jailer binary (from Firecracker) (default "jailer")
  -laddr string
        Local address to listen on (leave empty to disable) (default "localhost:1337")
  -netns string
        Network namespace to run Firecracker in (default "ark0")
  -numa-node int
        NUMA node to run Firecracker in
  -raddr string
        Remote address to connect to (leave empty to disable) (default "localhost:1337")
  -rescue-timeout duration
        Maximum amount of time to wait for rescue operations (default 1m0s)
  -resume-timeout duration
        Maximum amount of time to wait for agent and liveness to resume (default 1m0s)
  -uid int
        User ID for the Firecracker process
```

### Terminator

```shell
$ drafter-terminator --help
Usage of drafter-terminator:
  -devices string
        Devices configuration (default "[{\"name\":\"state\",\"output\":\"out/package/state.bin\"},{\"name\":\"memory\",\"output\":\"out/package/memory.bin\"},{\"name\":\"kernel\",\"output\":\"out/package/vmlinux\"},{\"name\":\"disk\",\"output\":\"out/package/rootfs.ext4\"},{\"name\":\"config\",\"output\":\"out/package/config.json\"},{\"name\":\"oci\",\"output\":\"out/package/oci.ext4\"}]")
  -raddr string
        Remote address to connect to (default "localhost:1337")
```

</details>

## Acknowledgements

- [Loophole Labs Silo](github.com/loopholelabs/silo) provides the storage and data migration framework.
- [coreos/go-iptables](github.com/coreos/go-iptables) provides the IPTables bindings for port-forwarding.
- [pojntfx/panrpc](https://github.com/pojntfx/panrpc) provides the RPC framework for local host ↔ guest communication.
- [Font Awesome](https://fontawesome.com/) provides the assets used for the icon and logo.

## Contributing

Bug reports and pull requests are welcome on GitHub at [https://github.com/loopholelabs/drafter][gitrepo]. For more contribution information check out [the contribution guide](https://github.com/loopholelabs/drafter/blob/master/CONTRIBUTING.md).

## License

The Drafter project is available as open source under the terms of the [GNU Affero General Public License, Version 3](https://www.gnu.org/licenses/agpl-3.0.en.html).

## Code of Conduct

Everyone interacting in the Drafter project's codebases, issue trackers, chat rooms and mailing lists is expected to follow the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/master/code-of-conduct.md).

## Project Managed By:

[![https://loopholelabs.io][loopholelabs]](https://loopholelabs.io)

[gitrepo]: https://github.com/loopholelabs/drafter
[loopholelabs]: https://cdn.loopholelabs.io/loopholelabs/LoopholeLabsLogo.svg
[loophomepage]: https://loopholelabs.io
