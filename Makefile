IMAGE ?= localhost:32000/systemd-runner:latest
NAMESPACE ?= default
GATEWAY_APP ?= zero-scale-gateway
APPS_DIR ?= /opt/apps
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: all build build-gateway build-dashboard build-static build-mock \
        deploy deploy-manifests deploy-dashboard build-and-deploy \
        image push restart full-deploy ship verify status clean install uninstall

all: build

# -------------------------------------------------------------
# Compilation Targets
# -------------------------------------------------------------
build: build-gateway build-mock build-dashboard build-ctl
	@echo "==> All binaries built successfully."

build-gateway:
	@echo "==> Building gateway..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o gateway ./cmd/gateway

build-mock:
	@echo "==> Building mock-app..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o mock-app ./cmd/mock-app

build-ctl:
	@echo "==> Building zero-scale-ctl..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o zero-scale-ctl ./cmd/zero-scale-ctl

build-static:
	@echo "==> Building minified dashboard static assets..."
	go run ./cmd/zero-scale-dashboard/build

build-dashboard: build-static
	@echo "==> Building zero-scale-dashboard..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o zero-scale-dashboard ./cmd/zero-scale-dashboard/go

build-jwks-aggregator:
	@echo "==> Building temporal-jwks-aggregator..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o temporal-jwks-aggregator ./cmd/temporal-jwks-aggregator

# -------------------------------------------------------------
# Kubernetes Manifests
# -------------------------------------------------------------
deploy-manifests:
	@echo "==> Applying the core gateway manifests in deploy/ to namespace '$(NAMESPACE)'..."
	@tmp=$$(mktemp -d) && cp deploy/*.yaml $$tmp/ && \
	  sed -i 's/^namespace: .*/namespace: $(NAMESPACE)/' $$tmp/kustomization.yaml && \
	  kubectl apply -k $$tmp; status=$$?; rm -rf $$tmp; exit $$status

