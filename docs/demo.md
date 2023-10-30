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
sudo architekt-daemon --host-interface enp1s0f0 # Sets up networking and keeps running; CTRL-C to tear down networking. Be sure to adjust --host-interface to your local system.
```

## Build a Blueprint on a Workstation

### Base

```shell
# For Redis
export DISK_SIZE="256M"

# For Minecraft (Cuberite/1.12.2)
export DISK_SIZE="512M"

# For Minecraft (Official server)
export DISK_SIZE="2G"

export GATEWAY_IP="172.100.100.1"
export GUEST_CIDR="172.100.100.2/30"

rm -rf out/blueprint
mkdir -p out/blueprint

rm -rf /tmp/kernel
mkdir -p /tmp/kernel

curl -Lo /tmp/kernel.tar.xz https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.194.tar.xz
tar Jxvf /tmp/kernel.tar.xz --strip-components=1 -C /tmp/kernel

curl -Lo /tmp/kernel/.config https://raw.githubusercontent.com/loopholelabs/firecracker/live-migration-1.5/resources/guest_configs/microvm-kernel-ci-x86_64-5.10.config

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
apk add alpine-base util-linux linux-virt linux-virt-dev coreutils binutils grep bzip2 chrony
echo root:root | chpasswd

ln -s agetty /etc/init.d/agetty.ttyS0
echo ttyS0 >/etc/securetty
rc-update add agetty.ttyS0 default

sed -i 's/initstepslew/#initstepslew/g' /etc/chrony/chrony.conf
echo 'refclock PHC /dev/ptp0 poll 3 dpoll -2 offset 0' >> /etc/chrony/chrony.conf

rc-update add networking default
rc-update add chronyd default
EOT

sudo cp /tmp/blueprint/boot/initramfs-virt out/blueprint/architekt.arkinitramfs
sudo chown ${USER} out/blueprint/architekt.arkinitramfs

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

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
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

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
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
Description=Cuberite - in C++!
ShutdownMessage=Server shutdown
MaxPlayers=100
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
LimitPlayerBlockChanges=1

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

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
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
export SERVICE_DEPENDENCY="minecraft-server"

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

CGO_ENABLED=0 go build -o /tmp/blueprint/usr/sbin/architekt-liveness ./cmd/architekt-liveness

tee /tmp/blueprint/etc/init.d/architekt-liveness <<EOT
#!/sbin/openrc-run

command="/usr/sbin/architekt-liveness"
command_args="--vsock-port ${LIVENESS_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net ${SERVICE_DEPENDENCY} architekt-agent
}
EOT
chmod +x /tmp/blueprint/etc/init.d/architekt-liveness

sudo chroot /tmp/blueprint sh - <<'EOT'
rc-update add architekt-liveness default
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
export SERVICE_DEPENDENCY="minecraft-server"

sudo umount /tmp/blueprint || true
rm -rf /tmp/blueprint
mkdir -p /tmp/blueprint

sudo mount out/blueprint/architekt.arkdisk /tmp/blueprint
sudo chown ${USER} /tmp/blueprint

CGO_ENABLED=0 go build -o /tmp/blueprint/usr/sbin/architekt-agent ./cmd/architekt-agent

tee /tmp/blueprint/etc/init.d/architekt-agent <<EOT
#!/sbin/openrc-run

command="/usr/sbin/architekt-agent"
command_args="--vsock-port ${AGENT_VSOCK_PORT}"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/stdout"
error_log="/dev/stderr"

depend() {
	need net ${SERVICE_DEPENDENCY}
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
# For Redis & Minecraft (Cuberite/1.12.2)
sudo architekt-packager --memory-size 512

# For Minecraft (Official server; needs more RAM than the default 1024 MB)
sudo architekt-packager --memory-size 2048

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
sudo architekt-peer --netns ark0 --raddr ${NODE_1_IP}:1338 --enable-input # On ${NODE_2_IP}: Migrates to this peer (be sure to use a free namespace)
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
sudo architekt-worker --verbose --name node-1 --host-interface wlp0s20f3
```

```shell
curl -v http://localhost:1400/nodes | jq

curl -v http://localhost:1400/nodes/node-1/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://localhost:1400/nodes/node-1/instances/localhost:1337 | jq -r) # Create VM

curl -v http://localhost:1400/nodes/node-1/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://localhost:1400/nodes/node-1/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM

curl -v http://localhost:1400/nodes/node-1/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://localhost:1400/nodes/node-1/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM again

curl -v http://localhost:1400/nodes/node-1/instances | jq

curl -v -X DELETE http://localhost:1400/nodes/node-1/instances/${PACKAGE_RADDR} # Delete VM

curl -v http://localhost:1400/nodes/node-1/instances | jq
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
sudo architekt-worker --verbose --name node-1 --host-interface enp1s0f0 --ahost ${NODE_1_IP} --control-plane-raddr ${CONTROL_PLANE_IP}:1399 # On ${NODE_1_IP}
```

```shell
sudo architekt-worker --verbose --name node-2 --host-interface enp1s0f1 --ahost ${NODE_2_IP} --control-plane-raddr ${CONTROL_PLANE_IP}:1399 # On ${NODE_2_IP}
```

```shell
curl -v http://${CONTROL_PLANE_IP}:1400/nodes | jq

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-2/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances/${REGISTRY_IP}:1337 | jq -r) # Create VM on node 1

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-2/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://${CONTROL_PLANE_IP}:1400/nodes/node-2/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM from node 1 to node 2

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-2/instances | jq

