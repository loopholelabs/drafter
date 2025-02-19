package snapshotter

const (
	VSockName = "vsock.sock"

	DefaultBootArgs      = "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0 clocksource=tsc nokaslr lapic=notscdeadline tsc=unstable"
	DefaultBootArgsNoPVM = "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0 nokaslr"
)
