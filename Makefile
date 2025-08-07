# Public variables
DESTDIR ?=
PREFIX ?= /usr/local
OUTPUT_DIR ?= out
DST ?=

OS_URL ?= https://github.com/loopholelabs/buildroot/archive/8958adcacfec580cda9ee5ff049017a9f64a6996.tar.gz
OS_DEFCONFIG ?= drafteros-oci-firecracker-x86_64_defconfig
OS_BR2_EXTERNAL ?= ../../os

# Private variables
obj = drafter-nat drafter-forwarder drafter-agent drafter-liveness drafter-snapshotter drafter-packager drafter-peer
all: $(addprefix build/,$(obj))

# Build
build: $(addprefix build/,$(obj))
$(addprefix build/,$(obj)):
ifdef DST
	go build -o $(DST) ./cmd/$(subst build/,,$@)
else
	go build -o $(OUTPUT_DIR)/$(subst build/,,$@) ./cmd/$(subst build/,,$@)
endif

# Build OS
build/os:
	# Common OS packages
	if grep -q "BR2_PACKAGE_DRAFTER_AGENT=y" $(OUTPUT_DIR)/buildroot/.config; then \
		$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" drafter-agent-reconfigure; \
	fi
	if grep -q "BR2_PACKAGE_DRAFTER_LIVENESS=y" $(OUTPUT_DIR)/buildroot/.config; then \
		$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" drafter-liveness-reconfigure; \
	fi	

	# OCI OS packages
	if grep -q "BR2_PACKAGE_OCI_RUNTIME_BUNDLE=y" $(OUTPUT_DIR)/buildroot/.config; then \
		$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" oci-runtime-bundle-reconfigure; \
	fi

	# k3s OS packages
	if grep -q "BR2_PACKAGE_K3S=y" $(OUTPUT_DIR)/buildroot/.config; then \
		$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" k3s-dirclean; \
		$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" k3s-reconfigure; \
	fi

	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)"

	mkdir -p $(OUTPUT_DIR)/blueprint
	cp $(OUTPUT_DIR)/buildroot/output/images/vmlinux $(OUTPUT_DIR)/blueprint/vmlinux
	cp $(OUTPUT_DIR)/buildroot/output/images/rootfs.ext4 $(OUTPUT_DIR)/blueprint/rootfs.ext4

# Build Kernel
build/kernel:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" linux

# Configure OS
config/os:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" menuconfig

# Configure kernel
config/kernel:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" linux-menuconfig

# Save OS defconfig changes
save/os:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" savedefconfig

# Save kernel defconfig changes
save/kernel:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" linux-savedefconfig
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" linux-update-defconfig

# Install
install: $(addprefix install/,$(obj))
$(addprefix install/,$(obj)):
	install -D -m 0755 $(OUTPUT_DIR)/$(subst install/,,$@) $(DESTDIR)$(PREFIX)/bin/$(subst install/,,$@)

# Uninstall
uninstall: $(addprefix uninstall/,$(obj))
$(addprefix uninstall/,$(obj)):
	rm $(DESTDIR)$(PREFIX)/bin/$(subst uninstall/,,$@)

# Run
$(addprefix run/,$(obj)):
	$(subst run/,,$@) $(ARGS)

# Test
test:
	go test -timeout 3600s -parallel $(shell nproc) ./...

# Benchmark
benchmark:
	go test -timeout 3600s -bench=./... ./...

# Clean
clean:
	rm -rf out

# Clean OS
clean/os:
	rm -rf $(OUTPUT_DIR)/buildroot

# Dependencies
depend:
	go generate ./...

# OS dependencies
depend/os:
	rm -rf $(OUTPUT_DIR)/buildroot
	mkdir -p $(OUTPUT_DIR)/buildroot
	curl -L $(OS_URL) | tar -xz -C $(OUTPUT_DIR)/buildroot --strip-components=1
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" $(OS_DEFCONFIG)