# -------------------------------------------------------------
# Gateway Deployment (Binary + Service Update + Restart)
# -------------------------------------------------------------
deploy-gateway: build-gateway
	@echo "==> Deploying gateway to active pod in namespace '$(NAMESPACE)'..."
	@POD=$$(kubectl get pods -l app=$(GATEWAY_APP) -n $(NAMESPACE) --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'); \
	if [ -z "$$POD" ]; then \
		echo "ERROR: No running pod found with label app=$(GATEWAY_APP) in namespace $(NAMESPACE)"; \
		exit 1; \
	fi; \
	echo "--> Target Pod: $$POD"; \
	echo "--> Copying updated gateway binary to /usr/local/bin/gateway..."; \
	kubectl cp gateway $(NAMESPACE)/$$POD:/usr/local/bin/gateway -c systemd-runner; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- chmod +x /usr/local/bin/gateway; \
	echo "--> Updating /etc/systemd/system/gateway.service..."; \
	kubectl cp services/gateway.service $(NAMESPACE)/$$POD:/etc/systemd/system/gateway.service -c systemd-runner; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- systemctl daemon-reload; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- systemctl restart gateway.service; \
	echo "==> Gateway restarted with fast idle-timeout."

# -------------------------------------------------------------
# Zero-Scale Dashboard Deployment (Binary + Symlinks + Unit Reset)
# -------------------------------------------------------------
deploy-dashboard: build-dashboard
	@echo "==> Deploying zero-scale-dashboard to active pod in namespace '$(NAMESPACE)'..."
	@POD=$$(kubectl get pods -l app=$(GATEWAY_APP) -n $(NAMESPACE) --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'); \
	if [ -z "$$POD" ]; then \
		echo "ERROR: No running pod found with label app=$(GATEWAY_APP) in namespace $(NAMESPACE)"; \
		exit 1; \
	fi; \
	echo "--> Target Pod: $$POD"; \
	echo "--> Copying zero-scale-dashboard binary to $(APPS_DIR)/status.kube.fairfaxmedia.net..."; \
	kubectl cp zero-scale-dashboard $(NAMESPACE)/$$POD:$(APPS_DIR)/status.kube.fairfaxmedia.net -c systemd-runner; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- chmod +x $(APPS_DIR)/status.kube.fairfaxmedia.net; \
	echo "--> Creating host symlinks (status.fairfaxmedia.net, kstat.fairfaxmedia.net, kstat.kube.fairfaxmedia.net)..."; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- ln -sf $(APPS_DIR)/status.kube.fairfaxmedia.net $(APPS_DIR)/status.fairfaxmedia.net; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- ln -sf $(APPS_DIR)/status.kube.fairfaxmedia.net $(APPS_DIR)/kstat.fairfaxmedia.net; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- ln -sf $(APPS_DIR)/status.kube.fairfaxmedia.net $(APPS_DIR)/kstat.kube.fairfaxmedia.net; \
	echo "--> Resetting transient service units for clean reload..."; \
	kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- systemctl stop \
		app-status.kube.fairfaxmedia.net.service \
		app-status.fairfaxmedia.net.service \
		app-kstat.fairfaxmedia.net.service \
		app-kstat.kube.fairfaxmedia.net.service 2>/dev/null || true; \
	echo "==> Dashboard deployed and host mappings active."

# -------------------------------------------------------------
# Health Check Verification
# -------------------------------------------------------------
verify:
	@echo "==> Verifying live dashboard endpoints..."
	@echo -n "Checking https://status.kube.fairfaxmedia.net/ ... "; \
	STATUS1=$$(curl -4 -k -s -o /dev/null -w "%{http_code}" --connect-timeout 8 https://status.kube.fairfaxmedia.net/ || echo "ERR"); \
	echo "$$STATUS1"; \
	echo -n "Checking https://kstat.fairfaxmedia.net/ ... "; \
	STATUS2=$$(curl -k -s -o /dev/null -w "%{http_code}" --connect-timeout 8 https://kstat.fairfaxmedia.net/ || echo "ERR"); \
	echo "$$STATUS2"; \
	if [ "$$STATUS1" = "200" ] && [ "$$STATUS2" = "200" ]; then \
		echo "==> All endpoints operational (200 OK)!"; \
	else \
		echo "WARNING: Some endpoints did not return 200 OK (status.kube: $$STATUS1, kstat: $$STATUS2)"; \
	fi

# -------------------------------------------------------------
# Combined Deploy & Build-and-Deploy
# -------------------------------------------------------------
deploy: deploy-manifests deploy-gateway deploy-dashboard verify
	@echo "==> Deployment complete."

build-and-deploy: build deploy
	@echo "==> Build and Deploy successfully finished!"

# -------------------------------------------------------------
# Container Image & Full Cluster Rollout
# -------------------------------------------------------------
image: build
	@echo "==> Building Docker image $(IMAGE)..."
	docker build -t $(IMAGE) -f Dockerfile.systemd .

push: image
	@echo "==> Pushing Docker image $(IMAGE)..."
	@PF_PID=""; \
	if ! curl -s --connect-timeout 1 http://localhost:32000/v2/ >/dev/null 2>&1; then \
		echo "--> Setting up temporary port-forward to container-registry on port 32000..."; \
		kubectl port-forward -n container-registry svc/registry 32000:5000 >/dev/null 2>&1 & \
		PF_PID=$$!; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			if curl -s --connect-timeout 1 http://localhost:32000/v2/ >/dev/null 2>&1; then \
				break; \
			fi; \
			sleep 0.5; \
		done; \
	fi; \
	docker push $(IMAGE); \
	PUSH_STATUS=$$?; \
	if [ -n "$$PF_PID" ]; then \
		kill "$$PF_PID" 2>/dev/null || true; \
	fi; \
	exit $$PUSH_STATUS

restart:
	@echo "==> Restarting deployment $(GATEWAY_APP)..."
	kubectl rollout restart deployment/$(GATEWAY_APP) -n $(NAMESPACE)
	kubectl rollout status deployment/$(GATEWAY_APP) -n $(NAMESPACE) --timeout=120s

full-deploy: build image push deploy-manifests restart deploy-dashboard verify
	@echo "==> Full stack container build and deployment complete!"

ship: full-deploy

# -------------------------------------------------------------
# Status & Inspection
# -------------------------------------------------------------
status:
	@echo "==> Cluster Pods:"
	@kubectl get pods -l app=$(GATEWAY_APP) -n $(NAMESPACE)
	@echo "\n==> Ingresses:"
	@kubectl get ingress -l app=$(GATEWAY_APP) -n $(NAMESPACE)
	@echo "\n==> Live Apps in $(APPS_DIR):"
	@POD=$$(kubectl get pods -l app=$(GATEWAY_APP) -n $(NAMESPACE) --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'); \
	if [ -n "$$POD" ]; then \
		kubectl exec $$POD -n $(NAMESPACE) -c systemd-runner -- ls -la $(APPS_DIR); \
	fi

clean:
	rm -f gateway mock-app zero-scale-dashboard status-dashboard zero-scale-ctl

# -------------------------------------------------------------
# Local Installation
# -------------------------------------------------------------
install: build-ctl
	@echo "==> Installing zero-scale-ctl to $(BINDIR)..."
	@mkdir -p $(BINDIR)
	install -m 755 zero-scale-ctl $(BINDIR)/zero-scale-ctl
	@echo "✅ Installed: $(BINDIR)/zero-scale-ctl"
	@echo "Run 'zero-scale-ctl help' to verify."

uninstall:
	@echo "==> Removing zero-scale-ctl from $(BINDIR)..."
	rm -f $(BINDIR)/zero-scale-ctl
	@echo "✅ Uninstalled $(BINDIR)/zero-scale-ctl"
