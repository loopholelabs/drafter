# Drafter Demo

> Use kernel 5.10 on _both_ the host and guest; newer kernel versions are known to cause freezes when restoring snapshots/migrating VMs

## Installing Firecracker and Drafter

```shell
git clone https://github.com/loopholelabs/firecracker /tmp/firecracker
cd /tmp/firecracker

git remote add upstream https://github.com/firecracker-microvm/firecracker
git fetch --all

tools/devtool build --release

sudo install ./build/cargo_target/x86_64-unknown-linux-musl/release/{firecracker,jailer} /usr/local/bin/
```

```shell
git clone https://github.com/loopholelabs/drafter.git /tmp/drafter
cd /tmp/drafter

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
sudo modprobe nbd nbds_max=4096
sudo tee /etc/modules-load.d/nbd.conf <<EOT
nbd
EOT
sudo tee /etc/modprobe.d/nbd.conf <<EOT
options nbd nbds_max=4096
EOT
```

```shell
sudo drafter-daemon --host-interface bond0 # Sets up networking and keeps running; CTRL-C to tear down networking. Be sure to adjust --host-interface to your local system.
```

## Build a Blueprint on a Workstation

### Kernel

#### 6.1

> Unsupported; might freeze on snapshot restores

```shell
rm -rf out/blueprint
mkdir -p out/blueprint

rm -rf /tmp/kernel
mkdir -p /tmp/kernel

curl -Lo /tmp/kernel.tar.xz https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.1.60.tar.xz
tar Jxvf /tmp/kernel.tar.xz --strip-components=1 -C /tmp/kernel

curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/loopholelabs/firecracker/live-migration-1.6-main-1/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config

sh - <<'EOT'
cd /tmp/kernel

make -j$(nproc) vmlinux
EOT

cp /tmp/kernel/vmlinux out/blueprint/drafter.drftkernel
```

#### 5.10

```shell
rm -rf out/blueprint
mkdir -p out/blueprint

rm -rf /tmp/kernel
mkdir -p /tmp/kernel

curl -Lo /tmp/kernel.tar.xz https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.194.tar.xz
tar Jxvf /tmp/kernel.tar.xz --strip-components=1 -C /tmp/kernel

curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/loopholelabs/firecracker/live-migration-1.6-main-1/resources/guest_configs/microvm-kernel-ci-x86_64-5.10.config

sh - <<'EOT'
cd /tmp/kernel

make -j$(nproc) vmlinux
EOT

cp /tmp/kernel/vmlinux out/blueprint/drafter.drftkernel
```

### Base

```shell
# For Redis
export DISK_SIZE="384M"

# # For Minecraft (Cuberite/1.12.2)
# export DISK_SIZE="1536M"

# # For Minecraft (Official server)
# export DISK_SIZE="2G"

export GATEWAY_IP="172.100.100.1"
export GUEST_CIDR="172.100.100.2/30"

qemu-img create -f raw out/blueprint/drafter.drftdisk ${DISK_SIZE}
mkfs.ext4 out/blueprint/drafter.drftdisk

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

curl -Lo /tmp/rootfs.tar.gz https://dl-cdn.alpinelinux.org/alpine/v3.18/releases/x86_64/alpine-minirootfs-3.18.4-x86_64.tar.gz
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
apk add alpine-base util-linux linux-virt linux-virt-dev coreutils binutils grep bzip2 chrony haveged
echo root:root | chpasswd

ln -s agetty /etc/init.d/agetty.ttyS0
echo ttyS0 >/etc/securetty
rc-update add agetty.ttyS0 default

sed -i 's/initstepslew/#initstepslew/g' /etc/chrony/chrony.conf
echo 'refclock PHC /dev/ptp0 poll 3 dpoll -2 offset 0' >> /etc/chrony/chrony.conf

rc-update add networking default
rc-update add chronyd default
rc-update add haveged default
EOT

sudo cp /tmp/blueprint/boot/initramfs-virt out/blueprint/drafter.drftinitramfs
sudo chown ${USER} out/blueprint/drafter.drftinitramfs

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

### Application

#### Redis

```shell
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

sudo chroot /tmp/blueprint sh - <<'EOT'
apk add redis redis-openrc

rc-update add redis default
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

#### Minecraft (Cuberite/1.12.2)

