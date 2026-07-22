GO ?= go

# The CLI ships two binaries: git-aisphere (user-facing login/logout/status/
# install/diagnose) and git-credential-aisphere (the credential helper invoked
# by Git during clone/fetch/push/LFS). Git maps `git aisphere login` to
# git-aisphere and `helper = aisphere` to git-credential-aisphere.
BIN_DIR := bin
LDFLAGS := -s -w -X main.Version=$(VERSION)
BUILD_FLAGS := -buildvcs=false -trimpath -ldflags "$(LDFLAGS)"

ifeq ($(OS),Windows_NT)
VERSION ?= $(shell git describe --tags --always --dirty 2>NUL || echo dev)
EXE := .exe
else
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
EXE :=
endif

# Two explicit targets (no shell for-loop): older Make on Windows runs the
# loop under cmd.exe, which mis-parses for/do/done and silently skips the
# `go build` calls. One rule per binary keeps each build a single command.
.PHONY: help build tidy test clean
.PHONY: build-git-aisphere build-git-credential-aisphere

help:
	@echo "AISphere Git CLI targets:"
	@echo "  make build   - build git-aisphere and git-credential-aisphere into bin/"
	@echo "  make tidy    - go mod tidy"
	@echo "  make test    - go test ./..."
	@echo "  make clean   - remove bin/"

build: build-git-aisphere build-git-credential-aisphere

build-git-aisphere:
	@echo "building git-aisphere..."
	@mkdir -p $(BIN_DIR)
	@$(GO) build $(BUILD_FLAGS) -o $(BIN_DIR)/git-aisphere$(EXE) ./cmd/git-aisphere

build-git-credential-aisphere:
	@echo "building git-credential-aisphere..."
	@mkdir -p $(BIN_DIR)
	@$(GO) build $(BUILD_FLAGS) -o $(BIN_DIR)/git-credential-aisphere$(EXE) ./cmd/git-credential-aisphere

tidy:
	$(GO) mod tidy

test:
	$(GO) test ./...

clean:
	rm -rf $(BIN_DIR)
