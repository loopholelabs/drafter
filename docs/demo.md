# Architekt Demo

## Installing Firecracker and Architekt

```shell
git clone https://github.com/loopholelabs/firecracker /tmp/firecracker
cd /tmp/firecracker

git remote add upstream https://github.com/firecracker-microvm/firecracker
git fetch --all

tools/devtool build --release

sudo install ./build/cargo_target/x86_64-unknown-linux-musl/release/{firecracker,jailer} /usr/local/bin/
```

```shell
git clone https://github.com/loopholelabs/architekt.git /tmp/architekt
cd /tmp/architekt

make depend
make
sudo make install
```

## Setting Up Workstation and Server Dependencies

```shell
grep -Eoc '(vmx)' /proc/cpuinfo # Intel: Must be > 0 for KVM support
grep -Eoc '(svm)' /proc/cpuinfo # AMD: Must be > 0 for KVM support

sudo modprobe kvm
sudo tee /etc/modules-load.d/kvm.conf <<EOT
kvm
EOT
```

```shell
sudo modprobe nbd
sudo tee /etc/modules-load.d/nbd.conf <<EOT
nbd
EOT
```

```shell
sudo architekt-daemon # Sets up networking and keeps running; CTRL-C to tear down networking
```

## Build a Blueprint on a Workstation

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

curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/loopholelabs/firecracker/live-migration-1.4/resources/guest_configs/microvm-kernel-x86_64-5.10.config
# curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-ci-x86_64-5.10.config

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

## Creating and Running a Blueprint on a Workstation

```shell
sudo architekt-packager
sudo architekt-runner # CTRL-C to flush the snapshot and run again to resume
```

## Distributing, Running and Migrating Packages

### On a Workstation

```shell
architekt-registry
```

```shell
sudo architekt-peer --netns ark0 --raddr localhost:1337 --enable-input # If --enable-input is specified, CTRL-C is forwarded to the VM, so to stop the VM use `sudo pkill -2 architekt-peer` instead (be sure to use a free namespace)
```

```shell
sudo architekt-peer --netns ark1 --enable-input # Migrates to this peer; be sure to use a different namespace (i.e. ark1) since you're migrating on the same machine
```

```shell
sudo architekt-peer --netns ark0 # Migrates to this peer without enabling input; CTRL-C to flush the snapshot and stop the VM (be sure to use a free namespace)
```

### On a Cluster

```shell
export REGISTRY_IP="186.233.186.43"
export NODE_1_IP="186.233.186.43"
export NODE_2_IP="160.202.128.189"
```

```shell
architekt-registry # On ${REGISTRY_IP}
```

```shell
sudo architekt-peer --netns ark0 --raddr ${REGISTRY_IP}:1337 --enable-input # On ${NODE_1_IP}: If --enable-input is specified, CTRL-C is forwarded to the VM, so to stop the VM use `sudo pkill -2 architekt-peer` instead (be sure to use a free namespace)
```

```shell
sudo architekt-peer --netns ark0 --raddr ${NODE_1_IP}:1337 --enable-input # On ${NODE_2_IP}: Migrates to this peer (be sure to use a free namespace)
```

```shell
sudo architekt-peer --netns ark0 --raddr ${NODE_2_IP}:1338 # On ${NODE_1_IP}: Migrates to this peer without enabling input; CTRL-C to flush the snapshot and stop the VM (be sure to use a free namespace)
```

## Using the Control Plane

### On a Workstation

```shell
architekt-registry
```

```shell
architekt-manager --verbose
```

```shell
sudo architekt-worker --verbose --host-interface wlp0s20f3
```

```shell
export NODE_ID=$(curl -v http://localhost:1400/nodes | jq -r .[0])

curl -v http://localhost:1400/nodes/${NODE_ID}/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://localhost:1400/nodes/${NODE_ID}/instances/localhost:1337 | jq -r) # Create VM

curl -v http://localhost:1400/nodes/${NODE_ID}/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://localhost:1400/nodes/${NODE_ID}/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM

curl -v http://localhost:1400/nodes/${NODE_ID}/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://localhost:1400/nodes/${NODE_ID}/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM again

curl -v http://localhost:1400/nodes/${NODE_ID}/instances | jq

curl -v -X DELETE http://localhost:1400/nodes/${NODE_ID}/instances/${PACKAGE_RADDR} # Delete VM

curl -v http://localhost:1400/nodes/${NODE_ID}/instances | jq
```

### On a Cluster

```shell
export REGISTRY_IP="186.233.186.43"
export CONTROL_PLANE_IP="186.233.186.43"
export NODE_1_IP="186.233.186.43"
export NODE_2_IP="160.202.128.189"
```

```shell
architekt-registry # On ${REGISTRY_IP}
```

```shell
architekt-manager --verbose # On ${CONTROL_PLANE_IP}
```

```shell
sudo architekt-worker --verbose --host-interface enp1s0f0 --ahost ${NODE_1_IP} --control-plane-raddr ${CONTROL_PLANE_IP}:1399 # On ${NODE_1_IP}
```

```shell
sudo architekt-worker --verbose --host-interface enp1s0f0 --ahost ${NODE_2_IP} --control-plane-raddr ${CONTROL_PLANE_IP}:1399 # On ${NODE_2_IP}
```

```shell
readarray -t NODE_IDS < <(curl -s http://${CONTROL_PLANE_IP}:1400/nodes | jq -r '.[]') && export NODE_ID_1=${NODE_IDS[0]} && export NODE_ID_2=${NODE_IDS[1]}

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_2}/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances/${REGISTRY_IP}:1337 | jq -r) # Create VM on node 1

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_2}/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_2}/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM from node 1 to node 2

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_2}/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM from node 2 to node 1

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_2}/instances | jq

curl -v -X DELETE http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances/${PACKAGE_RADDR} # Delete VM from node 2

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_1}/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/${NODE_ID_2}/instances | jq
```

## Tearing Down Workstation and Server Dependencies

```shell
sudo pkill -2 architekt-peer
sudo pkill -2 architekt-registry
sudo pkill -2 architekt-daemon
sudo pkill -2 architekt-worker

# Completely resetting the network configuration (should not be necessary)
sudo iptables -X
sudo iptables -F
for ns in $(ip netns list | awk '{print $1}'); do
    sudo ip netns delete $ns
done

# Completely cleaning up artifacts from failed runs (should not be necessary)
sudo pkill -9 firecracker
sudo umount out/redis.ark
sudo rm -f out/redis.ark
sudo rm -rf out/vms
sudo rm -rf out/cache
```

## Uninstalling Firecracker and Architekt

```shell
sudo rm /usr/local/bin/{firecracker,jailer}

cd /tmp/architekt
sudo make uninstall
```