```shell
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

if [ ! -d /tmp/blueprint/root/cuberite ]; then
    git clone --recursive https://github.com/cuberite/cuberite.git /tmp/blueprint/root/cuberite
fi

sudo chroot /tmp/blueprint sh - <<'EOT'
apk add build-base git python3 perl clang cmake expect bash

cd /root

if [ ! -f /cuberite/Release/Server/Cuberite ]; then
    mkdir -p cuberite/Release
    cd cuberite/Release

    cmake -DCMAKE_BUILD_TYPE=RELEASE .. -DCMAKE_CXX_COMPILER=/usr/bin/clang++
    make -l

    tee Server/settings.ini <<EOL
; This is the main server configuration
; Most of the settings here can be configured using the webadmin interface, if enabled in webadmin.ini

[Authentication]
Authenticate=0
AllowBungeeCord=0
OnlyAllowBungeeCord=0
ProxySharedSecret=
Server=sessionserver.mojang.com
Address=/session/minecraft/hasJoined?username=%USERNAME%&serverId=%SERVERID%

[MojangAPI]
NameToUUIDServer=api.mojang.com
NameToUUIDAddress=/profiles/minecraft
UUIDToProfileServer=sessionserver.mojang.com
UUIDToProfileAddress=/session/minecraft/profile/%UUID%?unsigned=false

[Server]
Description=Minecraft on Drafter
ShutdownMessage=Server shutdown
MaxPlayers=20
HardcoreEnabled=0
AllowMultiLogin=0
RequireResourcePack=0
ResourcePackUrl=
CustomRedirectUrl=https://youtu.be/dQw4w9WgXcQ
Ports=25565
AllowMultiWorldTabCompletion=1
DefaultViewDistance=10

[RCON]
Enabled=0

[AntiCheat]
LimitPlayerBlockChanges=0

[Worlds]
DefaultWorld=world
World=world_nether
World=world_the_end

[WorldPaths]
world=world
world_nether=world_nether
world_the_end=world_the_end

[Plugins]
Core=1
ChatLog=1
ProtectionAreas=0

[DeadlockDetect]
Enabled=1
IntervalSec=20

[Seed]
Seed=775375601

[SpawnPosition]
MaxViewDistance=10
X=0.500000
Y=115.000000
Z=0.500000
PregenerateDistance=20
EOL
fi

mkdir -p /root/.cache/
EOT

tee /tmp/blueprint/etc/init.d/minecraft-server <<EOT
#!/sbin/openrc-run

command="/bin/bash"
command_args="-c 'cp -r /root/cuberite/Release/Server/* /run && cd /run && unbuffer ./Cuberite'"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net
}
EOT
chmod +x /tmp/blueprint/etc/init.d/minecraft-server

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add minecraft-server default
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

#### Minecraft (Official Server)

> This is significantly more memory-intensive than Cuberite, leading to much higher migration times.

```shell
sudo umount /tmp/blueprint/proc || true
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

sudo mount -t proc proc /tmp/blueprint/proc

sudo chroot /tmp/blueprint sh - <<'EOT'
apk add openjdk17 curl

curl -L -o /usr/sbin/minecraft-server.jar https://piston-data.mojang.com/v1/objects/5b868151bd02b41319f54c8d4061b8cae84e665c/server.jar

cd /root

/usr/bin/java -Xmx1024M -Xms1024M -jar /usr/sbin/minecraft-server.jar nogui || true

echo 'eula=true' > eula.txt
echo 'online-mode=false' >> server.properties
EOT

sudo umount /tmp/blueprint/proc

tee /tmp/blueprint/etc/init.d/minecraft-server <<EOT
#!/sbin/openrc-run

command="/usr/bin/java"
command_args="-Xmx1024M -Xms1024M -jar /usr/sbin/minecraft-server.jar nogui"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"
directory="/root"

depend() {
	need net
}
EOT
chmod +x /tmp/blueprint/etc/init.d/minecraft-server

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add minecraft-server default
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

### Liveness

```shell
export LIVENESS_VSOCK_PORT="25"

# For Redis
export SERVICE_DEPENDENCY="redis"

# For Minecraft
# export SERVICE_DEPENDENCY="minecraft-server"

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

CGO_ENABLED=0 go build -o /tmp/blueprint/usr/sbin/drafter-liveness ./cmd/drafter-liveness

tee /tmp/blueprint/etc/init.d/drafter-liveness <<EOT
#!/sbin/openrc-run

command="/usr/sbin/drafter-liveness"
command_args="--vsock-port ${LIVENESS_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net ${SERVICE_DEPENDENCY} drafter-agent
}
EOT
chmod +x /tmp/blueprint/etc/init.d/drafter-liveness

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add drafter-liveness default
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

### Agent

```shell
export AGENT_VSOCK_PORT="26"

