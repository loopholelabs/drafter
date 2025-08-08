################################################################################
#
# drafter-liveness
#
################################################################################

DRAFTER_LIVENESS_VERSION = 1.0
DRAFTER_LIVENESS_SITE = "$(BR2_EXTERNAL_LOOPHOLE_LABS_DRAFTER_OS_PATH)/../.."
DRAFTER_LIVENESS_SITE_METHOD = local
DRAFTER_LIVENESS_OVERRIDE_SRCDIR_RSYNC_EXCLUSIONS = --exclude out

DRAFTER_LIVENESS_LICENSE = AGPL-3.0-or-later
DRAFTER_LIVENESS_LICENSE_FILES = LICENSE

DRAFTER_LIVENESS_GOMOD = github.com/loopholelabs/drafter

DRAFTER_LIVENESS_BUILD_TARGETS = cmd/drafter-liveness

define DRAFTER_LIVENESS_CONFIGURE_CMDS
	cd $(@D) && GOROOT="$(HOST_GO_ROOT)" GOPATH="$(HOST_GO_GOPATH)" GOPROXY="direct" $(GO_BIN) mod vendor
endef

define DRAFTER_LIVENESS_INSTALL_INIT_SYSTEMD
	mkdir -p $(TARGET_DIR)/usr/lib/systemd/system
	sed -e "s%@DRAFTER_LIVENESS_VSOCK_PORT@%$(BR2_PACKAGE_DRAFTER_LIVENESS_VSOCK_PORT)%g" \
		-e "s%@DRAFTER_LIVENESS_SYSTEMD_DEPENDENCY@%$(BR2_PACKAGE_DRAFTER_LIVENESS_SYSTEMD_DEPENDENCY)%g" \
		$(DRAFTER_LIVENESS_PKGDIR)/drafter-liveness.service.in \
		> $(TARGET_DIR)/usr/lib/systemd/system/drafter-liveness.service
endef

$(eval $(golang-package))
