#!/bin/bash

cd cmd/drafter-agent
go build .
cd ../..

# Now insert it into the /out/blueprint

mkdir rootfs
sudo mount out/blueprint/rootfs.ext4 rootfs
sudo cp cmd/drafter-agent/drafter-agent rootfs/usr/bin/drafter-agent
sudo umount out/blueprint/rootfs.ext4
rmdir rootfs