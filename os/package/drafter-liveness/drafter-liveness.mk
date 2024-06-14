################################################################################
#
# drafter-liveness
#
################################################################################

DRAFTER_LIVENESS_VERSION = 5ef1735c45a277f39c3d0df1ba42d5777e304b5a
DRAFTER_LIVENESS_SITE = $(call github,loopholelabs,drafter,$(DRAFTER_LIVENESS_VERSION))

DRAFTER_LIVENESS_LICENSE = AGPL-3.0-or-later
DRAFTER_LIVENESS_LICENSE_FILES = LICENSE

DRAFTER_LIVENESS_GOMOD = github.com/loopholelabs/drafter

DRAFTER_LIVENESS_BUILD_TARGETS = cmd/drafter-liveness

define DRAFTER_LIVENESS_INSTALL_INIT_SYSTEMD
	mkdir -p $(TARGET_DIR)/usr/lib/systemd/system
	sed -e 's%@DRAFTER_LIVENESS_VSOCK_PORT@%$(BR2_PACKAGE_DRAFTER_LIVENESS_VSOCK_PORT)%g' \
		$(DRAFTER_LIVENESS_PKGDIR)/drafter-liveness.service.in \
		> $(TARGET_DIR)/usr/lib/systemd/system/drafter-liveness.service
endef

$(eval $(golang-package))
