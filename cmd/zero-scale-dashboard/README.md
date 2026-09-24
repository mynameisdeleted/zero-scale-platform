# Zero-Scale Platform Dashboard

A lightweight, zero-dependency Kubernetes cluster status dashboard and services directory built for the **Open Cream Cheese** platform and **Zero-Scale Platform** runtime.

## Features

- **Cluster Health & Overview**: Total nodes, Ready status, total & allocatable CPU cores, total & used RAM, and aggregate usage percentages.
- **Per-Node Resource Metrics**: Real-time CPU & memory consumption via `metrics.k8s.io`, OS release, kernel version, kubelet version, architecture, and node age/uptime.
- **Zero-Scale Runtime Services**: Real-time state of all zero-scale applications (`bdash2`, `hopscotch`, etc.) with direct app links and isolated Web Log viewer links (`/_logs`).
- **Cluster Services Directory**: Automatically queries in-cluster Ingresses to catalog internal services (Gitea, Kubernetes Dashboard, Prometheus, MinIO, Nextcloud, Odoo, Mastodon, Syncthing, PAPI).
- **Direct Source Link**: Prominent navigation link to `https://git.fairfaxmedia.net/`.
- **JSON API**: Full programmatic data access at `GET /api/status`.

## Build

```bash
go build -o zero-scale-dashboard ./cmd/zero-scale-dashboard
```

## Run

```bash
# Run locally (uses local kubectl context or in-cluster service account)
./zero-scale-dashboard -port 8080
```