export PACKAGE_RADDR=$(curl -v -X POST http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances/${PACKAGE_RADDR} | jq -r) # Migrate VM from node 2 to node 1

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-2/instances | jq

curl -v -X DELETE http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances/${PACKAGE_RADDR} # Delete VM from node 2

curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-1/instances | jq
curl -v http://${CONTROL_PLANE_IP}:1400/nodes/node-2/instances | jq
```

## Using the Operator

### On a Workstation

```shell
architekt-registry
```

```shell
architekt-manager --verbose
```

```shell
sudo architekt-worker --verbose --name minikube --host-interface wlp0s20f3
```

```shell
make operator/install # Be sure to have access to a Kubernetes cluster running on localhost, i.e. one started with `minikube start`
```

```shell
architekt-operator
```

```shell
kubectl apply -f config/samples/architekt_v1alpha1_instance.yaml # Create VM
```

```shell
kubectl get -o yaml instance.io.loopholelabs.architekt/redis # List VMs and watch for changes
```

```shell
kubectl apply -f config/samples/architekt_v1alpha1_instance.yaml # Simulate VM migration on localhost (on workstation; be sure to have access to the Kubernetes cluster and change `spec.packageRaddr` to `status.packageLaddr` from the watch command after the VM has `spec.status.state == running`
```

```shell
kubectl delete -f config/samples/architekt_v1alpha1_instance.yaml # Delete VM
```

### On a Cluster

```shell
export REGISTRY_IP="186.233.186.43"
export CONTROL_PLANE_IP="186.233.186.43"

export NODE_1_IP="186.233.186.43"
export NODE_1_NAME="chicago" # Name of the Kubernetes node (Kubernetes API server) on ${NODE_1_IP}

export NODE_2_IP="160.202.128.189"
export NODE_2_NAME="nyc" # Name of the Kubernetes node (Kubernetes worker) on ${NODE_2_IP}
```

```shell
architekt-registry # On ${REGISTRY_IP}
```

```shell
architekt-manager --verbose # On ${CONTROL_PLANE_IP}
```

```shell
sudo architekt-worker --verbose --name ${NODE_1_NAME} --host-interface enp1s0f0 --ahost ${NODE_1_IP} --control-plane-raddr ${CONTROL_PLANE_IP}:1399 # On ${NODE_1_IP}
```

```shell
sudo architekt-worker --verbose --name ${NODE_2_NAME} --host-interface enp1s0f1 --ahost ${NODE_2_IP} --control-plane-raddr ${CONTROL_PLANE_IP}:1399 # On ${NODE_2_IP}
```

```shell
make operator/install # On workstation; be sure to have access to the Kubernetes cluster
```

```shell
KUBECONFIG="/etc/rancher/k3s/k3s.yaml" architekt-operator # On ${NODE_1_IP}/where Kubernetes API server runs; assuming k3s cluster, point to correct Kubeconfig location otherwise
```

```shell
kubectl apply -f config/samples/architekt_v1alpha1_instance.yaml # Create VM (on workstation; be sure to have access to the Kubernetes cluster and change `packageRaddr` to ${REGISTRY_IP} and `nodeName` to ${NODE_1_NAME})
```

```shell
kubectl get -o yaml instance.io.loopholelabs.architekt/redis # List VMs and watch for changes (on workstation; be sure to have access to the Kubernetes cluster)
```

```shell
kubectl apply -f config/samples/architekt_v1alpha1_instance.yaml # Migrate VM from node 1 to node 2 (on workstation; be sure to have access to the Kubernetes cluster and change `nodeName` to ${NODE_2_NAME})
```

```shell
kubectl apply -f config/samples/architekt_v1alpha1_instance.yaml # Migrate VM from node 2 to node 1 (on workstation; be sure to have access to the Kubernetes cluster and change `nodeName` to ${NODE_1_NAME})
```

```shell
kubectl delete -f config/samples/architekt_v1alpha1_instance.yaml # Delete VM (on workstation; be sure to have access to the Kubernetes cluster and change `nodeName` to ${NODE_1_NAME})
```

## Tearing Down Workstation and Server Dependencies

```shell
sudo pkill -2 architekt-peer
sudo pkill -2 architekt-registry
sudo pkill -2 architekt-daemon
sudo pkill -2 architekt-worker
sudo pkill -2 architekt-operator

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
