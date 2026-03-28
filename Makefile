# Makefile
IMG ?= ghcr.io/ClaudioBotelhOSB/taonode-guardian:latest
PROBE_IMG ?= ghcr.io/ClaudioBotelhOSB/taonode-guardian-probe:latest

.PHONY: generate manifests test build docker-build install run lint e2e

generate:
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."
	controller-gen rbac:roleName=taonode-guardian-manager-role crd webhook \
		paths="./..." output:crd:artifacts:config=config/crd/bases

manifests: generate
	cd config/manager && kustomize edit set image controller=$(IMG)
	mkdir -p dist
	kustomize build config/default > dist/install.yaml

test:
	KUBEBUILDER_ASSETS="$$(setup-envtest use -p path)" go test ./... -coverprofile cover.out -count=1

lint:
	golangci-lint run ./...

build:
	CGO_ENABLED=0 go build -o bin/manager cmd/main.go

docker-build:
	docker build -t $(IMG) .
	docker build -t $(PROBE_IMG) hack/chain-probe/

install: manifests
	kubectl apply -f config/crd/bases/

run: generate
	go run ./cmd/main.go --leader-elect=false --zap-log-level=debug

e2e:
	kind create cluster --name taonode-e2e || true
	make install
	go test ./test/e2e/... -v -count=1
