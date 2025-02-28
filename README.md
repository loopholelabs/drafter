<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="./docs/logo-dark.svg">
  <img alt="Logo" src="./docs/logo-light.svg">
</picture>

[![License: AGPL 3.0](https://img.shields.io/github/license/loopholelabs/drafter)](https://www.gnu.org/licenses/agpl-3.0.en.html)
[![Discord](https://dcbadge.vercel.app/api/server/JYmFhtdPeu?style=flat)](https://loopholelabs.io/discord)
[![hydrun CI](https://github.com/loopholelabs/drafter/actions/workflows/hydrun.yaml/badge.svg)](https://github.com/loopholelabs/drafter/actions/workflows/hydrun.yaml)
![Go Version](https://img.shields.io/badge/go%20version-%3E=1.21-61CFDD.svg)
[![Go Reference](https://pkg.go.dev/badge/github.com/loopholelabs/drafter.svg)](https://pkg.go.dev/github.com/loopholelabs/drafter)

</div>

## Overview

Drafter is a compute primitive with live migration support.

It enables you to:

- **Snapshot, package, and distribute stateful VMs**: With an opinionated packaging format and simple developer tools, managing, packaging, and distributing VMs becomes as straightforward as working with containers.
- **Run OCI images as VMs**: In addition to running almost any Linux distribution (Alpine Linux, Fedora, Debian, Ubuntu etc.), Drafter can also run OCI images as VMs without the overhead of a nested Docker daemon or full CRI implementation. It uses a dynamic disk configuration system, an optional custom Buildroot-based OS to start the OCI image, and a familiar Docker-like networking configuration.
- **Easily live migrate VMs between heterogeneous nodes with no downtime**: Drafter leverages a [custom optimized Firecracker fork](https://github.com/loopholelabs/firecracker) and [patches to PVM](https://github.com/loopholelabs/linux-pvm-ci) to enable live migration of VMs between heterogeneous nodes, data centers and cloud providers without hardware virtualization support, even across continents. With a [customizable hybrid pre- and post-copy strategy](https://pojntfx.github.io/networked-linux-memsync/main.pdf), migrations typically take below 100ms within the same data center and around 500ms for Europe ↔ North America migrations over the public internet, depending on the application.
- **Hook into suspend and resume lifecycle with agents**: Drafter uses a VSock- and [panrpc](https://github.com/pojntfx/panrpc)-based agent system to signal to guest applications before a suspend/resume event, allowing them to react accordingly.
- **Easily embed VMs inside your applications**: Drafter provides a powerful, context-aware [Go library](https://pkg.go.dev/github.com/loopholelabs/drafter) for all system components, including a NAT for guest-to-host networking, a forwarder for local port-forwarding/host-to-guest networking, an agent and liveness component for responding to snapshots and suspend/resume events inside the guest, a snapshotter for creating snapshots, a packager for packaging VM images, a runner for starting VM images locally, a registry for serving VM images over the network, a mounter for re-using files and devices between VMs and moving them without migrating the VM itself, a peer for starting and live migrating VMs over the network, and a terminator for backing up a VM.

**Want to see it in action?** See this snippet from our KubeCon talk where we live migrate a Minecraft server between two continents without downtime:

<p align="center">
  <a href="https://youtu.be/HrtX0JrjekE?si=IOMP3pY-xyYSkKy1&t=1866" target="_blank">
    <img alt="YouTube thumbnail of the KubeCon NA 2023 talk on Drafter" width="60%" src="https://img.youtube.com/vi/HrtX0JrjekE/0.jpg" />
  </a>
</p>

## Installation

Drafter is available as static binaries on [GitHub releases](https://github.com/loopholelabs/drafter/releases). On Linux, you can install them like so:

```shell
for BINARY in drafter-nat drafter-forwarder drafter-snapshotter drafter-packager drafter-runner drafter-registry drafter-mounter drafter-peer drafter-terminator; do
    curl -L -o "/tmp/${BINARY}" "https://github.com/loopholelabs/drafter/releases/latest/download/${BINARY}.linux-$(uname -m)"
    sudo install "/tmp/${BINARY}" /usr/local/bin
done
```

Drafter depends on our [custom optimized Firecracker fork](https://github.com/loopholelabs/firecracker) for live migration support. Our optional [patches to PVM](https://github.com/loopholelabs/linux-pvm-ci) also allow live migrations of VMs between heterogeneous nodes, data centers and cloud providers without hardware virtualization support.

The Firecracker fork is available as static binaries on [GitHub releases](https://github.com/loopholelabs/firecracker/releases). On Linux, you can install them like so:

```shell
# Without PVM support
for BINARY in firecracker jailer; do
    curl -L -o "/tmp/${BINARY}" "https://github.com/loopholelabs/firecracker/releases/download/release-main-live-migration/${BINARY}.linux-$(uname -m)"
    sudo install "/tmp/${BINARY}" /usr/local/bin
done

# With PVM support
for BINARY in firecracker jailer; do
    curl -L -o "/tmp/${BINARY}" "https://github.com/loopholelabs/firecracker/releases/download/release-main-live-migration-pvm/${BINARY}.linux-$(uname -m)"
    sudo install "/tmp/${BINARY}" /usr/local/bin
done
```

PVM installation instructions depend on your operating system; refer to [loopholelabs/linux-pvm-ci#installation](https://github.com/loopholelabs/linux-pvm-ci?tab=readme-ov-file#installation) for more information. Verify that you have a KVM implementation (Intel, AMD or PVM) available by running the following:

```shell
$ file /dev/kvm # Should return "/dev/kvm: character special (10/232)"
```

Using Drafter for live migration also requires the NBD kernel module to be loaded, preferably with more devices than the default (12). You can increase the number of available NBD devices by running the following:

```shell
$ sudo modprobe nbd nbds_max=4096
```

## Tutorial

### 1. Downloading or Building a VM Blueprint

To create a VM blueprint, you have two options: download a pre-built blueprint or build one from scratch. Blueprints are typically packaged as `.tar.zst` archives using `drafter-packager`.

In this tutorial, we'll use Drafter to run the key-value store [Valkey](https://valkey.io/) (formerly Redis). We'll use two blueprints: the DrafterOS blueprint, a minimal Buildroot-based Linux OS for running OCI images, and the Valkey blueprint, which contains the Valkey OCI image. Note that you can also include Valkey or any other application directly in the base OS blueprint instead of using an OCI image.

#### Downloading a Pre-Built VM Blueprint

<details>
  <summary>Expand section</summary>

To download the pre-built blueprints for your architecture, execute the following commands:

```shell
$ mkdir -p out
$ curl -Lo out/drafteros-oci.tar.zst "https://github.com/loopholelabs/drafter/releases/latest/download/drafteros-oci-$(uname -m).tar.zst" # Use `drafteros-oci-$(uname -m)_pvm.tar.zst` if you're using PVM
$ curl -Lo out/oci-valkey.tar.zst "https://github.com/loopholelabs/drafter/releases/latest/download/oci-valkey-$(uname -m).tar.zst"
```

Next, use `drafter-packager` to extract the blueprints:

```shell
$ drafter-packager --package-path out/drafteros-oci.tar.zst --extract --devices '[
  {
    "name": "kernel",
    "path": "out/blueprint/vmlinux"
  },
  {
    "name": "disk",
    "path": "out/blueprint/rootfs.ext4"
  }
]'

$ drafter-packager --package-path out/oci-valkey.tar.zst --extract --devices '[
  {
    "name": "oci",
    "path": "out/blueprint/oci.ext4"
  }
]'
```

The output directory should now contain the VM blueprint files:

```shell
$ tree out/
out/
├── blueprint
│   ├── oci.ext4
│   ├── rootfs.ext4
│   └── vmlinux
# ...
```

**Congratulations!** You've downloaded and extracted your first VM blueprint; be sure to check out the [packager reference](#packager) for more information. Next, let's create a snapshot from it!

</details>

#### Building a VM Blueprint Locally

<details>
  <summary>Expand section</summary>

To build the blueprints locally, you can use the [included Makefile](./Makefile) with the following commands:

```shell
# Build the DrafterOS blueprint
$ make depend/os OS_DEFCONFIG=drafteros-oci-firecracker-x86_64_defconfig # Use `drafteros-oci-firecracker-x86_64_pvm_defconfig` if you're using PVM and `drafteros-oci-firecracker-aarch64_defconfig` if you're on `aarch64`
$ make config/kernel # Optional: Configure kernel
$ make save/kernel # Optional: Write back the kernel configuration to the defconfig
$ make config/os # Optional: Configure DrafterOS
$ make save/os # Optional: Write back the DrafterOS configuration to the defconfig
$ make build/os

# Build the Valkey OCI image blueprint
$ sudo make build/oci OCI_IMAGE_URI=docker://valkey/valkey:latest OCI_IMAGE_ARCHITECTURE=amd64 # Or `arm64` if you're on `aarch64`
```

The output directory should now contain the VM blueprint files:

```shell
$ tree out/
out/
├── blueprint
│   ├── oci.ext4
│   ├── rootfs.ext4
│   └── vmlinux
# ...
```

You can optionally package the VM blueprint files using `drafter-packager` for distribution by running the following:

```shell
$ drafter-packager --package-path out/drafteros-oci.tar.zst --devices '[
  {
    "name": "kernel",
    "path": "out/blueprint/vmlinux"
  },
  {
    "name": "disk",
    "path": "out/blueprint/rootfs.ext4"
  }
]'

$ drafter-packager --package-path out/oci-valkey.tar.zst --devices '[
  {
    "name": "oci",
    "path": "out/blueprint/oci.ext4"
  }
]'
```

Drafter doesn't concern itself with the actual process of building the underlying VM images aside from this simple build tooling. If you're looking for a more advanced and streamlined process, like streaming conversion and startup of OCI images, a way to replicate/distribute packages or a build service, check out [Loophole Labs Architect](https://architect.run/).

**Congratulations!** You've built and packaged your first VM blueprint; be sure to check out the [packager reference](#packager) for more information. Next, let's create a snapshot from it!

</details>

### 2. Creating a VM Package with `drafter-snapshotter`

Now that you have a blueprint available, you can create a VM package from it by using `drafter-snapshotter`. Drafter heavily relies on the use of snapshots to amortize kernel cold starts and application boot times. To get started, start the Drafter NAT so that the VM(s) can access the network:

```shell
$ sudo drafter-nat --host-interface wlp0s20f3 # Replace wlp0s20f3 with the network interface you want to route outgoing traffic from the VMs to
```

In a new terminal, start the snapshotter:

> Using PVM? Make sure to pass a value to `--cpu-template`, e.g. `T2`, so that your snapshots are portable across hosts/different cloud providers with different CPU models!

```shell
$ sudo drafter-snapshotter --netns ark0 --devices '[
  {
    "name": "state",
    "output": "out/package/state.bin"
  },
  {
    "name": "memory",
    "output": "out/package/memory.bin"
  },
  {
    "name": "kernel",
    "input": "out/blueprint/vmlinux",
    "output": "out/package/vmlinux"
  },
  {
    "name": "disk",
    "input": "out/blueprint/rootfs.ext4",
    "output": "out/package/rootfs.ext4"
  },
  {
    "name": "config",
    "output": "out/package/config.json"
  },
  {
    "name": "oci",
    "input": "out/blueprint/oci.ext4",
    "output": "out/package/oci.ext4"
  }
]'
```

The output directory should now contain the VM package files with the snapshot:

```shell
$ tree out/
out/
# ...
├── package
│   ├── config.json
│   ├── memory.bin
│   ├── oci.ext4
│   ├── rootfs.ext4
│   ├── state.bin
│   └── vmlinux
# ...
```

You can optionally package the VM package files using `drafter-packager` for distribution by running the following:

```shell
$ drafter-packager --package-path out/valkey.tar.zst --devices '[
  {
    "name": "state",
    "path": "out/package/state.bin"
  },
  {
    "name": "memory",
    "path": "out/package/memory.bin"
  },
  {
    "name": "kernel",
    "path": "out/package/vmlinux"
  },
  {
    "name": "disk",
    "path": "out/package/rootfs.ext4"
  },
  {
    "name": "config",
    "path": "out/package/config.json"
  },
  {
    "name": "oci",
    "path": "out/blueprint/oci.ext4"
  }
]'
```

Note that unless you're using PVM (see [installation](#installation) and [Live Migrating a VM Instance Across Processes and Hosts](#live-migrating-a-vm-instance-across-processes-and-hosts) for more information), these snapshots are not generally portable across different hosts, even if you use CPU templates. If you're looking for a way to automate snapshot compatibility checks, host-specific builds or a snapshot creation service, check out [Loophole Labs Architect](https://architect.run/).

**Cheers!** You've created your first VM package; be sure to check out the [snapshotter reference](#snapshotter) for more information. Next, let's find out how we can start it!

### 3. Creating a VM Instance from a Package with `drafter-runner`

Now that we have a VM package, you can start it locally with `drafter-runner` by running the following:

```shell
$ sudo drafter-runner --netns ark0 --devices '[
  {
    "name": "state",
    "path": "out/package/state.bin"
  },
  {
    "name": "memory",
    "path": "out/package/memory.bin"
  },
  {
    "name": "kernel",
    "path": "out/package/vmlinux"
  },
  {
    "name": "disk",
    "path": "out/package/rootfs.ext4"
  },
  {
    "name": "config",
    "path": "out/package/config.json"
  },
  {
    "name": "oci",
    "path": "out/blueprint/oci.ext4"
  }
]'
```

You should see the Valkey logs and a login prompt appear:

```plaintext
# ...
[    0.902719] crun[383]: 1:M 22 Jun 2024 07:35:10.407 * Server initialized
[    0.903232] crun[383]: 1:M 22 Jun 2024 07:35:10.408 * Ready to accept connections tcp

Welcome to Loophole Labs DrafterOS
drafterhost login:
```

By default, input is disabled. To enable it, pass the `--enable-input` flag to `drafter-runner`. To stop the VM, press `CTRL-C` or send the `SIGINT` signal to the `drafter-runner` process. Before stopping, `drafter-runner` takes a snapshot of the VM, which it uses to resume the VM the next time it is started.

**Well done!** You've successfully started your VM package; be sure to check out the [runner reference](#runner) for more information. Next, let's find out how you can access it!

### 4. Forwarding a VM Instance Port with `drafter-forwarder`

> See [Does Drafter Support IPv6?](#does-drafter-support-ipv6) for more information on the current state of IPv6 support

To access the Valkey instance running inside the VM, you can forward its port to the host using `drafter-forwarder`. For example, to forward Valkey's TCP port `6379` to port `3333` on your local system (similar to Docker's `-p 6379:3333/tcp` flag), run the following command:

```shell
$ sudo drafter-forwarder --port-forwards '[
  {
    "netns": "ark0",
    "internalPort": "6379",
    "protocol": "tcp",
    "externalAddr": "127.0.0.1:3333"
  }
]'
```

In a new terminal, connect to Valkey with:

```shell
$ valkey-cli -u redis://127.0.0.1:3333/0
```

Valkey is now accessible on your local system. To also expose it to your network on port `3333`, run the following command (replacing `192.168.12.31` with your IPv4 address):

```shell
$ sudo drafter-forwarder --port-forwards '[
  {
    "netns": "ark0",
    "internalPort": "6379",
    "protocol": "tcp",
    "externalAddr": "127.0.0.1:3333"
  },
  {
    "netns": "ark0",
    "internalPort": "6379",
    "protocol": "tcp",
    "externalAddr": "192.168.12.31:3333"
  }
]'
```

Remember to open port `3333/tcp` on your firewall to make it reachable from your network. To access the port from both your local system (e.g. for a reverse proxy setup) and the network, you must forward it to both `127.0.0.1:3333` and `192.168.12.31:3333`. If you intend on using the port-forwarder during live migrations, keep in mind that connections will break after moving to the new host; if you're interested in keeping the connections alive, see [How Can I Keep My Network Connections Alive While Live Migrating?](#how-can-i-keep-my-network-connections-alive-while-live-migrating).

**Enjoy your Valkey service!** Feel free to play around with it using your preferred tooling and check out the [forwarder reference](#forwarder) for more information. Next, let's find out how we can live migrate it between hosts!

### 5. Creating and Live Migrating VM Instances with `drafter-peer`

While `drafter-runner` is useful for trying out VM packages locally, using `drafter-peer` is recommended for any advanced usage, such as creating multiple VM instances from the same VM package or live migrating VM instances between hosts.

#### Creating a Single VM Instance from a VM Package

<details>
  <summary>Expand section</summary>

To replicate `drafter-runner` with `drafter-peer`, that is to say, to create a single VM instance from a VM package where all changes get written back to the VM package, run the following:

```shell
$ sudo drafter-peer --netns ark0 --raddr '' --laddr '' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/overlay/state.bin",
    "state": "out/state/state.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/overlay/memory.bin",
    "state": "out/state/memory.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/overlay/vmlinux",
    "state": "out/state/vmlinux",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/overlay/rootfs.ext4",
    "state": "out/state/rootfs.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/overlay/config.json",
    "state": "out/state/config.json",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/overlay/oci.ext4",
    "state": "out/state/oci.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  }
]'
```

**🚀 That's it!** You've successfully created, snapshotted and started an instance of your first service with Drafter. We can't wait to see what you're going to build next! Be sure to take a look at the [reference](#reference), [examples](#examples) and [frequently asked questions](#faq) for more information.

</details>

#### Creating Multiple Independent VM Instances from a Single VM Package

<details>
  <summary>Expand section</summary>

`drafter-peer` supports the use of a copy-on-write mechanism to allow creating two independent VM instances from the same VM package. To use it, run each VM in its own network namespace and define a different `overlay` and `state` file for each VM instance. Start the first VM like so:

```shell
$ sudo drafter-peer --netns ark0 --raddr '' --laddr '' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/instance-0/overlay/state.bin",
    "state": "out/instance-0/state/state.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/instance-0/overlay/memory.bin",
    "state": "out/instance-0/state/memory.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/instance-0/overlay/vmlinux",
    "state": "out/instance-0/state/vmlinux",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/instance-0/overlay/rootfs.ext4",
    "state": "out/instance-0/state/rootfs.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/instance-0/overlay/config.json",
    "state": "out/instance-0/state/config.json",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/instance-0/overlay/oci.ext4",
    "state": "out/instance-0/state/oci.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  }
]'
```

Then start the second, independent VM from the same VM package like so:

```shell
$ sudo drafter-peer --netns ark1 --raddr '' --laddr '' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/instance-1/overlay/state.bin",
    "state": "out/instance-1/state/state.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/instance-1/overlay/memory.bin",
    "state": "out/instance-1/state/memory.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/instance-1/overlay/vmlinux",
    "state": "out/instance-1/state/vmlinux",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/instance-1/overlay/rootfs.ext4",
    "state": "out/instance-1/state/rootfs.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/instance-1/overlay/config.json",
    "state": "out/instance-1/state/config.json",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/instance-1/overlay/oci.ext4",
    "state": "out/instance-1/state/oci.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  }
]'
```

You should now have two independent VM instances started from a single VM package. To access both instances on your local system, you can forward each instance's ports to a matching port on the host; to make the first instance's Valkey available on port `3333` on the host, and the second instance's Valkey available on port `4444` on the host, run:

```shell
$ sudo drafter-forwarder --port-forwards '[
  {
    "netns": "ark0",
    "internalPort": "6379",
    "protocol": "tcp",
    "externalAddr": "127.0.0.1:3333"
  },
  {
    "netns": "ark1",
    "internalPort": "6379",
    "protocol": "tcp",
    "externalAddr": "127.0.0.1:4444"
  }
]'
```

In a new terminal, you can now connect to the two independent Valkey instances with:

```shell
# For the first instance
$ valkey-cli -u redis://127.0.0.1:3333/0
# For the second instance
$ valkey-cli -u redis://127.0.0.1:4444/0
```

**🚀 That's it!** You've successfully created, snapshotted and started multiple independent instances of your first service with Drafter. We can't wait to see what you're going to build next! Be sure to take a look at the [reference](#reference), [examples](#examples) and [frequently asked questions](#faq) for more information.

</details>

#### Live Migrating a VM Instance Across Processes and Hosts

<details>
  <summary>Expand section</summary>

To live migrate a VM instance, first start a new VM instance and set its listen address using the `--laddr` option. If you want the VM to be migratable over the network instead of just the local system, use `--laddr ':1337'`:

```shell
$ sudo drafter-peer --netns ark0 --raddr '' --laddr 'localhost:1337' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/instance-0/overlay/state.bin",
    "state": "out/instance-0/state/state.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/instance-0/overlay/memory.bin",
    "state": "out/instance-0/state/memory.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/instance-0/overlay/vmlinux",
    "state": "out/instance-0/state/vmlinux",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/instance-0/overlay/rootfs.ext4",
    "state": "out/instance-0/state/rootfs.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/instance-0/overlay/config.json",
    "state": "out/instance-0/state/config.json",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/instance-0/overlay/oci.ext4",
    "state": "out/instance-0/state/oci.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  }
]'
```

Once the instance has started, you should see output like this:

```shell
2024/06/24 13:43:11 Resumed VM in 139.720884ms on out/vms/firecracker/VKaFJFNJwPwM6QRoVwQV3i/root
2024/06/24 13:43:11 Serving on 127.0.0.1:1337
```

This instance is now the first peer in a migration chain. Now that the VM instance is migratable, start a second peer (the second peer in the migration chain) and set its remote address to that of the first instance using the `--raddr` option. This can be done either on the local system (to migrate a VM instance between two processes) or over a network to a second host (to migrate a VM instance between two hosts).

If you use PVM and a CPU template (see [installation](#installation)), this process should work across cloud providers and different CPU models, as long as the CPUs are from the same manufacturer (AMD to AMD and Intel to Intel migrations are supported; AMD to Intel migrations and vice versa will fail). If you're not using PVM, live migration typically only works if the exact same CPU model is used on both the first and second host.

In this example, we'll pass `--laddr ''` to the second instance, which will not make it migratable again, effectively terminating the migration chain. If you want to make the instance migratable again after the migration, effectively continuing the migration chain, set `--laddr` to a listen address of your choice. To migrate the VM, run the following command, replacing `--raddr 'localhost:1337'` with the actual remote address if you're migrating over the network:

```shell
$ sudo drafter-peer --netns ark1 --raddr 'localhost:1337' --laddr '' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/instance-1/overlay/state.bin",
    "state": "out/instance-1/state/state.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/instance-1/overlay/memory.bin",
    "state": "out/instance-1/state/memory.bin",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/instance-1/overlay/vmlinux",
    "state": "out/instance-1/state/vmlinux",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/instance-1/overlay/rootfs.ext4",
    "state": "out/instance-1/state/rootfs.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/instance-1/overlay/config.json",
    "state": "out/instance-1/state/config.json",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/instance-1/overlay/oci.ext4",
    "state": "out/instance-1/state/oci.ext4",
    "blockSize": 65536,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": false,
    "s3sync": false,
    "s3accesskey": "",
    "s3secretkey": "",
    "s3endpoint": "",
    "s3secure": false,
    "s3bucket": "",
    "s3concurrency": 0
  }
]'
```

During the migration, on the first instance, you should see logs like the following right before the first process exits:

```plaintext
# ...
2024/06/24 13:55:16 Migrated 16384 of 16384 initial blocks for local device 4
2024/06/24 13:55:16 Completed migration of local device 4
2024/06/24 13:55:16 Completed all device migrations
2024/06/24 13:55:16 Shutting down
```

On the destination, after all devices have been migrated, you should see logs like this which indicate that the migration has completed successfully:

```plaintext
# ...
2024/06/24 13:55:16 Resumed VM in 60.026997ms on out/vms/firecracker/39zY39Y9Gs8N4wPMs5WoLe/root
```

**🚀 That's it!** You've successfully created, snapshotted, started and live migrated your first service with Drafter. We can't wait to see what you're going to build next! Be sure to take a look at the [reference](#reference) (the [registry](#registry), [mounter](#mounter) and [terminator](#terminator) components can be quite helpful when working with live migration), [examples](#examples) and [frequently asked questions](#faq) for more information.

</details>

## Reference

### Examples

To make getting started with Drafter as a library easier, take a look at the following examples:

- [**NAT**](./cmd/drafter-nat/main.go): Enables guest-to-host networking
- [**Forwarder**](./cmd/drafter-forwarder/main.go): Enables local port-forwarding/host-to-guest networking
- [**Agent**](./cmd/drafter-agent/main.go) and [**Liveness**](./cmd/drafter-liveness/main.go): Allow responding to snapshot and suspend/resume events within the guest
- [**Snapshotter**](./cmd/drafter-snapshotter/main.go): Creates snapshots/VM packages from blueprints
- [**Packager**](./cmd/drafter-packager/main.go): Packages VM instances into distributable packages
- [**Runner**](./cmd/drafter-runner/main.go): Starts VM instances from packages locally
- [**Registry**](./cmd/drafter-registry/main.go): Distributes VM packages across the network
- [**Mounter**](./cmd/drafter-mounter/main.go): Allows files and devices to be re-used between VMs and moved without migrating the VM using them
- [**Peer**](./cmd/drafter-peer/main.go): Live migrates VM instances across the network
- [**Terminator**](./cmd/drafter-terminator/main.go): Handles backup operations for VMs

### Command Line Arguments

<details>
  <summary>Expand command reference</summary>

#### NAT

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
    	Gateway for the interface inside the namespace (default "172.16.0.1")
  -namespace-interface-ip string
    	IP for the interface inside the namespace (default "172.16.0.2")
  -namespace-interface-mac string
    	MAC address for the interface inside the namespace (default "02:0e:d9:fd:68:3d")
  -namespace-interface-netmask uint
    	Netmask for the interface inside the namespace (default 30)
  -namespace-prefix string
    	Prefix for the namespace IDs (default "ark")
  -namespace-veth-cidr string
    	CIDR for the veths inside the namespace (default "10.0.15.0/24")
```

#### Forwarder

```shell
$ drafter-forwarder --help
Usage of drafter-forwarder:
  -host-veth-cidr string
    	CIDR for the veths outside the namespace (default "10.0.8.0/22")
  -port-forwards string
    	Port forwards configuration (wildcard IPs like 0.0.0.0 are not valid, be explicit) (default "[{\"netns\":\"ark0\",\"internalPort\":\"6379\",\"protocol\":\"tcp\",\"externalAddr\":\"127.0.0.1:3333\"}]")
```

#### Agent

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

#### Liveness

```shell
$ drafter-liveness --help
Usage of drafter-liveness:
  -vsock-port int
    	VSock port (default 25)
  -vsock-timeout duration
    	VSock dial timeout (default 1m0s)
```

#### Snapshotter

```shell
$ drafter-snapshotter --help
Usage of drafter-snapshotter:
  -agent-vsock-port int
    	Agent VSock port (default 26)
  -boot-args string
    	Boot/kernel arguments (default "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0 clocksource=tsc nokaslr lapic=notscdeadline tsc=unstable")
  -cgroup-version int
    	Cgroup version to use for Jailer (default 2)
  -chroot-base-dir string
    	chroot base directory (default "out/vms")
  -cpu-count int
    	CPU count (default 1)
  -cpu-template string
    	Firecracker CPU template (see https://github.com/firecracker-microvm/firecracker/blob/main/docs/cpu_templates/cpu-templates.md#static-cpu-templates for the options) (default "None")
  -devices string
    	Devices configuration (default "[{\"name\":\"kernel\",\"input\":\"\",\"output\":\"out/blueprint/vmlinux\"},{\"name\":\"disk\",\"input\":\"\",\"output\":\"out/blueprint/rootfs.ext4\"},{\"name\":\"state\",\"input\":\"\",\"output\":\"out/package/state.bin\"},{\"name\":\"memory\",\"input\":\"\",\"output\":\"out/package/memory.bin\"},{\"name\":\"config\",\"input\":\"\",\"output\":\"out/package/config.json\"}]")
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

#### Packager

```shell
$ drafter-packager --help
Usage of drafter-packager:
  -devices string
    	Devices configuration (default "[{\"name\":\"kernel\",\"path\":\"out/package/vmlinux\"},{\"name\":\"disk\",\"path\":\"out/package/rootfs.ext4\"},{\"name\":\"state\",\"path\":\"out/package/state.bin\"},{\"name\":\"memory\",\"path\":\"out/package/memory.bin\"},{\"name\":\"config\",\"path\":\"out/package/config.json\"}]")
  -extract
    	Whether to extract or archive
  -package-path string
    	Path to package file (default "out/app.tar.zst")
```

#### Runner

```shell
$ drafter-runner --help
Usage of drafter-runner:
  -cgroup-version int
    	Cgroup version to use for Jailer (default 2)
  -chroot-base-dir string
    	chroot base directory (default "out/vms")
  -devices string
    	Devices configuration (default "[{\"name\":\"kernel\",\"path\":\"out/package/vmlinux\",\"shared\":false},{\"name\":\"disk\",\"path\":\"out/package/rootfs.ext4\",\"shared\":false},{\"name\":\"state\",\"path\":\"out/package/state.bin\",\"shared\":false},{\"name\":\"memory\",\"path\":\"out/package/memory.bin\",\"shared\":false},{\"name\":\"config\",\"path\":\"out/package/config.json\",\"shared\":false}]")
  -enable-input
    	Whether to enable VM stdin
  -enable-output
    	Whether to enable VM stdout and stderr (default true)
  -experimental-map-private
    	(Experimental) Whether to use MAP_PRIVATE for memory and state devices
  -experimental-map-private-memory-output string
    	(Experimental) Path to write the local changes to the shared memory to (leave empty to write back to device directly) (ignored unless --experimental-map-private)
  -experimental-map-private-state-output string
    	(Experimental) Path to write the local changes to the shared state to (leave empty to write back to device directly) (ignored unless --experimental-map-private)
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

#### Registry

```shell
$ drafter-registry --help
Usage of drafter-registry:
  -concurrency int
    	Number of concurrent workers to use in migrations (default 1024)
  -devices string
    	Devices configuration (default "[{\"name\":\"kernel\",\"input\":\"out/package/vmlinux\",\"blockSize\":65536},{\"name\":\"disk\",\"input\":\"out/package/rootfs.ext4\",\"blockSize\":65536},{\"name\":\"state\",\"input\":\"out/package/state.bin\",\"blockSize\":65536},{\"name\":\"memory\",\"input\":\"out/package/memory.bin\",\"blockSize\":65536},{\"name\":\"config\",\"input\":\"out/package/config.json\",\"blockSize\":65536}]")
  -laddr string
    	Address to listen on (default ":1600")
```

#### Mounter

```shell
$ drafter-mounter --help
Usage of drafter-mounter:
  -concurrency int
    	Number of concurrent workers to use in migrations (default 1024)
  -devices string
    	Devices configuration (default "[{\"name\":\"state\",\"base\":\"out/package/state.bin\",\"overlay\":\"out/overlay/state.bin\",\"state\":\"out/state/state.bin\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true},{\"name\":\"memory\",\"base\":\"out/package/memory.bin\",\"overlay\":\"out/overlay/memory.bin\",\"state\":\"out/state/memory.bin\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true},{\"name\":\"kernel\",\"base\":\"out/package/vmlinux\",\"overlay\":\"out/overlay/vmlinux\",\"state\":\"out/state/vmlinux\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true},{\"name\":\"disk\",\"base\":\"out/package/rootfs.ext4\",\"overlay\":\"out/overlay/rootfs.ext4\",\"state\":\"out/state/rootfs.ext4\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true},{\"name\":\"config\",\"base\":\"out/package/config.json\",\"overlay\":\"out/overlay/config.json\",\"state\":\"out/state/config.json\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true},{\"name\":\"oci\",\"base\":\"out/package/oci.ext4\",\"overlay\":\"out/overlay/oci.ext4\",\"state\":\"out/state/oci.ext4\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true}]")
  -laddr string
    	Local address to listen on (leave empty to disable) (default "localhost:1337")
  -raddr string
    	Remote address to connect to (leave empty to disable) (default "localhost:1337")
```

#### Peer

```shell
$ drafter-peer --help
Usage of drafter-peer:
  -cgroup-version int
    	Cgroup version to use for Jailer (default 2)
  -chroot-base-dir string
    	chroot base directory (default "out/vms")
  -concurrency int
    	Number of concurrent workers to use in migrations (default 1024)
  -devices string
    	Devices configuration (default "[{\"name\":\"state\",\"base\":\"out/package/state.bin\",\"overlay\":\"out/overlay/state.bin\",\"state\":\"out/state/state.bin\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true,\"shared\":false,\"sharedbase\":false,\"s3sync\":false,\"s3accesskey\":\"\",\"s3secretkey\":\"\",\"s3endpoint\":\"\",\"s3secure\":false,\"s3bucket\":\"\",\"s3concurrency\":0},{\"name\":\"memory\",\"base\":\"out/package/memory.bin\",\"overlay\":\"out/overlay/memory.bin\",\"state\":\"out/state/memory.bin\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true,\"shared\":false,\"sharedbase\":false,\"s3sync\":false,\"s3accesskey\":\"\",\"s3secretkey\":\"\",\"s3endpoint\":\"\",\"s3secure\":false,\"s3bucket\":\"\",\"s3concurrency\":0},{\"name\":\"kernel\",\"base\":\"out/package/vmlinux\",\"overlay\":\"out/overlay/vmlinux\",\"state\":\"out/state/vmlinux\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true,\"shared\":false,\"sharedbase\":false,\"s3sync\":false,\"s3accesskey\":\"\",\"s3secretkey\":\"\",\"s3endpoint\":\"\",\"s3secure\":false,\"s3bucket\":\"\",\"s3concurrency\":0},{\"name\":\"disk\",\"base\":\"out/package/rootfs.ext4\",\"overlay\":\"out/overlay/rootfs.ext4\",\"state\":\"out/state/rootfs.ext4\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true,\"shared\":false,\"sharedbase\":false,\"s3sync\":false,\"s3accesskey\":\"\",\"s3secretkey\":\"\",\"s3endpoint\":\"\",\"s3secure\":false,\"s3bucket\":\"\",\"s3concurrency\":0},{\"name\":\"config\",\"base\":\"out/package/config.json\",\"overlay\":\"out/overlay/config.json\",\"state\":\"out/state/config.json\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true,\"shared\":false,\"sharedbase\":false,\"s3sync\":false,\"s3accesskey\":\"\",\"s3secretkey\":\"\",\"s3endpoint\":\"\",\"s3secure\":false,\"s3bucket\":\"\",\"s3concurrency\":0},{\"name\":\"oci\",\"base\":\"out/package/oci.ext4\",\"overlay\":\"out/overlay/oci.ext4\",\"state\":\"out/state/oci.ext4\",\"blockSize\":65536,\"expiry\":1000000000,\"maxDirtyBlocks\":200,\"minCycles\":5,\"maxCycles\":20,\"cycleThrottle\":500000000,\"makeMigratable\":true,\"shared\":false,\"sharedbase\":false,\"s3sync\":false,\"s3accesskey\":\"\",\"s3secretkey\":\"\",\"s3endpoint\":\"\",\"s3secure\":false,\"s3bucket\":\"\",\"s3concurrency\":0}]")
  -disable-postcopy-migration
    	Whether to disable post-copy migration
  -enable-input
    	Whether to enable VM stdin
  -enable-output
    	Whether to enable VM stdout and stderr (default true)
  -experimental-map-private
    	(Experimental) Whether to use MAP_PRIVATE for memory and state devices
  -experimental-map-private-memory-output string
    	(Experimental) Path to write the local changes to the shared memory to (leave empty to write back to device directly) (ignored unless --experimental-map-private)
  -experimental-map-private-state-output string
    	(Experimental) Path to write the local changes to the shared state to (leave empty to write back to device directly) (ignored unless --experimental-map-private)
  -firecracker-bin string
    	Firecracker binary (default "firecracker")
  -gid int
    	Group ID for the Firecracker process
  -jailer-bin string
    	Jailer binary (from Firecracker) (default "jailer")
  -laddr string
    	Local address to listen on (leave empty to disable) (default "localhost:1337")
  -metrics string
    	Address to serve metrics from
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

#### Terminator

```shell
$ drafter-terminator --help
Usage of drafter-terminator:
  -devices string
    	Devices configuration (default "[{\"name\":\"kernel\",\"output\":\"out/package/vmlinux\"},{\"name\":\"disk\",\"output\":\"out/package/rootfs.ext4\"},{\"name\":\"state\",\"output\":\"out/package/state.bin\"},{\"name\":\"memory\",\"output\":\"out/package/memory.bin\"},{\"name\":\"config\",\"output\":\"out/package/config.json\"}]")
  -raddr string
    	Remote address to connect to (default "localhost:1337")
```

</details>

## FAQ

### How Can I Embed Drafter in My Application?

Integrating Drafter into your Go project is straightforward; it's a pure Go library that does not require CGo. To begin, you can add Drafter by running the following:

```shell
$ go get github.com/loopholelabs/drafter/...@latest
```

The [`pkg`](https://pkg.go.dev/github.com/loopholelabs/drafter/pkg) package includes all the necessary functionality for most usage scenarios. For practical implementation examples, refer to [examples](#examples).

### How Can I Add Additional Disks to My VM Instance?

Adding additional disks to a VM instance is as easy as adding them to the `--devices` flag for the snapshotter, runner or peer. For example, to add a disk at `/tmp/mydisk.ext4` to the peer, add the following to `--devices`:

```json
{
  "name": "mydisk",
  "base": "tmp/mydisk.ext4",
  "overlay": "out/instance-0/overlay/mydisk.ext4",
  "state": "out/instance-0/state/mydisk.ext4",
  "blockSize": 65536,
  "expiry": 1000000000,
  "maxDirtyBlocks": 200,
  "minCycles": 5,
  "maxCycles": 20,
  "cycleThrottle": 500000000,
  "makeMigratable": true,
  "shared": false,
  "sharedbase": false,
  "s3sync": false,
  "s3accesskey": "",
  "s3secretkey": "",
  "s3endpoint": "",
  "s3secure": false,
  "s3bucket": "",
  "s3concurrency": 0
},
```

Disks aren't mounted automatically; to find and mount them, use the `lsblk` and `mount` commands in the guest VM. If you want to mount the disks at boot time, you can modify `/etc/fstab` and add a line like so (assuming that the disk has the label `mydisk`):

```shell
LABEL=mydisk    /mymount    ext4    defaults    0    2
```

### How Can I Add Additional VSocks to My VM?

Using additional VSocks requires getting access to the runner's `VMPath`, which is available at `${runner.VMPath}/${snapshotter.VSockName}` and `${peer.VMPath}/${snapshotter.VSockName}`. Other than that, refer to the [Firecracker docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md) and [examples](#examples) for more information on how to use Firecracker's VSock-via-UNIX socket implementation.

### Does Drafter Support IPv6?

Currently, Drafter's NAT only supports IPv4. IPv6 support is planned. If you need IPv6 support now, use a reverse proxy like `socat`, Traefik, HAProxy or Envoy that binds to an IPv6 address on the host and forwards traffic to Drafter's forwarded port over IPv4.

### Does Drafter Support GPUs?

Drafter doesn't support GPUs at this time [Firecracker does not support GPU passthrough](https://github.com/firecracker-microvm/firecracker/issues/1179). Unless Firecracker implements GPU passthrough, there is no way to directly expose a GPU to the guest. Alternatives include porting Drafter to support [Cloud Hypervisor, which has GPU passthrough support](https://fly.io/blog/fly-io-has-gpus-now/), or if a GPU is required as a graphical device, to use [LLVMpipe](https://docs.mesa3d.org/drivers/llvmpipe.html) and a headless Wayland compositor like [Cage](https://github.com/cage-kiosk/cage) and [wayvnc](https://github.com/any1/wayvnc).

### How Can I Authenticate Live Migrations or Migrate over Another Network Protocol like TLS or a UNIX socket?

Drafter doesn't have a preferred authentication or transport mechanism; it instead relies on an external implementation of an `io.ReadWriter` to function. This `io.ReadWriter` can be implemented and authenticated by a TCP connection, UDP connection, TLS connection, WebSocket connection, UNIX socket or any other protocol of choice. See [examples](#examples) for more information on how to use specific transports for live migrations, e.g. TCP. If you need built-in authentication or a network solution that integrates with your existing environment, check out [Loophole Labs Architect](https://architect.run/).

### How Can I Make a VM Migratable after Starting It?

The `drafter-peer` CLI only allows for a static configuration; if you supply a `--laddr`, the instance will automatically become migratable after resuming. If you wish to make a VM migratable at a specific point, or make it non-migratable, see [How Can I Embed Drafter in My Application?](#how-can-i-embed-drafter-in-my-application) to use the peer API directly, or check out [Loophole Labs Architect](https://architect.run/) for a solution with a built-in control plane.

### How Can I Keep My Network Connections Alive While Live Migrating?

Drafter doesn't concern itself with networking aside from its simple NAT and port forwarding implementations. If you're interested in a more full-fledged, production-ready networking solution with support for advanced networking rules and zero-downtime live migrations of network connections, check out [Loophole Labs Architect](https://architect.run/).

### How Can I Change the Environment Variables, Volume Mounts or Startup Command of the OCI Image to Start?

Drafter doesn't work with OCI images; instead, it works directly with [OCI runtime bundles](https://github.com/opencontainers/runtime-spec/blob/main/bundle.md), which can be created from an OCI image with commands such as `podman create` or `umoci unpack`. These OCI runtime bundles are then copied to an EXT4 file system and passed to an OCI runtime in the guest VM. Drafter includes a minimal implementation of the OCI image to OCI runtime bundle conversion process (see [Building a VM Blueprint Locally](#building-a-vm-blueprint-locally)) and utility targets such as `make unpack/oci` and `make pack/oci`, which allow you to unpack an OCI image to an OCI runtime bundle, adjust the OCI `config.json` file (at `out/oci-runtime-bundle/config.json`), and then pack the OCI image to an EXT4 file system.

Drafter doesn't concern itself with the actual process of building the underlying VM images aside from this simple build tooling. This is because it also supports starting any other Linux distribution without any OCI integration, such as a Valkey instance running directly in the guest operating system, or running a full-fledged Docker daemon in the guest. If you're looking for a more advanced and streamlined process, like streaming conversion and startup of OCI images, a way to replicate/distribute packages or a build service, check out [Loophole Labs Architect](https://architect.run/).

## Acknowledgements

- [Loophole Labs Silo](https://github.com/loopholelabs/silo) provides the storage and data migration framework.
- [coreos/go-iptables](https://github.com/coreos/go-iptables) provides the IPTables bindings for port-forwarding.
- [pojntfx/panrpc](https://github.com/pojntfx/panrpc) provides the RPC framework for local host ↔ guest communication.
- [Font Awesome](https://fontawesome.com/) provides the assets used for the icon and logo.

## Contributing

Bug reports and pull requests are welcome on GitHub at [https://github.com/loopholelabs/drafter](https://github.com/loopholelabs/drafter). For more contribution information check out [the contribution guide](./CONTRIBUTING.md).

## License

The Drafter project is available as open source under the terms of the [GNU Affero General Public License, Version 3](https://www.gnu.org/licenses/agpl-3.0.en.html).

## Code of Conduct

Everyone interacting in the Drafter project's codebases, issue trackers, chat rooms and mailing lists is expected to follow the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/master/code-of-conduct.md).

## Project Managed By:

[![https://loopholelabs.io](https://cdn.loopholelabs.io/loopholelabs/LoopholeLabsLogo.svg)](https://loopholelabs.io)
