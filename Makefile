GO ?= go
BUF ?= buf

KERNEL_VERSION ?= v0.0.2

APP_NAME ?= server
APP_CMD ?= ./cmd/$(APP_NAME)
CONF ?= ./configs/config.yaml
RUN_ARGS ?= -conf $(CONF)

LOCAL_BIN := $(CURDIR)/.bin
BIN_DIR := $(CURDIR)/bin
COVERPROFILE ?= coverage.out

ifeq ($(OS),Windows_NT)
LOCAL_BIN := $(CURDIR)\.bin
BIN_DIR := $(CURDIR)\bin
VERSION ?= $(shell git describe --tags --always --dirty 2>NUL || echo dev)
export PATH := $(LOCAL_BIN);$(PATH)
else
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
export PATH := $(LOCAL_BIN):$(PATH)
endif

.PHONY: help tools check-tools api config wire generate build run test tidy verify clean

help:
	@echo "Kernel service targets:"
	@echo "  make tools        install codegen tools into .bin"
	@echo "  make check-tools  check required tools in .bin"
	@echo "  make api          generate api proto code by buf.gen.yaml"
	@echo "  make config       generate internal config proto code if buf.gen.config.yaml exists"
	@echo "  make wire         generate dependency injection code"
	@echo "  make generate     run go generate"
	@echo "  make build        build service binary"
	@echo "  make run          run service locally"
	@echo "  make test         run all tests"
	@echo "  make tidy         run go mod tidy"
	@echo "  make verify       run api, config, wire, generate, tidy, test, build"
	@echo "  make clean        clean local artifacts"
	@echo ""
	@echo "Variables:"
	@echo "  KERNEL_VERSION=$(KERNEL_VERSION)"
	@echo "  APP_NAME=$(APP_NAME)"
	@echo "  APP_CMD=$(APP_CMD)"
	@echo "  CONF=$(CONF)"

tools:
ifeq ($(OS),Windows_NT)
	@cmd /c "if not exist .bin mkdir .bin"
	@cmd /c "set GOBIN=$(LOCAL_BIN)&& $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11"
	@cmd /c "set GOBIN=$(LOCAL_BIN)&& $(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1"
	@cmd /c "set GOBIN=$(LOCAL_BIN)&& $(GO) install github.com/bufbuild/buf/cmd/buf@v1.50.0"
	@cmd /c "set GOBIN=$(LOCAL_BIN)&& $(GO) install github.com/google/wire/cmd/wire@v0.7.0"
	@cmd /c "set GOBIN=$(LOCAL_BIN)&& $(GO) install github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.1"
else
	@mkdir -p $(LOCAL_BIN)
	@GOBIN=$(LOCAL_BIN) $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	@GOBIN=$(LOCAL_BIN) $(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
	@GOBIN=$(LOCAL_BIN) $(GO) install github.com/bufbuild/buf/cmd/buf@v1.50.0
	@GOBIN=$(LOCAL_BIN) $(GO) install github.com/google/wire/cmd/wire@v0.7.0
	@GOBIN=$(LOCAL_BIN) $(GO) install github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.1
endif

check-tools:
ifeq ($(OS),Windows_NT)
	@cmd /c "if not exist .bin\buf.exe echo missing .bin\buf.exe && exit /b 1"
	@cmd /c "if not exist .bin\wire.exe echo missing .bin\wire.exe && exit /b 1"
	@cmd /c "if not exist .bin\protoc-gen-go.exe echo missing .bin\protoc-gen-go.exe && exit /b 1"
	@cmd /c "if not exist .bin\protoc-gen-go-grpc.exe echo missing .bin\protoc-gen-go-grpc.exe && exit /b 1"
	@cmd /c "if not exist .bin\protoc-gen-go-http.exe echo missing .bin\protoc-gen-go-http.exe && exit /b 1"
	@cmd /c "if not exist .bin\protoc-gen-openapi.exe echo missing .bin\protoc-gen-openapi.exe && exit /b 1"
else
	@test -x "$(LOCAL_BIN)/buf" || (echo "missing $(LOCAL_BIN)/buf"; exit 1)
	@test -x "$(LOCAL_BIN)/wire" || (echo "missing $(LOCAL_BIN)/wire"; exit 1)
	@test -x "$(LOCAL_BIN)/protoc-gen-go" || (echo "missing $(LOCAL_BIN)/protoc-gen-go"; exit 1)
	@test -x "$(LOCAL_BIN)/protoc-gen-go-grpc" || (echo "missing $(LOCAL_BIN)/protoc-gen-go-grpc"; exit 1)
	@test -x "$(LOCAL_BIN)/protoc-gen-go-http" || (echo "missing $(LOCAL_BIN)/protoc-gen-go-http"; exit 1)
	@test -x "$(LOCAL_BIN)/protoc-gen-openapi" || (echo "missing $(LOCAL_BIN)/protoc-gen-openapi"; exit 1)
endif

api: check-tools
ifeq ($(OS),Windows_NT)
	@cmd /c "set PATH=$(LOCAL_BIN);%PATH%&& .bin\buf.exe generate --template buf.gen.yaml"
else
	@PATH="$(LOCAL_BIN):$$PATH" $(LOCAL_BIN)/buf generate --template buf.gen.yaml
endif

config: check-tools
ifeq ($(OS),Windows_NT)
	@cmd /c "set PATH=$(LOCAL_BIN);%PATH%&& if exist buf.gen.config.yaml (.bin\buf.exe generate --template buf.gen.config.yaml) else (echo buf.gen.config.yaml not found; skip config)"
else
	@if [ -f buf.gen.config.yaml ]; then PATH="$(LOCAL_BIN):$$PATH" $(LOCAL_BIN)/buf generate --template buf.gen.config.yaml; else echo "buf.gen.config.yaml not found; skip config"; fi
endif

wire: check-tools
ifeq ($(OS),Windows_NT)
	@cmd /c "set PATH=$(LOCAL_BIN);%PATH%&& .bin\wire.exe ./cmd/$(APP_NAME)"
else
	@PATH="$(LOCAL_BIN):$$PATH" $(LOCAL_BIN)/wire ./cmd/$(APP_NAME)
endif

generate:
	$(GO) generate ./...

build:
ifeq ($(OS),Windows_NT)
	@cmd /c "if not exist bin mkdir bin"
	$(GO) build -ldflags "-X main.Version=$(VERSION)" -o bin\$(APP_NAME).exe $(APP_CMD)
else
	@mkdir -p bin
	$(GO) build -ldflags "-X main.Version=$(VERSION)" -o bin/$(APP_NAME) $(APP_CMD)
endif

run:
	$(GO) run $(APP_CMD) $(RUN_ARGS)

test:
	$(GO) test ./...

tidy:
	$(GO) mod tidy

verify: api config wire generate tidy test build

clean:
ifeq ($(OS),Windows_NT)
	@cmd /c "if exist .bin rmdir /s /q .bin"
	@cmd /c "if exist bin rmdir /s /q bin"
	@cmd /c "if exist $(COVERPROFILE) del /f /q $(COVERPROFILE)"
	@cmd /c "if exist coverage.html del /f /q coverage.html"
else
	rm -rf $(LOCAL_BIN)
	rm -rf bin
	rm -f $(COVERPROFILE) coverage.html
endif