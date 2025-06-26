# === Configuration ===
APP_NAME := aws-go-forward
BUILD_DIR := build
INSTALL_DIR := /usr/local/bin
TEST_DIR := test_setup

# === Default ===
.PHONY: all
all: build

# === Build for host ===
.PHONY: build
build:
	@echo "Building for host platform..."
	go build -o $(APP_NAME) .

# === Cross-Compilation Targets ===
OSARCH := \
	darwin-amd64 darwin-arm64 \
	linux-amd64 linux-arm64 \
	windows-amd64 windows-arm64 \
	freebsd-amd64 freebsd-arm64

$(OSARCH):
	@echo "Building for $@..."
	GOOS=$(word 1,$(subst -, ,$@)) GOARCH=$(word 2,$(subst -, ,$@)) \
	go build -o $(BUILD_DIR)/$(APP_NAME)-$@$(if $(findstring windows,$@),.exe,) .

# === Install ===
.PHONY: install
install: build
	@echo "Installing to $(INSTALL_DIR)..."
	install -m 0755 $(APP_NAME) $(INSTALL_DIR)/$(APP_NAME)

# === Terraform test ===
.PHONY: test
test:
	cd $(TEST_DIR) && terraform init && terraform apply -auto-approve

.PHONY: clean-test
clean-test:
	cd $(TEST_DIR) && terraform destroy -auto-approve

# === Clean ===
.PHONY: clean
clean:
	@echo "Cleaning up..."
	rm -f $(APP_NAME)
	rm -rf $(BUILD_DIR)
