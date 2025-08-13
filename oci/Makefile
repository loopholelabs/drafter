# Public variables
OUTPUT_DIR ?= out

OCI_IMAGE_URI ?= docker://valkey/valkey:latest
OCI_IMAGE_ARCHITECTURE ?= amd64
OCI_IMAGE_HOSTNAME ?= drafterguest

# Build OCI runtime bundle
build/oci:
	$(MAKE) unpack/oci
	$(MAKE) pack/oci

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

	sudo resize2fs -M $(OUTPUT_DIR)/blueprint/oci.ext4

# Clean OCI runtime bundle
clean/oci:
	rm -rf $(OUTPUT_DIR)/oci-image $(OUTPUT_DIR)/oci-runtime-bundle $(OUTPUT_DIR)/blueprint/oci.ext4
