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

# # For PostgreSQL
# export DISK_SIZE="1536M"

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
sudo umount /tmp/blueprint/proc || true
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

#### PostgreSQL

```shell
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/drafter.drftdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

sudo mount --bind /dev /tmp/blueprint/dev

sudo chroot /tmp/blueprint sh - <<'EOT'
apk add postgresql postgresql-client

rc-update add postgresql default

su postgres -c "initdb -D /var/lib/postgresql/data"

echo "host all all 0.0.0.0/0 trust" > /var/lib/postgresql/data/pg_hba.conf
echo "listen_addresses='*'" >> /var/lib/postgresql/data/postgresql.conf
EOT

sync -f /tmp/blueprint
sudo umount /tmp/blueprint/dev || true
sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
```

### Liveness

```shell
export LIVENESS_VSOCK_PORT="25"

# For Redis
export SERVICE_DEPENDENCY="redis"

# # For Minecraft
# export SERVICE_DEPENDENCY="minecraft-server"

# # For PostgreSQL
# export SERVICE_DEPENDENCY="postgresql"

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

# # For Minecraft
# export SERVICE_DEPENDENCY="minecraft-server"

# # For PostgreSQL
# export SERVICE_DEPENDENCY="postgresql"

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
sudo drafter-snapshotter --netns ark0 --memory-size 512 \
    --state-output-path out/package/redis/drafter.drftstate \
    --memory-output-path out/package/redis/drafter.drftmemory \
    --initramfs-output-path out/package/redis/drafter.drftinitramfs \
    --kernel-output-path out/package/redis/drafter.drftkernel \
    --disk-output-path out/package/redis/drafter.drftdisk \
    --config-output-path out/package/redis/drafter.drftconfig

# For Minecraft (Cuberite/1.12.2) (be sure to use a free namespace)
sudo drafter-snapshotter --netns ark0 --memory-size 512 \
    --state-output-path out/package/minecraft-cuberite/drafter.drftstate \
    --memory-output-path out/package/minecraft-cuberite/drafter.drftmemory \
    --initramfs-output-path out/package/minecraft-cuberite/drafter.drftinitramfs \
    --kernel-output-path out/package/minecraft-cuberite/drafter.drftkernel \
    --disk-output-path out/package/minecraft-cuberite/drafter.drftdisk \
    --config-output-path out/package/minecraft-cuberite/drafter.drftconfig

# For Minecraft (Official server; needs more RAM than the default 1024 MB) (be sure to use a free namespace)
sudo drafter-snapshotter --netns ark0 --memory-size 1024 \
    --state-output-path out/package/minecraft-official/drafter.drftstate \
    --memory-output-path out/package/minecraft-official/drafter.drftmemory \
    --initramfs-output-path out/package/minecraft-official/drafter.drftinitramfs \
    --kernel-output-path out/package/minecraft-official/drafter.drftkernel \
    --disk-output-path out/package/minecraft-official/drafter.drftdisk \
    --config-output-path out/package/minecraft-official/drafter.drftconfig

# For PostgreSQL (be sure to use a free namespace)
sudo drafter-snapshotter --netns ark0 --memory-size 512 \
    --state-output-path out/package/postgresql/drafter.drftstate \
    --memory-output-path out/package/postgresql/drafter.drftmemory \
    --initramfs-output-path out/package/postgresql/drafter.drftinitramfs \
    --kernel-output-path out/package/postgresql/drafter.drftkernel \
    --disk-output-path out/package/postgresql/drafter.drftdisk \
    --config-output-path out/package/postgresql/drafter.drftconfig
```

```shell
# For Redis
sudo drafter-packager --package-path out/redis.drft \
    --state-path out/package/redis/drafter.drftstate \
    --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-path out/package/redis/drafter.drftkernel \
    --disk-path out/package/redis/drafter.drftdisk \
    --config-path out/package/redis/drafter.drftconfig # Append --extract to extract the package instead

# For Minecraft (Cuberite/1.12.2)
sudo drafter-packager --package-path out/minecraft-cuberite.drft \
    --state-path out/package/minecraft-cuberite/drafter.drftstate \
    --memory-path out/package/minecraft-cuberite/drafter.drftmemory \
    --initramfs-path out/package/minecraft-cuberite/drafter.drftinitramfs \
    --kernel-path out/package/minecraft-cuberite/drafter.drftkernel \
    --disk-path out/package/minecraft-cuberite/drafter.drftdisk \
    --config-path out/package/minecraft-cuberite/drafter.drftconfig # Append --extract to extract the package instead

# For Minecraft (Official server)
sudo drafter-packager --package-path out/minecraft-official.drft \
    --state-path out/package/minecraft-official/drafter.drftstate \
    --memory-path out/package/minecraft-official/drafter.drftmemory \
    --initramfs-path out/package/minecraft-official/drafter.drftinitramfs \
    --kernel-path out/package/minecraft-official/drafter.drftkernel \
    --disk-path out/package/minecraft-official/drafter.drftdisk \
    --config-path out/package/minecraft-official/drafter.drftconfig # Append --extract to extract the package instead

# For PostgreSQL
sudo drafter-packager --package-path out/postgresql.drft \
    --state-path out/package/postgresql/drafter.drftstate \
    --memory-path out/package/postgresql/drafter.drftmemory \
    --initramfs-path out/package/postgresql/drafter.drftinitramfs \
    --kernel-path out/package/postgresql/drafter.drftkernel \
    --disk-path out/package/postgresql/drafter.drftdisk \
    --config-path out/package/postgresql/drafter.drftconfig # Append --extract to extract the package instead
```

