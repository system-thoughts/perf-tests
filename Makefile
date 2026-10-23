# # Copyright The Dragonfly Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Registry to push the images to, e.g. D7Y_REGISTRY=ghcr.io/dragonflyoss make docker-push-image-bench-images.
D7Y_REGISTRY ?= dragonflyoss

all: help

# Build file-server image.
docker-build-file-server:
	@echo "Begin to use docker build file-server image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/file-server:latest -f ./tools/file-server/Dockerfile .
.PHONY: docker-build-file-server

# Push file-server image.
docker-push-file-server:
	@echo "Begin to push file-server docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/file-server:latest -f ./tools/file-server/Dockerfile .
.PHONY: docker-push-file-server

# Build proxy-bench image.
docker-build-proxy-bench: build-proxy-bench-tools
	@echo "Begin to use docker build proxy-bench image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/proxy-bench:latest -f ./tools/proxy-bench/Dockerfile .
.PHONY: docker-build-proxy-bench

# Push proxy-bench image.
docker-push-proxy-bench: build-proxy-bench-tools
	@echo "Begin to push proxy-bench docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/proxy-bench:latest -f ./tools/proxy-bench/Dockerfile .
.PHONY: docker-push-proxy-bench

# Build file-bench image.
docker-build-file-bench:
	@echo "Begin to use docker build file-bench image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/file-bench:latest -f ./tools/file-bench/Dockerfile .
.PHONY: docker-build-file-bench

# Push file-bench image.
docker-push-file-bench:
	@echo "Begin to push file-bench docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/file-bench:latest -f ./tools/file-bench/Dockerfile .
.PHONY: docker-push-file-bench

# Build dfget-bench image.
docker-build-dfget-bench:
	@echo "Begin to use docker build dfget-bench image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/dfget-bench:latest -f ./tools/dfget-bench/Dockerfile .
.PHONY: docker-build-dfget-bench

# Push dfget-bench image.
docker-push-dfget-bench:
	@echo "Begin to push dfget-bench docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/dfget-bench:latest -f ./tools/dfget-bench/Dockerfile .
.PHONY: docker-push-dfget-bench

# Build image-bench image.
docker-build-image-bench:
	@echo "Begin to use docker build image-bench image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/image-bench:latest -f ./tools/image-bench/Dockerfile .
.PHONY: docker-build-image-bench

# Push image-bench image.
docker-push-image-bench:
	@echo "Begin to push image-bench docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/image-bench:latest -f ./tools/image-bench/Dockerfile .
.PHONY: docker-push-image-bench

# Build all image-bench images pulled by the image benchmark, see build/images/image-bench.
docker-build-image-bench-images: docker-build-image-bench-v1-1gb-4 docker-build-image-bench-v1-2gb-8 docker-build-image-bench-v1-4gb-8 docker-build-image-bench-v1-10gb-10 docker-build-image-bench-v1-20gb-4
	@echo "Build image-bench images done."
.PHONY: docker-build-image-bench-images

# Push all image-bench images pulled by the image benchmark.
docker-push-image-bench-images: docker-push-image-bench-v1-1gb-4 docker-push-image-bench-v1-2gb-8 docker-push-image-bench-v1-4gb-8 docker-push-image-bench-v1-10gb-10 docker-push-image-bench-v1-20gb-4
	@echo "Push image-bench images done."
.PHONY: docker-push-image-bench-images

# Build image-bench:v1-1gb-4 (1 GiB, 256 MiB x 4 layers).
docker-build-image-bench-v1-1gb-4:
	@echo "Begin to use docker build image-bench:v1-1gb-4 image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/image-bench:v1-1gb-4 -f ./build/images/image-bench/v1-1gb-4/Dockerfile .
.PHONY: docker-build-image-bench-v1-1gb-4

# Build image-bench:v1-2gb-8 (2 GiB, 256 MiB x 8 layers).
docker-build-image-bench-v1-2gb-8:
	@echo "Begin to use docker build image-bench:v1-2gb-8 image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/image-bench:v1-2gb-8 -f ./build/images/image-bench/v1-2gb-8/Dockerfile .
.PHONY: docker-build-image-bench-v1-2gb-8

