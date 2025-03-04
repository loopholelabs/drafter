#!/bin/bash

set -ex

sudo drafter-peer --netns ark0 --raddr '' --laddr 'localhost:1337' --metrics 'localhost:2112' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/instance-0/overlay/state.bin",
    "state": "out/instance-0/state/state.bin",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 2,
    "maxCycles": 3,
    "cycleThrottle": 100000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": true
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/instance-0/overlay/memory.bin",
    "state": "out/instance-0/state/memory.bin",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 2,
    "maxCycles": 3,
    "cycleThrottle": 100000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": true
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/instance-0/overlay/vmlinux",
    "state": "out/instance-0/state/vmlinux",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 2,
    "maxCycles": 3,
    "cycleThrottle": 100000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": true
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/instance-0/overlay/rootfs.ext4",
    "state": "out/instance-0/state/rootfs.ext4",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 2,
    "maxCycles": 3,
    "cycleThrottle": 100000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": true
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/instance-0/overlay/config.json",
    "state": "out/instance-0/state/config.json",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 2,
    "maxCycles": 3,
    "cycleThrottle": 100000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": true
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/instance-0/overlay/oci.ext4",
    "state": "out/instance-0/state/oci.ext4",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 2,
    "maxCycles": 3,
    "cycleThrottle": 100000000,
    "makeMigratable": true,
    "shared": false,
    "sharedbase": true
  }
]'