```shell
sudo drafter-runner --netns ark0 --package-path out/redis.drft # CTRL-C to flush the snapshot and run again to resume (be sure to use a free namespace and adjust the package path)
```

## Distributing, Running and Migrating Packages

The peer API can CLI are the primary way of distributing, running and migrating packages. They can be configured like so:

| `--raddr` | `--path` | `--laddr` | Supported configuration | Leech from[^1] | Leech via [^2] | Seed on[^3] | Write back changes to[^4] |
| --------- | -------- | --------- | ----------------------- | -------------- | -------------- | ----------- | ------------------------- |
|           |          |           | No                      |                |                |             |                           |
|           |          | Given     | No                      |                |                |             |                           |
|           | Given    |           | Yes                     | `--path`       | loop device    |             | `--path`                  |
|           | Given    | Given     | Yes                     | `--path`       | r3map device   | `--laddr`   | `--path`                  |
| Given     |          |           | Yes                     | `--raddr`      | r3map device   |             |                           |
| Given     | Given    |           | Yes                     | `--raddr`      | r3map device   |             | `--path`                  |
| Given     | Given    | Given     | Yes                     | `--raddr`      | r3map device   | `--laddr`   | `--path`                  |
| Given     |          | Given     | Yes                     | `--raddr`      | r3map device   | `--laddr`   |                           |

[^1]: Address from which the resource will be leeched from before resuming the VM
[^2]: Method with which the resource will be leeched
[^3]: Address on which the resource will be exposed to be migrated away after the VM has resumed/leeching has finished
[^4]: Address to which the changes will be written to if the peer API is `Close`d, e.g. if the process is stopped

These configurations lead to the following behavior:

- `drafter-peer`: Unsupported configuration
- `drafter-peer --laddr localhost:1234`: Unsupported configuration
- `drafter-peer --path ./resource.bin`: Leech from `--path` via a loop device, resume the VM, and write back changes to `--path` if interrupted
- `drafter-peer --path ./resource.bin --laddr localhost:1234`: Leech from `--path` via a r3map device, resume the VM, seed on `--laddr`, and write back changes to `--path` if interrupted
- `drafter-peer --raddr localhost:1234`: Leech from `--raddr` via a r3map device, resume the VM, and discard changes if interrupted
- `drafter-peer --raddr localhost:1234 --path ./resource.bin`: Leech from `--raddr` via a r3map device, resume the VM, and write back changes to `--path` if interrupted
- `drafter-peer --raddr localhost:1234 --path ./resource.bin --laddr localhost:1234`: Leech from `--raddr` via a r3map device, resume the VM, seed on `--laddr`, and write back changes to `--path` if interrupted
- `drafter-peer --raddr localhost:1234 --laddr localhost:1234`: Leech from `--raddr` via a r3map device, resume the VM, seed on `--laddr`, and discard changes if interrupted

### On a Workstation

```shell
drafter-registry --state-path out/package/redis/drafter.drftstate \
    --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-path out/package/redis/drafter.drftkernel \
    --disk-path out/package/redis/drafter.drftdisk \
    --config-path out/package/redis/drafter.drftconfig # Be sure to adjust the resource paths
```

```shell
sudo drafter-peer --netns ark0 --state-raddr localhost:1600 --memory-raddr localhost:1601 --initramfs-raddr localhost:1602 --kernel-raddr localhost:1603 --disk-raddr localhost:1604 --config-raddr localhost:1605 --enable-input # If --enable-input is specified, CTRL-C is forwarded to the VM, so to stop the VM use `sudo pkill -2 drafter-peer` instead (be sure to use a free namespace)
# Or alternatively, to make this first node become a registry instead of starting from a registry:
sudo drafter-peer --netns ark0 --state-raddr='' --state-path out/package/redis/drafter.drftstate \
    --memory-raddr='' --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-raddr='' --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-raddr='' --kernel-path out/package/redis/drafter.drftkernel \
    --disk-raddr='' --disk-path out/package/redis/drafter.drftdisk \
    --config-raddr='' --config-path out/package/redis/drafter.drftconfig \
    --enable-input
```

