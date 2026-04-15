# === Configuration ===
APP_NAME := aws-go-forward
BUILD_DIR := build
INSTALL_DIR := /usr/local/bin
INTEGRATION_DIR := integration_setup

# === Default ===
.PHONY: all
all: build

# === Build Directory ===
.PHONY: build-dir
build-dir:
	mkdir -p $(BUILD_DIR)

# === Build for host ===
.PHONY: build
build: build-dir
	@echo "Building for host platform..."
	go build -o $(BUILD_DIR)/$(APP_NAME) .

# === Cross-Compilation Targets ===
OSARCH := \
	darwin-amd64 darwin-arm64 \
	linux-amd64 linux-arm64 \
	windows-amd64 windows-arm64 \
	freebsd-amd64 freebsd-arm64

.PHONY: cross $(OSARCH)
cross: $(OSARCH)

$(OSARCH): build-dir
	@echo "Building for $@..."
	GOOS=$(word 1,$(subst -, ,$@)) GOARCH=$(word 2,$(subst -, ,$@)) \
	go build -o $(BUILD_DIR)/$(APP_NAME)-$@$(if $(findstring windows,$@),.exe,) .

# === Install ===
.PHONY: install
install: build
	@echo "Installing to $(INSTALL_DIR)..."
	install -m 0755 $(BUILD_DIR)/$(APP_NAME) $(INSTALL_DIR)/$(APP_NAME)

# === Unit Tests ===
.PHONY: test
test:
	go test ./...

# === Go Maintenance ===
.PHONY: fmt vet check
fmt:
	go fmt ./...

vet:
	go vet ./...

check: fmt vet test

# === Terraform Integration Environment ===
.PHONY: integration-up
integration-up:
	cd $(INTEGRATION_DIR) && terraform init && terraform apply -auto-approve
	@echo ""
	@echo "=== Connection Instructions ==="
	@terraform -chdir=$(INTEGRATION_DIR) output -raw aws_go_forward_command || true

.PHONY: integration-down clean-test
integration-down:
	cd $(INTEGRATION_DIR) && terraform destroy -auto-approve

clean-test: integration-down

# === Clean ===
.PHONY: clean
clean:
	@echo "Cleaning up..."
	rm -rf $(BUILD_DIR)