# For Redis
export SERVICE_DEPENDENCY="redis"

# For Minecraft
# export SERVICE_DEPENDENCY="minecraft-server"

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

CGO_ENABLED=0 go build -o /tmp/blueprint/usr/sbin/drafter-agent ./cmd/drafter-agent

tee /tmp/blueprint/etc/init.d/drafter-agent <<EOT
#!/sbin/openrc-run

command="/usr/sbin/drafter-agent"
command_args="--vsock-port ${AGENT_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net ${SERVICE_DEPENDENCY}
}
EOT
chmod +x /tmp/blueprint/etc/init.d/drafter-agent

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add drafter-agent default
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

## Creating and Running a Blueprint on a Workstation

```shell
# For Redis (be sure to use a free namespace)
sudo drafter-packager --netns ark0 --memory-size 512 --package-output-path out/redis.drft

# For Minecraft (Cuberite/1.12.2) (be sure to use a free namespace)
sudo drafter-packager --netns ark0 --memory-size 512 --package-output-path out/minecraft-cuberite.drft

# For Minecraft (Official server; needs more RAM than the default 1024 MB) (be sure to use a free namespace)
sudo drafter-packager --netns ark0 --memory-size 2048 --package-output-path out/minecraft-official.drft

sudo drafter-runner --netns ark0 --package-path out/redis.drft # CTRL-C to flush the snapshot and run again to resume (be sure to use a free namespace and adjust the package path)
```

## Distributing, Running and Migrating Packages

### On a Workstation

```shell
drafter-registry --package-path out/redis.drft # Be sure to adjust the package path
```

```shell
sudo drafter-peer --netns ark0 --state-raddr localhost:1600 --memory-raddr localhost:1601 --initramfs-raddr localhost:1602 --kernel-raddr localhost:1603 --disk-raddr localhost:1604 --netns ark0 --enable-input # If --enable-input is specified, CTRL-C is forwarded to the VM, so to stop the VM use `sudo pkill -2 drafter-peer` instead (be sure to use a free namespace)
```

```shell
sudo drafter-peer --netns ark1 --enable-input # Migrates to this peer; be sure to use a different namespace (i.e. ark1) since you're migrating on the same machine
```

```shell
sudo drafter-peer --netns ark0 # Migrates to this peer without enabling input; CTRL-C to flush the snapshot and stop the VM (be sure to use a free namespace)
```

### On a Cluster

```shell
export REGISTRY_IP="136.144.59.97"

export NODE_1_IP="136.144.59.97"
export NODE_2_IP="145.40.75.137"
export NODE_3_IP="145.40.95.151"
```

```shell
drafter-registry --package-path out/redis.drft # On ${REGISTRY_IP}; be sure to adjust the package path
```

```shell
sudo drafter-peer --netns ark0 --raddr ${REGISTRY_IP}:1337 --enable-input # On ${NODE_1_IP}: If --enable-input is specified, CTRL-C is forwarded to the VM, so to stop the VM use `sudo pkill -2 drafter-peer` instead (be sure to use a free namespace)
```

```shell
sudo drafter-peer --netns ark0 --raddr ${NODE_1_IP}:1338 --enable-input # On ${NODE_2_IP}: Migrates to this peer (be sure to use a free namespace)
```

```shell
sudo drafter-peer --netns ark0 --raddr ${NODE_2_IP}:1338 # On ${NODE_3_IP}: Migrates to this peer without enabling input; CTRL-C to flush the snapshot and stop the VM (be sure to use a free namespace)
```

```shell
sudo drafter-peer --netns ark0 --raddr ${NODE_3_IP}:1338 # On ${NODE_1_IP}: Migrates to this peer without enabling input; CTRL-C to flush the snapshot and stop the VM (be sure to use a free namespace)
```

## Tearing Down Workstation and Server Dependencies

```shell
sudo pkill -2 drafter-registry
sudo pkill -2 drafter-peer
sudo pkill -2 drafter-daemon

# Completely resetting the network configuration (should not be necessary)
sudo iptables -X
sudo iptables -F
for ns in $(ip netns list | awk '{print $1}'); do
    sudo ip netns delete $ns
done

# Completely cleaning up artifacts from failed runs (should not be necessary)
sudo pkill -9 firecracker
sudo umount out/redis.drft
sudo rm -f out/redis.drft
sudo rm -rf out/vms
sudo rm -rf out/cache
```

## Uninstalling Firecracker and Drafter

```shell
sudo rm /usr/local/bin/{firecracker,jailer}

cd /tmp/drafter
sudo make uninstall
```
