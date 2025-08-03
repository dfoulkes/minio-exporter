# Copyright 2017 Giuseppe Pellegrino
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO       ?= GO15VENDOREXPERIMENT=1 go
GOPATH   := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
TARGET    = minio_exporter

VERSION             := $(shell cat VERSION)
OS                  := $(shell uname | tr A-Z a-z)
ARCH                := $(shell uname -p)
ARCHIVE             := $(TARGET)-$(VERSION)-$(OS)-$(ARCH).tar.gz

DOCKER_IMAGE_NAME   ?= ghcr.io/dfoulkes/minio-exporter
DOCKER_IMAGE_TAG    ?= v$(VERSION)

# Kubernetes deployment variables
NAMESPACE           := monitoring
RELEASE_NAME        := minio-exporter
CHART_PATH          := ./helm/minio-exporter
VALUES_FILE         := $(CHART_PATH)/values-k3s.yaml

pkgs := $(shell $(GO) list)

all: format build test

build: get_dep
	@echo "... building binaries"
	@CGO_ENABLED=0 $(GO) build -o $(TARGET) -a -installsuffix cgo $(pkgs)

format:
	@echo "... formatting packages"
	@$(GO) fmt $(pkgs)

test:
	@echo "... testing binary"
	@$(GO) test -short $(pkgs)

get_dep:
	@echo "... getting dependencies"
	@$(GO) get -d

docker: docker_build docker_push
	@echo "... docker building and pushing"

docker_build: build
	@echo "... building docker image"
	@docker build -t $(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG) .

docker_push:
	@echo "... pushing docker image"
	@docker push $(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)

clean:
	@echo "... cleaning up"
	@rm -rf $(TARGET) $(ARCHIVE) .build

tarball: $(ARCHIVE)
$(ARCHIVE): $(TARGET)
	@echo "... creating tarball"
	@tar -czf $@ $<

# Helm deployment targets
helm-check:
	@echo "... checking Helm and kubectl availability"
	@which helm >/dev/null || (echo "ERROR: helm not found in PATH" && exit 1)
	@which kubectl >/dev/null || (echo "ERROR: kubectl not found in PATH" && exit 1)
	@echo "... checking Kubernetes connection"
	@kubectl cluster-info >/dev/null || (echo "ERROR: Cannot connect to Kubernetes cluster" && exit 1)

helm-namespace: helm-check
	@echo "... ensuring namespace $(NAMESPACE) exists"
	@kubectl get namespace $(NAMESPACE) >/dev/null 2>&1 || kubectl create namespace $(NAMESPACE)

helm-deploy-anonymous: helm-namespace
	@echo "... deploying minio-exporter without credentials"
	@if [ ! -f "$(VALUES_FILE)" ]; then \
		echo "ERROR: $(VALUES_FILE) not found. Please create it first."; \
		exit 1; \
	fi
	@helm upgrade --install $(RELEASE_NAME) $(CHART_PATH) \
		--namespace $(NAMESPACE) \
		--values $(VALUES_FILE) \
		--wait
	@$(MAKE) helm-status

helm-deploy: helm-namespace
	@echo "... deploying minio-exporter with credentials"
	@if [ -z "$(MINIO_ACCESS_KEY)" ] || [ -z "$(MINIO_ACCESS_SECRET)" ]; then \
		echo "ERROR: MINIO_ACCESS_KEY and MINIO_ACCESS_SECRET must be set"; \
		echo "Usage: make helm-deploy MINIO_ACCESS_KEY=xxx MINIO_ACCESS_SECRET=yyy"; \
		exit 1; \
	fi
	@if [ ! -f "$(VALUES_FILE)" ]; then \
		echo "ERROR: $(VALUES_FILE) not found. Please create it first."; \
		exit 1; \
	fi
	@helm upgrade --install $(RELEASE_NAME) $(CHART_PATH) \
		--namespace $(NAMESPACE) \
		--values $(VALUES_FILE) \
		--set minioExporter.accessKey="$(MINIO_ACCESS_KEY)" \
		--set minioExporter.accessSecret="$(MINIO_ACCESS_SECRET)" \
		--wait
	@$(MAKE) helm-status

helm-deploy-with-monitoring: helm-namespace
	@echo "... deploying minio-exporter with Prometheus monitoring"
	@if [ -z "$(MINIO_ACCESS_KEY)" ] || [ -z "$(MINIO_ACCESS_SECRET)" ]; then \
		echo "ERROR: MINIO_ACCESS_KEY and MINIO_ACCESS_SECRET must be set"; \
		echo "Usage: make helm-deploy-with-monitoring MINIO_ACCESS_KEY=xxx MINIO_ACCESS_SECRET=yyy"; \
		exit 1; \
	fi
	@if [ ! -f "$(VALUES_FILE)" ]; then \
		echo "ERROR: $(VALUES_FILE) not found. Please create it first."; \
		exit 1; \
	fi
	@helm upgrade --install $(RELEASE_NAME) $(CHART_PATH) \
		--namespace $(NAMESPACE) \
		--values $(VALUES_FILE) \
		--set minioExporter.accessKey="$(MINIO_ACCESS_KEY)" \
		--set minioExporter.accessSecret="$(MINIO_ACCESS_SECRET)" \
		--set serviceMonitor.enabled=true \
		--wait
	@$(MAKE) helm-status

helm-uninstall: helm-check
	@echo "... uninstalling minio-exporter"
	@helm uninstall $(RELEASE_NAME) --namespace $(NAMESPACE) || true

helm-status: helm-check
	@echo ""
	@echo "=== Deployment Status ==="
	@kubectl get pods -n $(NAMESPACE) -l app.kubernetes.io/name=minio-exporter
	@echo ""
	@echo "=== Useful Commands ==="
	@echo "Check logs:    kubectl logs -n $(NAMESPACE) -l app.kubernetes.io/name=minio-exporter"
	@echo "Port forward:  kubectl port-forward -n $(NAMESPACE) svc/$(RELEASE_NAME) 9290:9290"
	@echo "Test metrics:  curl http://localhost:9290/metrics"
	@echo ""

helm-logs: helm-check
	@echo "... showing minio-exporter logs"
	@kubectl logs -n $(NAMESPACE) -l app.kubernetes.io/name=minio-exporter --tail=50

helm-port-forward: helm-check
	@echo "... port forwarding to localhost:9290"
	@echo "Access metrics at: http://localhost:9290/metrics"
	@kubectl port-forward -n $(NAMESPACE) svc/$(RELEASE_NAME) 9290:9290

.PHONY: all build format test docker_build docker_push tarball clean \
	helm-check helm-namespace helm-deploy-anonymous helm-deploy helm-deploy-with-monitoring \
	helm-uninstall helm-status helm-logs helm-port-forward
