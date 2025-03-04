#!/bin/bash

set -ex

sudo drafter-peer --netns ark1 --raddr 'localhost:1337' --laddr '' --metrics 'localhost:2113' --devices '[
  {
    "name": "state",
    "base": "out/package/state.bin",
    "overlay": "out/instance-1/overlay/state.bin",
    "state": "out/instance-1/state/state.bin",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false
  },
  {
    "name": "memory",
    "base": "out/package/memory.bin",
    "overlay": "out/instance-1/overlay/memory.bin",
    "state": "out/instance-1/state/memory.bin",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false
  },
  {
    "name": "kernel",
    "base": "out/package/vmlinux",
    "overlay": "out/instance-1/overlay/vmlinux",
    "state": "out/instance-1/state/vmlinux",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false
  },
  {
    "name": "disk",
    "base": "out/package/rootfs.ext4",
    "overlay": "out/instance-1/overlay/rootfs.ext4",
    "state": "out/instance-1/state/rootfs.ext4",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false
  },
  {
    "name": "config",
    "base": "out/package/config.json",
    "overlay": "out/instance-1/overlay/config.json",
    "state": "out/instance-1/state/config.json",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false
  },
  {
    "name": "oci",
    "base": "out/package/oci.ext4",
    "overlay": "out/instance-1/overlay/oci.ext4",
    "state": "out/instance-1/state/oci.ext4",
    "blockSize": 1048576,
    "expiry": 1000000000,
    "maxDirtyBlocks": 200,
    "minCycles": 5,
    "maxCycles": 20,
    "cycleThrottle": 500000000,
    "makeMigratable": true,
    "shared": false
  }
]'
