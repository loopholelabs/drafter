# Drafter migration test

This can run a migration test on the same host, or between hosts, or in a ring.

## Setup

1. You can use it to create a snapshot of some blueprints if you don't yet have a snapshot. The snapshot folder 'blueprint' should have `oci.ext4  rootfs.ext4  vmlinux`. For example download
https://github.com/loopholelabs/drafter/releases/download/v0.6.1/oci-valkey-x86_64.tar.zst
and https://github.com/loopholelabs/drafter/releases/download/v0.6.1/drafteros-oci-x86_64_pvm.tar.zst

        sudo ./drafter-mignettest --blueprints blueprint --pvm=true --start

2. You should now share the snapshot between hosts, so that COW works as expected. (Use scp or similar)

3. You can now run a migration loop.

### Host A

    sudo ./drafter-mignettest --num 1000 --snapshot snap_test --start=true --dest 172.31.34.210:7788 --listen :7788 --pvm=true

### Host B

    sudo ./drafter-mignettest --num 1000 --snapshot snap_test --dest 172.31.39.77:7788 --listen :7788 --pvm=true

Alternatively you can of course start from a working snapshot.