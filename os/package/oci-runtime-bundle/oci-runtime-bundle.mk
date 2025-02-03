################################################################################
#
# oci-runtime-bundle
#
################################################################################

OCI_RUNTIME_BUNDLE_DEPENDENCIES = crun

OCI_RUNTIME_BUNDLE_VERSION = 1.0
OCI_RUNTIME_BUNDLE_SITE = $(@D) # This package has no source
OCI_RUNTIME_BUNDLE_SITE_METHOD = local

OCI_RUNTIME_BUNDLE_LICENSE = AGPL-3.0-or-later

define OCI_RUNTIME_BUNDLE_INSTALL_TARGET_CMDS
	$(eval LINE := $(shell sed -e "s%@OCI_RUNTIME_BUNDLE_MOUNT_DIR@%$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_MOUNT_DIR)%g" \
		-e "s%@OCI_RUNTIME_BUNDLE_MOUNT_LABEL@%$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_MOUNT_LABEL)%g" \
		$(OCI_RUNTIME_BUNDLE_PKGDIR)/fstab.in))
	@grep -qxF "$(LINE)" $(TARGET_DIR)/etc/fstab || echo -e "\n$(LINE)" >> $(TARGET_DIR)/etc/fstab
endef

define OCI_RUNTIME_BUNDLE_INSTALL_INIT_SYSTEMD
	mkdir -p $(TARGET_DIR)$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_SYSTEMD_INSTALL_DIR)
	sed -e "s%@OCI_RUNTIME_BUNDLE_MOUNT_DIR@%$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_MOUNT_DIR)%g" \
		-e "s%@OCI_RUNTIME_BUNDLE_MOUNT_LABEL@%$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_MOUNT_LABEL)%g" \
		-e "s%@OCI_RUNTIME_BUNDLE_CONTAINER_NAME@%$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_CONTAINER_NAME)%g" \
		$(OCI_RUNTIME_BUNDLE_PKGDIR)/oci-runtime-bundle.service.in \
		> $(TARGET_DIR)$(BR2_PACKAGE_OCI_RUNTIME_BUNDLE_SYSTEMD_INSTALL_DIR)/oci-runtime-bundle.service
endef

$(eval $(generic-package))
