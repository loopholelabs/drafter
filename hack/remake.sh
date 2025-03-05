#!/bin/bash

set -ex

make -j$(nproc)
sudo make install -j$(nproc)
sudo killall drafter-peer || true
sudo killall firecracker || true
sudo rm -rf out/instance-*

#mkdir out/instance-1
#mkdir out/instance-1/state
#mkdir out/instance-1/overlay
