################################################################################
#
# drafter-agent
#
################################################################################

DRAFTER_AGENT_VERSION = 1.0
DRAFTER_AGENT_SITE = "$(BR2_EXTERNAL_LOOPHOLE_LABS_DRAFTER_OS_PATH)/.."
DRAFTER_AGENT_SITE_METHOD = local
DRAFTER_AGENT_OVERRIDE_SRCDIR_RSYNC_EXCLUSIONS = --exclude out

DRAFTER_AGENT_LICENSE = AGPL-3.0-or-later
DRAFTER_AGENT_LICENSE_FILES = LICENSE

DRAFTER_AGENT_GOMOD = github.com/loopholelabs/drafter

DRAFTER_AGENT_BUILD_TARGETS = cmd/drafter-agent

define DRAFTER_AGENT_CONFIGURE_CMDS
	cd $(@D) && GOROOT="$(HOST_GO_ROOT)" GOPATH="$(HOST_GO_GOPATH)" GOPROXY="direct" $(GO_BIN) mod vendor
endef

define DRAFTER_AGENT_INSTALL_INIT_SYSTEMD
	mkdir -p $(TARGET_DIR)/usr/lib/systemd/system
	sed -e "s%@DRAFTER_AGENT_VSOCK_PORT@%$(BR2_PACKAGE_DRAFTER_AGENT_VSOCK_PORT)%g" \
		-e "s%@DRAFTER_AGENT_SYSTEMD_DEPENDENCY@%$(BR2_PACKAGE_DRAFTER_AGENT_SYSTEMD_DEPENDENCY)%g" \
		$(DRAFTER_AGENT_PKGDIR)/drafter-agent.service.in \
		> $(TARGET_DIR)/usr/lib/systemd/system/drafter-agent.service
endef

$(eval $(golang-package))
