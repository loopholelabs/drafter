# Public variables
OUTPUT_DIR ?= out

OS_URL ?= https://github.com/loopholelabs/buildroot/archive/666509742535a5b7b04315040249ffdcf1ed7350.tar.gz
OS_DEFCONFIG ?= drafteros-oci-firecracker-x86_64_defconfig
OS_BR2_EXTERNAL ?= ../../os

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

# Clean OS
clean/os:
	rm -rf $(OUTPUT_DIR)/buildroot

# OS dependencies
depend/os:
	rm -rf $(OUTPUT_DIR)/buildroot
	mkdir -p $(OUTPUT_DIR)/buildroot
	curl -L $(OS_URL) | tar -xz -C $(OUTPUT_DIR)/buildroot --strip-components=1
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" $(OS_DEFCONFIG)
