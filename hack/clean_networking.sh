#!/bin/bash

killall drafter-perf
killall firecracker

ip netns list | grep dra | awk '{system("ip netns delete " $1)}'
ip route list | grep dra | awk '{system("ip route delete " $1)}'

iptables-restore ipt
rm -rf testdir snapdir
