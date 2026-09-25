# Zero-Scale Platform Dashboard

A lightweight, zero-node-dependency Kubernetes cluster status dashboard and services directory built for the **Open Cream Cheese** platform and **Zero-Scale Platform** serverless runtime.

## Directory Structure

```
cmd/zero-scale-dashboard/
├── build/                 # Pure Go static asset minifier & bundler
│   └── build.go           # Minifies CSS/JS/HTML using github.com/tdewolff/minify/v2
├── go/                    # Go backend service
│   ├── collector.go       # Kube API & systemd runner metrics collector
│   ├── types.go           # Cluster status & efficiency data types
│   ├── main.go            # HTTP server embedding dist/ assets via embed.FS
│   └── dist/              # Generated production bundles (embedded into binary)
│       ├── index.html
│       ├── style.min.css
│       └── bundle.min.js
└── static/                # Source static assets
    ├── index.html         # Semantic HTML structure
    ├── css/               # Modular CSS stylesheets
    │   ├── 01-variables.css
    │   ├── 02-layout.css
    │   ├── 03-cards.css
    │   ├── 04-components.css
    │   └── 05-modal.css
    └── js/                # Feature-scoped JavaScript modules
        ├── auth.js        # RBAC bearer token management
        ├── metrics.js     # Resource formatting & summary cards
        ├── efficiency.js  # Serverless architecture memory savings
        ├── operators.js   # CloudNativePG & Longhorn operator telemetry
        ├── nodes.js       # Per-node CPU/RAM & top memory consumers
        ├── zeroscale.js   # Zero-scale service cards & lifecycle
        ├── sites.js       # Ingress cluster services directory
        └── app.js         # Async telemetry fetch loop & auto-refresh
```

## Features

- **Pure Go Pipeline (Zero npm / Node.js)**: Uses `github.com/tdewolff/minify/v2` to bundle and minify CSS, JS, and HTML into high-performance distribution assets.
- **Single Embedded Binary**: Embedded in Go via `//go:embed dist/*` and `embed.FS` for instant zero-dependency deployment.
- **Cluster Health & Telemetry**: Total nodes, Ready status, total & allocatable CPU cores, total & used RAM, and usage percentages.
- **Architecture & Memory Savings**: Real-time comparison of serverless zero-scale idle footprint (0 MB) and pooled CloudNativePG HA databases vs. traditional 24/7 baseline pods.
- **Per-Node Telemetry & Memory Breakdown**: Real-time CPU & memory consumption via `metrics.k8s.io`, OS release, kernel version, kubelet version, architecture, and node age/uptime, along with zero-scale footprint vs. top 24/7 memory hogs.
- **Zero-Scale Serverless Services**: Real-time state of all zero-scale applications (`bdash2`, `hopscotch`, etc.) with 10s idle scale-down and isolated Web Log viewer links (`/_logs`).
- **Cluster Services Directory**: Automatically queries in-cluster Ingresses to catalog internal services.
- **JSON API**: Full programmatic data access at `GET /api/status`.

## Build

```bash
# Build static assets (pure Go minifier)
make build-static

# Build complete binary embedding minified static bundle
make build-dashboard
```

## Run

```bash
# Run locally (uses local kubectl context or in-cluster service account)
./zero-scale-dashboard -port 8080

# Run with live local assets for development (bypasses embedded bundle)
./zero-scale-dashboard -port 8080 -static-dir ./cmd/zero-scale-dashboard/static
```
