# Drafter perf tester tool

## Setup

Firstly, get some blueprints. These should be placed in a directory together, and should be named `vmlinux`, `rootfs.ext4` and `oci.ext4`.

You can download the first two from Drafter releases (choose from PVM or non PVM version).

## Prerequisites

* nbd kernel module loaded (To run silo)
* firecracker and jailer
* kvm or pvm

## Example run

### Valkey

`go run -exec=sudo ./cmd/drafter-perf/ --bluedir out/blueprint/ --valkey --cpus 4 --memory 4096 --nosilo --silowc --valkeynum 100000`

### General oci with listen on 4567 for job complete

`go run -exec=sudo ./cmd/drafter-perf/ --bluedir testoci --cpus 4 --memory 4096 --nosilo --silowc`

## Issues

At the moment if there's a failure, sometimes networking is not tidied up.
Steps to cleanup

* killall firecracker
* sudo iptables-restore iptables-file
* sudo ip netns delete dra0 dra1
* sudo ip route delete ... some of the lines ...
* sudo rm -rf testdir
