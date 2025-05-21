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

To do some valkey performance tests, you can grab the valkey oci from drafter downloads, and run something like the following.

`go run -exec=sudo ./cmd/drafter-perf/ --bluedir out/blueprint/ --valkey --cpus 4 --memory 4096 --nosilo --silowc --valkeynum 100000`

### General oci with listen on 4568 for job start and 4567 for job complete

This will start a workload, and wait until it signals that the workload has completed.

`go run -exec=sudo ./cmd/drafter-perf/ --bluedir testoci --cpus 4 --memory 4096 --nosilo --silowc`

## Issues

At the moment if there's a failure, sometimes networking is not tidied up.
Steps to cleanup

* killall drafter-perf
* killall firecracker
* sudo iptables-restore iptables-file
* ip netns list | grep dra | awk '{system("ip netns delete " $1)}'
* ip route list | grep dra | awk '{system("ip route delete " $1)}'
* sudo rm -rf testdir