# Build image-bench:v1-4gb-8 (4 GiB, 512 MiB x 8 layers).
docker-build-image-bench-v1-4gb-8:
	@echo "Begin to use docker build image-bench:v1-4gb-8 image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/image-bench:v1-4gb-8 -f ./build/images/image-bench/v1-4gb-8/Dockerfile .
.PHONY: docker-build-image-bench-v1-4gb-8

# Build image-bench:v1-10gb-10 (10 GiB, 1 GiB x 10 layers).
docker-build-image-bench-v1-10gb-10:
	@echo "Begin to use docker build image-bench:v1-10gb-10 image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/image-bench:v1-10gb-10 -f ./build/images/image-bench/v1-10gb-10/Dockerfile .
.PHONY: docker-build-image-bench-v1-10gb-10

# Build image-bench:v1-20gb-4 (20 GiB, 5 GiB x 4 layers).
docker-build-image-bench-v1-20gb-4:
	@echo "Begin to use docker build image-bench:v1-20gb-4 image."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(D7Y_REGISTRY)/image-bench:v1-20gb-4 -f ./build/images/image-bench/v1-20gb-4/Dockerfile .
.PHONY: docker-build-image-bench-v1-20gb-4

# Push image-bench:v1-1gb-4.
docker-push-image-bench-v1-1gb-4:
	@echo "Begin to push image-bench:v1-1gb-4 docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/image-bench:v1-1gb-4 -f ./build/images/image-bench/v1-1gb-4/Dockerfile .
.PHONY: docker-push-image-bench-v1-1gb-4

# Push image-bench:v1-2gb-8.
docker-push-image-bench-v1-2gb-8:
	@echo "Begin to push image-bench:v1-2gb-8 docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/image-bench:v1-2gb-8 -f ./build/images/image-bench/v1-2gb-8/Dockerfile .
.PHONY: docker-push-image-bench-v1-2gb-8

# Push image-bench:v1-4gb-8.
docker-push-image-bench-v1-4gb-8:
	@echo "Begin to push image-bench:v1-4gb-8 docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/image-bench:v1-4gb-8 -f ./build/images/image-bench/v1-4gb-8/Dockerfile .
.PHONY: docker-push-image-bench-v1-4gb-8

# Push image-bench:v1-10gb-10.
docker-push-image-bench-v1-10gb-10:
	@echo "Begin to push image-bench:v1-10gb-10 docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/image-bench:v1-10gb-10 -f ./build/images/image-bench/v1-10gb-10/Dockerfile .
.PHONY: docker-push-image-bench-v1-10gb-10

# Push image-bench:v1-20gb-4.
docker-push-image-bench-v1-20gb-4:
	@echo "Begin to push image-bench:v1-20gb-4 docker image."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t $(D7Y_REGISTRY)/image-bench:v1-20gb-4 -f ./build/images/image-bench/v1-20gb-4/Dockerfile .
.PHONY: docker-push-image-bench-v1-20gb-4

# Build fixed-pool preparation commands.
build-proxy-bench-data-prepare:
	@mkdir -p bin
	go build -mod=readonly -o bin/proxy-bench-data-prepare ./tools/proxy-bench/fixed-pool/cmd/data-prepare
.PHONY: build-proxy-bench-data-prepare

build-proxy-bench-plan-prepare:
	@mkdir -p bin
	go build -mod=readonly -o bin/proxy-bench-plan-prepare ./tools/proxy-bench/fixed-pool/cmd/plan-prepare
.PHONY: build-proxy-bench-plan-prepare

# Build the Seed-cache preparation command.
build-proxy-bench-seed-cache-prepare:
	@mkdir -p bin
	go build -mod=readonly -o bin/proxy-bench-seed-cache-prepare ./tools/proxy-bench/fixed-pool/cmd/seed-cache-prepare
.PHONY: build-proxy-bench-seed-cache-prepare

# Build the internal worker input/report helper.
build-proxy-bench-worker-support:
	@mkdir -p bin
	go build -mod=readonly -o bin/proxy-bench-worker-support ./tools/proxy-bench/fixed-pool/cmd/worker-support
.PHONY: build-proxy-bench-worker-support