```shell
sudo drafter-peer --netns ark1 --enable-input # Migrates to this peer and seed; be sure to use a different namespace (i.e. ark1) since you're migrating on the same machine
# Or alternatively, to start an ephemeral VM and not seed again after startup:
sudo drafter-peer --state-laddr='' --memory-laddr='' --initramfs-laddr='' --kernel-laddr='' --disk-laddr='' --config-laddr='' --netns ark1
```

```shell
sudo drafter-peer --netns ark0 --state-path out/package/redis/drafter.drftstate \
    --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-path out/package/redis/drafter.drftkernel \
    --disk-path out/package/redis/drafter.drftdisk \
    --config-path out/package/redis/drafter.drftconfig # Migrates to this peer without enabling input; CTRL-C to flush the snapshot, stop the VM, and write the changes back to the paths (be sure to use a free namespace)
```

### On a Cluster

```shell
export REGISTRY_IP="136.144.59.97"

export NODE_1_IP="136.144.59.97"
export NODE_2_IP="145.40.75.137"
export NODE_3_IP="145.40.95.151"
```

```shell
drafter-registry --state-path out/package/redis/drafter.drftstate \
    --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-path out/package/redis/drafter.drftkernel \
    --disk-path out/package/redis/drafter.drftdisk \
    --config-path out/package/redis/drafter.drftconfig # On ${REGISTRY_IP}; be sure to adjust the resource paths
```

```shell
sudo drafter-peer --netns ark0 --state-raddr ${REGISTRY_IP}:1600 --memory-raddr ${REGISTRY_IP}:1601 --initramfs-raddr ${REGISTRY_IP}:1602 --kernel-raddr ${REGISTRY_IP}:1603 --disk-raddr ${REGISTRY_IP}:1604 --config-raddr ${REGISTRY_IP}:1605 --enable-input # On ${NODE_1_IP}: If --enable-input is specified, CTRL-C is forwarded to the VM, so to stop the VM use `sudo pkill -2 drafter-peer` instead (be sure to use a free namespace)
# Or alternatively, to make this first node become a registry instead of starting from a registry:
sudo drafter-peer --netns ark0 --state-raddr='' --state-path out/package/redis/drafter.drftstate \
    --memory-raddr='' --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-raddr='' --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-raddr='' --kernel-path out/package/redis/drafter.drftkernel \
    --disk-raddr='' --disk-path out/package/redis/drafter.drftdisk \
    --config-raddr='' --config-path out/package/redis/drafter.drftconfig \
    --enable-input
```

```shell
sudo drafter-peer --netns ark0 --state-raddr ${NODE_1_IP}:1500 --memory-raddr ${NODE_1_IP}:1501 --initramfs-raddr ${NODE_1_IP}:1502 --kernel-raddr ${NODE_1_IP}:1503 --disk-raddr ${NODE_1_IP}:1504 --config-raddr ${NODE_1_IP}:1505 --enable-input # On ${NODE_2_IP}: Migrates to this peer (be sure to use a free namespace)
# Or alternatively, to start an ephemeral VM and not seed again after startup:
sudo drafter-peer --netns ark0 --state-raddr ${NODE_1_IP}:1500 --memory-raddr ${NODE_1_IP}:1501 --initramfs-raddr ${NODE_1_IP}:1502 --kernel-raddr ${NODE_1_IP}:1503 --disk-raddr ${NODE_1_IP}:1504 --config-raddr ${NODE_1_IP}:1505 --state-laddr='' --memory-laddr='' --initramfs-laddr='' --kernel-laddr='' --disk-laddr='' --config-laddr='' --enable-input
```

```shell
sudo drafter-peer --netns ark0 --state-raddr ${NODE_2_IP}:1500 --memory-raddr ${NODE_2_IP}:1501 --initramfs-raddr ${NODE_2_IP}:1502 --kernel-raddr ${NODE_2_IP}:1503 --disk-raddr ${NODE_2_IP}:1504 --config-raddr ${NODE_2_IP}:1505 # On ${NODE_3_IP}: Migrates to this peer without enabling input; CTRL-C to flush the snapshot and stop the VM (be sure to use a free namespace)
```

```shell
sudo drafter-peer --netns ark0 --state-raddr ${NODE_3_IP}:1500 --state-path out/package/redis/drafter.drftstate \
    --memory-raddr ${NODE_3_IP}:1501 --memory-path out/package/redis/drafter.drftmemory \
    --initramfs-raddr ${NODE_3_IP}:1502 --initramfs-path out/package/redis/drafter.drftinitramfs \
    --kernel-raddr ${NODE_3_IP}:1503 --kernel-path out/package/redis/drafter.drftkernel \
    --disk-raddr ${NODE_3_IP}:1504 --disk-path out/package/redis/drafter.drftdisk \
    --config-raddr ${NODE_3_IP}:1505 --config-path out/package/redis/drafter.drftconfig # On ${NODE_1_IP}: Migrates to this peer without enabling input; CTRL-C to flush the snapshot, stop the VM, and write the changes back to the paths (be sure to use a free namespace)
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
