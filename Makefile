# Public variables
DESTDIR ?=
PREFIX ?= /usr/local
OUTPUT_DIR ?= out
DST ?=

# Private variables
obj = architekt-daemon architekt-packager architekt-runner architekt-registry architekt-peer architekt-manager architekt-worker architekt-operator
all: $(addprefix build/,$(obj))

# Build
build: $(addprefix build/,$(obj))
$(addprefix build/,$(obj)):
ifdef DST
	go build -o $(DST) ./cmd/$(subst build/,,$@)
else
	go build -o $(OUTPUT_DIR)/$(subst build/,,$@) ./cmd/$(subst build/,,$@)
endif

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

# Operator
operator/install:
	kustomize build config/crd | kubectl apply -f -

operator/uninstall:
	kustomize build config/crd | kubectl delete --ignore-not-found=false -f -

# Clean
clean:
	rm -rf out

# Dependencies
depend:
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

	controller-gen rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	controller-gen object paths="./..."

	go generate ./...