# Build static preparation and worker binaries for images and distribution.
build-proxy-bench-tools:
	@for arch in amd64 arm64; do \
		mkdir -p bin/proxy-bench-tools/$$arch; \
		for command in data-prepare plan-prepare seed-cache-prepare worker-support; do \
			CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -mod=readonly \
				-o bin/proxy-bench-tools/$$arch/proxy-bench-$$command \
				./tools/proxy-bench/fixed-pool/cmd/$$command || exit $$?; \
		done; \
	done
.PHONY: build-proxy-bench-tools

docker-build-proxy-bench-tools: build-proxy-bench-tools
	docker buildx build --platform linux/amd64,linux/arm64 --output type=oci,dest=bin/proxy-bench-tools-image.tar -t $(D7Y_REGISTRY)/proxy-bench-tools:latest -f tools/proxy-bench/fixed-pool/Dockerfile.tools .
.PHONY: docker-build-proxy-bench-tools

# Run code lint
lint: markdownlint
	@echo "Begin to golangci-lint."
	@golangci-lint run
.PHONY: lint

# Run markdown lint
markdownlint:
	@echo "Begin to markdownlint."
	@./hack/markdownlint.sh
.PHONY: markdownlint

# Run unit tests
test:
	@echo "Begin to run unit tests."
	@go test -mod=readonly -count=1 ./...
.PHONY: test

# Run unit tests with the race detector
test-race:
	@echo "Begin to run unit tests with race detector."
	@go test -race -mod=readonly -count=1 ./...
.PHONY: test-race

# Clear compiled files
clean:
	@go clean
	@rm -rf bin .go .cache
.PHONY: clean

help: 
	@echo "make build-proxy-bench-worker-support       build the internal worker helper"
	@echo "make build-proxy-bench-seed-cache-prepare   build the fixed-pool Seed preparation command"
	@echo "make build-proxy-bench-tools                build amd64/arm64 preparation and worker binaries"
	@echo "make docker-build-proxy-bench-tools         build the preparation tools image"
	@echo "make build-proxy-bench-data-prepare         build the fixed-pool dataset command"
	@echo "make build-proxy-bench-plan-prepare         build the fixed-pool access-plan command"
	@echo "make docker-build-file-server               build file-server image"
	@echo "make docker-push-file-server                push file-server image"
	@echo "make docker-build-proxy-bench               build proxy-bench image"
	@echo "make docker-push-proxy-bench                push proxy-bench image"
	@echo "make docker-build-file-bench                build file-bench image"
	@echo "make docker-push-file-bench                 push file-bench image"
	@echo "make docker-build-dfget-bench               build dfget-bench image"
	@echo "make docker-push-dfget-bench                push dfget-bench image"
	@echo "make docker-build-image-bench               build image-bench image"
	@echo "make docker-push-image-bench                push image-bench image"
	@echo "make docker-build-image-bench-images        build all image-bench images pulled by the image benchmark"
	@echo "make docker-build-image-bench-v1-1gb-4      build image-bench:v1-1gb-4 (1 GiB, 256 MiB x 4 layers)"
	@echo "make docker-build-image-bench-v1-2gb-8      build image-bench:v1-2gb-8 (2 GiB, 256 MiB x 8 layers)"
	@echo "make docker-build-image-bench-v1-4gb-8      build image-bench:v1-4gb-8 (4 GiB, 512 MiB x 8 layers)"
	@echo "make docker-build-image-bench-v1-10gb-10    build image-bench:v1-10gb-10 (10 GiB, 1 GiB x 10 layers)"
	@echo "make docker-build-image-bench-v1-20gb-4     build image-bench:v1-20gb-4 (20 GiB, 5 GiB x 4 layers)"
	@echo "make docker-push-image-bench-images         push all image-bench images pulled by the image benchmark"
	@echo "make docker-push-image-bench-v1-1gb-4       push image-bench:v1-1gb-4"
	@echo "make docker-push-image-bench-v1-2gb-8       push image-bench:v1-2gb-8"
	@echo "make docker-push-image-bench-v1-4gb-8       push image-bench:v1-4gb-8"
	@echo "make docker-push-image-bench-v1-10gb-10     push image-bench:v1-10gb-10"
	@echo "make docker-push-image-bench-v1-20gb-4      push image-bench:v1-20gb-4"
	@echo "make lint                                   run code lint"
	@echo "make markdownlint                           run markdown lint"
	@echo "make test                                   run unit tests"
	@echo "make test-race                              run unit tests with race detector"
	@echo "make clean                                  clean"
