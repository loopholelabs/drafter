# Architekt Demo

## Setting up Firecracker (with Live Migration Support)

```shell
git clone https://github.com/loopholelabs/firecracker /tmp/firecracker
cd /tmp/firecracker

cargo build --target x86_64-unknown-linux-gnu
sudo install ./build/cargo_target/x86_64-unknown-linux-gnu/debug/firecracker /usr/local/bin/
```

## Setting up Networking

```shell
export GATEWAY_INTERFACE="wlp0s20f3"
export BRIDGE_INTERFACE="firecracker0"
export BRIDGE_CIDR="192.168.233.1/24"

sudo systemctl stop firewalld || true
sudo iptables -X
sudo iptables -F
sudo ip link del ${BRIDGE_INTERFACE} type bridge || true

sudo ip link add name ${BRIDGE_INTERFACE} type bridge
sudo ip addr add ${BRIDGE_CIDR} dev ${BRIDGE_INTERFACE}
sudo ip link set dev ${BRIDGE_INTERFACE} up
sudo sysctl -w net.ipv4.ip_forward=1
sudo iptables --table nat --append POSTROUTING --out-interface ${GATEWAY_INTERFACE} -j MASQUERADE
sudo iptables --insert FORWARD --in-interface ${BRIDGE_INTERFACE} -j ACCEPT
```

## Setting up Template

```shell
export DISK_SIZE="5G"
export GATEWAY_IP="192.168.233.1"
export GUEST_CIDR="192.168.233.2/24"
export LIVENESS_VSOCK_CID="2"
export LIVENESS_VSOCK_PORT="25"
export AGENT_VSOCK_CID="3"
export AGENT_VSOCK_PORT="26"

rm -rf out/template
mkdir -p out/template

rm -rf /tmp/kernel
mkdir -p /tmp/kernel

curl -Lo /tmp/kernel.tar.xz https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.194.tar.xz
tar Jxvf /tmp/kernel.tar.xz --strip-components=1 -C /tmp/kernel

curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-x86_64-5.10.config

sh - <<'EOT'
cd /tmp/kernel

make -j$(nproc) vmlinux
EOT

cp /tmp/kernel/vmlinux out/template/architekt.kernel

qemu-img create -f raw out/template/architekt.disk ${DISK_SIZE}
mkfs.ext4 out/template/architekt.disk

sudo umount /tmp/template || true
rm -rf /tmp/template
mkdir -p /tmp/template

sudo mount out/template/architekt.disk /tmp/template
sudo chown ${USER} /tmp/template

curl -Lo /tmp/rootfs.tar.gz https://dl-cdn.alpinelinux.org/alpine/v3.18/releases/x86_64/alpine-minirootfs-3.18.3-x86_64.tar.gz
tar zxvf /tmp/rootfs.tar.gz -C /tmp/template

tee /tmp/template/etc/resolv.conf <<'EOT'
nameserver 1.1.1.1
EOT

tee /tmp/template/etc/network/interfaces <<EOT
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet static
    address ${GUEST_CIDR}
    gateway ${GATEWAY_IP}
EOT

sudo chroot /tmp/template sh - <<'EOT'
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

sudo cp /tmp/template/boot/initramfs-virt out/template/architekt.initramfs
sudo chown ${USER} out/template/architekt.initramfs

sync -f /tmp/template
sudo umount /tmp/template || true
rm -rf /tmp/template

sudo umount /tmp/template || true
rm -rf /tmp/template
mkdir -p /tmp/template

sudo mount out/template/architekt.disk /tmp/template
sudo chown ${USER} /tmp/template

CGO_ENABLED=0 go build -o /tmp/template/usr/sbin/architect-liveness ./cmd/architect-liveness
CGO_ENABLED=0 go build -o /tmp/template/usr/sbin/architect-agent ./cmd/architect-agent

tee /tmp/template/etc/init.d/architect-liveness <<EOT
#!/sbin/openrc-run

command="/usr/sbin/architect-liveness"
command_args="--vsock-cid ${LIVENESS_VSOCK_CID} --vsock-port ${LIVENESS_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net redis
}
EOT
chmod +x /tmp/template/etc/init.d/architect-liveness

sudo chroot /tmp/template sh - <<'EOT'
rc-update add architect-liveness default
EOT

tee /tmp/template/etc/init.d/architect-agent <<EOT
#!/sbin/openrc-run

command="/usr/sbin/architect-agent"
command_args="--vsock-cid ${AGENT_VSOCK_CID} --vsock-port ${AGENT_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net redis
}
EOT
chmod +x /tmp/template/etc/init.d/architect-agent

sudo chroot /tmp/template sh - <<'EOT'
rc-update add architect-agent default
EOT

sync -f /tmp/template
sudo umount /tmp/template || true
rm -rf /tmp/template
```

## Creating an Image

```shell
go build -o /tmp/architect-packager ./cmd/architect-packager/ && sudo /tmp/architect-packager
```

## Starting Manager and Worker

```shell
go build -o /tmp/architect-worker ./cmd/architect-worker/ && sudo /tmp/architect-worker

go build -o /tmp/architect-manager ./cmd/architect-manager/ && sudo /tmp/architect-manager --start
go build -o /tmp/architect-manager ./cmd/architect-manager/ && sudo /tmp/architect-manager --stop

go build -o /tmp/architect-manager ./cmd/architect-manager/ && sudo /tmp/architect-manager --flush # Pauses the VM
```
