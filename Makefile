IMAGE ?= localhost:32000/systemd-runner:latest
NAMESPACE ?= default

.PHONY: all build image push deploy restart clean

all: build

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o gateway ./cmd/gateway
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o mock-app ./cmd/mock-app
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o status-dashboard ./cmd/status-dashboard

image: build
	docker build -t $(IMAGE) -f Dockerfile.systemd .

push: image
	docker push $(IMAGE)

deploy:
	kubectl apply -f deploy/deployment.yaml -n $(NAMESPACE)
	kubectl apply -f deploy/service.yaml -n $(NAMESPACE)
	kubectl apply -f deploy/ingress.yaml -n $(NAMESPACE)

restart:
	kubectl rollout restart deployment/zero-scale-gateway -n $(NAMESPACE)

clean:
	rm -f gateway mock-app status-dashboard
