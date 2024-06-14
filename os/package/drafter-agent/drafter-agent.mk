################################################################################
#
# drafter-agent
#
################################################################################

DRAFTER_AGENT_VERSION = 5ef1735c45a277f39c3d0df1ba42d5777e304b5a
DRAFTER_AGENT_SITE = $(call github,loopholelabs,drafter,$(DRAFTER_AGENT_VERSION))

DRAFTER_AGENT_LICENSE = AGPL-3.0-or-later
DRAFTER_AGENT_LICENSE_FILES = LICENSE

DRAFTER_AGENT_GOMOD = github.com/loopholelabs/drafter

DRAFTER_AGENT_BUILD_TARGETS = cmd/drafter-agent

define DRAFTER_AGENT_INSTALL_INIT_SYSTEMD
	mkdir -p $(TARGET_DIR)/usr/lib/systemd/system
	sed -e 's%@DRAFTER_AGENT_VSOCK_PORT@%$(BR2_PACKAGE_DRAFTER_AGENT_VSOCK_PORT)%g' \
		-e 's%@DRAFTER_AGENT_SYSTEMD_DEPENDENCY@%$(BR2_PACKAGE_DRAFTER_AGENT_SYSTEMD_DEPENDENCY)%g' \
		$(DRAFTER_AGENT_PKGDIR)/drafter-agent.service.in \
		> $(TARGET_DIR)/usr/lib/systemd/system/drafter-agent.service
endef

$(eval $(golang-package))
