# Architekt Demo

## Overview

1. **Installing Firecracker and Architekt**: Builds Firecracker and Architekt binaries from source and installs them in bash
2. **Setting Up Workstation and Server Dependencies**: Checking for KVM, NBD and availability of Firecracker, pre-creating networks (all in Bash/`architekt-network-setup`, and in the future in the single-instance-per-host run-once DaemonSet `architekt-daemon`'s startup hook)
3. **Building a Blueprint on a Workstation**: Custom shell script, in the future a Go CLI `architekt-builder` that converts an OCI image to a `.arkdisk` and adds a `.arkinitramfs` and a `.arkkernel`
4. **Creating and Running a Package on a Workstation**: Using `architekt-packager` to create a `.ark` package and running it with `architekt-runner`
5. **Distributing, Running and Migrating Packages between Servers**: Using `architekt-registry` to serve a `.ark` package and `architekt-peer` to download and migrate it
6. **Tearing Down Workstation and Server Dependencies**: Stopping running Firecracker/`architekt-runner`/`architekt-peer` processes, deleting networks (all in Bash/`architekt-network-teardown`, and in the future in the single-instance-per-host run-once DaemonSet `architekt-daemon`'s shutdown hook)
7. **Uninstalling Firecracker and Architekt**: Removes Firecracker and Architekt binaries and data

## Setting up (with Live Migration Support), NBD and Networking

```shell
# Firecracker
git clone https://github.com/loopholelabs/firecracker /tmp/firecracker
cd /tmp/firecracker

git remote add upstream https://github.com/firecracker-microvm/firecracker
git fetch --all

tools/devtool build --release

sudo install ./build/cargo_target/x86_64-unknown-linux-musl/release/{firecracker,jailer} /usr/local/bin/

# NBD
sudo modprobe nbd
sudo tee /etc/modules-load.d/nbd.conf <<EOT
nbd
EOT

# Networking
export GATEWAY_INTERFACE="wlp0s20f3"

# Completely reseting the network configuration (should not be necessary)
sudo systemctl stop firewalld || true
sudo iptables -X
sudo iptables -F

for ns in $(ip netns list | awk '{print $1}'); do
    sudo ip netns delete $ns
done

go build -o /tmp/architekt-daemon ./cmd/architekt-daemon/ && sudo /tmp/architekt-daemon # Ctrl-C to clean up
```

## Setting up Blueprint

```shell
export DISK_SIZE="5G"
export GATEWAY_IP="172.100.100.1"
export GUEST_CIDR="172.100.100.2/30"
export LIVENESS_VSOCK_PORT="25"
export AGENT_VSOCK_PORT="26"

rm -rf out/blueprint
mkdir -p out/blueprint

rm -rf /tmp/kernel
mkdir -p /tmp/kernel

curl -Lo /tmp/kernel.tar.xz https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.194.tar.xz
tar Jxvf /tmp/kernel.tar.xz --strip-components=1 -C /tmp/kernel

curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-x86_64-5.10.config

sh - <<'EOT'
cd /tmp/kernel

make -j$(nproc) vmlinux
EOT

cp /tmp/kernel/vmlinux out/blueprint/architekt.arkkernel

qemu-img create -f raw out/blueprint/architekt.arkdisk ${DISK_SIZE}
mkfs.ext4 out/blueprint/architekt.arkdisk

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

curl -Lo /tmp/rootfs.tar.gz https://dl-cdn.alpinelinux.org/alpine/v3.18/releases/x86_64/alpine-minirootfs-3.18.3-x86_64.tar.gz
tar zxvf /tmp/rootfs.tar.gz -C /tmp/blueprint

tee /tmp/blueprint/etc/resolv.conf <<'EOT'
nameserver 1.1.1.1
EOT

tee /tmp/blueprint/etc/network/interfaces <<EOT
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet static
    address ${GUEST_CIDR}
    gateway ${GATEWAY_IP}
EOT

sudo chroot /tmp/blueprint sh - <<'EOT'
apk add alpine-base util-linux linux-virt linux-virt-dev coreutils binutils grep bzip2 chrony redis redis-openrc
echo root:root | chpasswd

ln -s agetty /etc/init.d/agetty.ttyS0
echo ttyS0 >/etc/securetty
rc-update add agetty.ttyS0 default

sed -i 's/initstepslew/#initstepslew/g' /etc/chrony/chrony.conf
echo 'refclock PHC /dev/ptp0 poll 3 dpoll -2 offset 0' >> /etc/chrony/chrony.conf

rc-update add networking default
rc-update add chronyd default
rc-update add redis default
EOT

sudo cp /tmp/blueprint/boot/initramfs-virt out/blueprint/architekt.arkinitramfs
sudo chown ${USER} out/blueprint/architekt.arkinitramfs

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

CGO_ENABLED=0 go build -o /tmp/blueprint/usr/sbin/architekt-liveness ./cmd/architekt-liveness
CGO_ENABLED=0 go build -o /tmp/blueprint/usr/sbin/architekt-agent ./cmd/architekt-agent

tee /tmp/blueprint/etc/init.d/architekt-liveness <<EOT
#!/sbin/openrc-run

command="/usr/sbin/architekt-liveness"
command_args="--vsock-port ${LIVENESS_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net redis architekt-agent
}
EOT
chmod +x /tmp/blueprint/etc/init.d/architekt-liveness

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add architekt-liveness default
EOT

tee /tmp/blueprint/etc/init.d/architekt-agent <<EOT
#!/sbin/openrc-run

command="/usr/sbin/architekt-agent"
command_args="--vsock-port ${AGENT_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net redis
}
EOT
chmod +x /tmp/blueprint/etc/init.d/architekt-agent

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add architekt-agent default
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

## Starting Packager, Runner, Registry and Peer

```shell
sudo pkill -9 firecracker; sudo umount out/redis.ark; sudo rm -f out/redis.ark; sudo rm -rf out/vms # Cleaning up artifacts from potentially failed runs

go build -o /tmp/architekt-packager ./cmd/architekt-packager/ && sudo /tmp/architekt-packager

go build -o /tmp/architekt-runner ./cmd/architekt-runner/ && sudo /tmp/architekt-runner # You can now CTRL+C to flush the snapshot & run again to resume

go build -o /tmp/architekt-registry ./cmd/architekt-registry/ && sudo /tmp/architekt-registry

sudo pkill -9 firecracker; sudo pkill -9 architekt-peer; sudo rm -rf out/vms # Cleaning up artifacts from potentially failed runs

sudo mount -o remount,size=24G,noatime /tmp # Potentially increase /tmp disk space (where the r3map cache file is stored)

go build -o /tmp/architekt-peer ./cmd/architekt-peer/ && sudo /tmp/architekt-peer --raddr localhost:1337

go build -o /tmp/architekt-peer ./cmd/architekt-peer/ && sudo /tmp/architekt-peer --netns ark1 --enable-input # You can use this to interact with the VM, but do note that enabling input removes the ability to `CTRL-C` since signals are forwarded (use `pkill -2 architekt-peer` instead)

go build -o /tmp/architekt-peer ./cmd/architekt-peer/ && sudo /tmp/architekt-peer
```
