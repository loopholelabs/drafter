## FAQ

### How Can I Embed Drafter in My Application?

Integrating Drafter into your Go project is straightforward; it's a pure Go library that does not require CGo. To begin, you can add Drafter by running the following:

```shell
$ go get github.com/loopholelabs/drafter/...@latest
```

The [`pkg`](https://pkg.go.dev/github.com/loopholelabs/drafter/pkg) package includes all the necessary functionality for most usage scenarios. For practical implementation examples, refer to the tests.

### How Can I Add Additional Disks to My VM Instance?

Adding additional disks to a VM instance is as easy as adding them to the `--devices` flag for the snapshotter, or peer. For example, to add a disk at `/tmp/mydisk.ext4` to the peer, add the following to `--devices`:

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

Drafter doesn't have a preferred authentication or transport mechanism; it instead relies on an external implementation of an `io.ReadWriter` to function. This `io.ReadWriter` can be implemented and authenticated by a TCP connection, UDP connection, TLS connection, WebSocket connection, UNIX socket or any other protocol of choice. If you need built-in authentication or a network solution that integrates with your existing environment, check out [Loophole Labs Architect](https://architect.run/).

### How Can I Make a VM Migratable after Starting It?

If you supply a `--laddr` to `drafter-peer`, the instance will automatically become migratable after resuming. If you wish to make a VM migratable at a specific point, or make it non-migratable, see [How Can I Embed Drafter in My Application?](#how-can-i-embed-drafter-in-my-application) to use the peer API directly, or check out [Loophole Labs Architect](https://architect.run/) for a solution with a built-in control plane.

### How Can I Keep My Network Connections Alive While Live Migrating?

Drafter doesn't concern itself with networking aside from its simple NAT and port forwarding implementations. If you're interested in a more full-fledged, production-ready networking solution with support for advanced networking rules and zero-downtime live migrations of network connections, check out [Loophole Labs Architect](https://architect.run/).

### How Can I Change the Environment Variables, Volume Mounts or Startup Command of the OCI Image to Start?

Drafter doesn't work with OCI images; instead, it works directly with [OCI runtime bundles](https://github.com/opencontainers/runtime-spec/blob/main/bundle.md), which can be created from an OCI image with commands such as `podman create` or `umoci unpack`. These OCI runtime bundles are then copied to an EXT4 file system and passed to an OCI runtime in the guest VM. Drafter includes a minimal implementation of the OCI image to OCI runtime bundle conversion process (see [Building a VM Blueprint Locally](#building-a-vm-blueprint-locally)) and utility targets such as `make unpack/oci` and `make pack/oci`, which allow you to unpack an OCI image to an OCI runtime bundle, adjust the OCI `config.json` file (at `out/oci-runtime-bundle/config.json`), and then pack the OCI image to an EXT4 file system.

Drafter doesn't concern itself with the actual process of building the underlying VM images aside from this simple build tooling. This is because it also supports starting any other Linux distribution without any OCI integration, such as a Valkey instance running directly in the guest operating system, or running a full-fledged Docker daemon in the guest. If you're looking for a more advanced and streamlined process, like streaming conversion and startup of OCI images, a way to replicate/distribute packages or a build service, check out [Loophole Labs Architect](https://architect.run/).
