################################################################################
#
# k3s
#
################################################################################

K3S_VERSION = 1.31.2+k3s1
K3S_SITE = $(BR2_PACKAGE_K3S_DOWNLOAD_BASE_URL)/v$(K3S_VERSION)
K3S_SITE_METHOD = wget

ifeq ($(TARGET_ARCH), arm)
    K3S_SOURCE = k3s-armhf
else ifeq ($(TARGET_ARCH), aarch64)
    K3S_SOURCE = k3s-arm64
else
    K3S_SOURCE = k3s
endif

K3S_LICENSE = Apache-2.0
K3S_LICENSE_FILES = LICENSE

define K3S_EXTRACT_CMDS
	cp $($(PKG)_DL_DIR)/$(K3S_SOURCE) $(@D)
endef

define K3S_INSTALL_TARGET_CMDS
    install -D -m 0755 $(@D)/$(K3S_SOURCE) $(TARGET_DIR)/usr/local/bin/k3s
endef

define K3S_INSTALL_INIT_SYSTEMD
	mkdir -p $(TARGET_DIR)/usr/lib/systemd/system
	sed -e 's%@K3S_SYSTEMD_EXEC_START_ARGS@%$(shell echo $(BR2_PACKAGE_K3S_SYSTEMD_EXEC_START_ARGS))%g' \
		$(K3S_PKGDIR)/k3s.service.in \
		> $(TARGET_DIR)/usr/lib/systemd/system/k3s.service
endef

$(eval $(generic-package))
