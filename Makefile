# Public variables
DESTDIR ?=
PREFIX ?= /usr/local
OUTPUT_DIR ?= out
DST ?=

OCI_IMAGE_URI ?= docker://valkey/valkey:latest
OCI_IMAGE_ARCHITECTURE ?= amd64
OCI_IMAGE_HOSTNAME ?= drafterguest

OS_URL ?= https://buildroot.org/downloads/buildroot-2024.08-rc1.tar.gz
OS_DEFCONFIG ?= drafteros-firecracker-x86_64_defconfig
OS_BR2_EXTERNAL ?= ../../os

# Private variables
obj = drafter-nat drafter-forwarder drafter-agent drafter-liveness drafter-snapshotter drafter-packager drafter-runner drafter-registry drafter-mounter drafter-peer drafter-terminator
all: $(addprefix build/,$(obj))

# Build
build: $(addprefix build/,$(obj))
$(addprefix build/,$(obj)):
ifdef DST
	go build -o $(DST) ./cmd/$(subst build/,,$@)
else
	go build -o $(OUTPUT_DIR)/$(subst build/,,$@) ./cmd/$(subst build/,,$@)
endif

# Build OCI runtime bundle
build/oci:
	$(MAKE) unpack/oci
	$(MAKE) pack/oci

# Build OS
build/os:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" drafter-liveness-reconfigure drafter-agent-reconfigure oci-runtime-bundle-reconfigure
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)"

	mkdir -p $(OUTPUT_DIR)/blueprint
	cp $(OUTPUT_DIR)/buildroot/output/images/vmlinux $(OUTPUT_DIR)/blueprint/vmlinux
	cp $(OUTPUT_DIR)/buildroot/output/images/rootfs.ext4 $(OUTPUT_DIR)/blueprint/rootfs.ext4

# Configure OS
config/os:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" menuconfig

# Save OS defconfig changes
save/os:
	$(MAKE) -C $(OUTPUT_DIR)/buildroot BR2_EXTERNAL="$(OS_BR2_EXTERNAL)" savedefconfig

# Unpack OCI runtime bundle
unpack/oci:
	rm -rf $(OUTPUT_DIR)/oci-image
	mkdir -p $(OUTPUT_DIR)/oci-image
	skopeo --override-arch $(OCI_IMAGE_ARCHITECTURE) copy $(OCI_IMAGE_URI) oci:$(OUTPUT_DIR)/oci-image:latest

	sudo rm -rf $(OUTPUT_DIR)/oci-runtime-bundle
	mkdir -p $(OUTPUT_DIR)/oci-runtime-bundle
	sudo umoci unpack --image $(OUTPUT_DIR)/oci-image:latest $(OUTPUT_DIR)/oci-runtime-bundle

	TMPFILE=$$(mktemp) && jq '.process.terminal = false' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.hostname = "$(OCI_IMAGE_HOSTNAME)"' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.mounts += [{"destination": "/etc/resolv.conf", "type": "bind", "source": "/etc/resolv.conf", "options": ["bind", "rprivate"]}]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.mounts += [{"destination": "/etc/hosts", "type": "bind", "source": "/etc/hosts", "options": ["bind", "rprivate"]}]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.mounts += [{"destination": "/etc/hostname", "type": "bind", "source": "/etc/hostname", "options": ["bind", "rprivate"]}]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.process.capabilities.bounding += ["CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT"]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.process.capabilities.effective += ["CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT"]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.process.capabilities.inheritable += ["CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT"]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.process.capabilities.permitted += ["CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT"]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.process.capabilities.ambient += ["CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT"]' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json
	TMPFILE=$$(mktemp) && jq '.linux.namespaces |= map(select(.type != "network"))' $(OUTPUT_DIR)/oci-runtime-bundle/config.json > $${TMPFILE} && mv $${TMPFILE} $(OUTPUT_DIR)/oci-runtime-bundle/config.json

# Pack OCI runtime bundle
pack/oci:
	rm -f $(OUTPUT_DIR)/blueprint/oci.ext4
	mkdir -p $(OUTPUT_DIR)/blueprint
	sudo mke2fs -b 4096 -t ext4 -L oci -d $(OUTPUT_DIR)/oci-runtime-bundle/ $(OUTPUT_DIR)/blueprint/oci.ext4 $$(umoci stat --image $(OUTPUT_DIR)/oci-image:latest --json | jq '[.history[] | select(.layer != null) | .layer.size] | add * 1.1 | ceil')

	resize2fs -M $(OUTPUT_DIR)/blueprint/oci.ext4

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

# Clean OCI runtime bundle
clean/oci:
	rm -rf $(OUTPUT_DIR)/oci-image $(OUTPUT_DIR)/oci-runtime-bundle $(OUTPUT_DIR)/blueprint/oci.ext4

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
