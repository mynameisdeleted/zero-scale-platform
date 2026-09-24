package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

type Collector struct {
	k8sHost    string
	k8sPort    string
	token      string
	client     *http.Client
	appsDir    string
	gatewayURL string

	cacheMu   sync.RWMutex
	cachedRaw *ClusterStatus
	cacheTime time.Time
	cacheTTL  time.Duration
}

func NewCollector(gatewayURL, appsDir string) *Collector {
	tokenBytes, _ := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	token := strings.TrimSpace(string(tokenBytes))

	k8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if k8sHost == "" {
		k8sHost = "kubernetes.default.svc"
	}
	k8sPort := os.Getenv("KUBERNETES_SERVICE_PORT")
	if k8sPort == "" {
		k8sPort = "443"
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	if gatewayURL == "" {
		gatewayURL = "http://zero-scale-gateway.default.svc.cluster.local"
	}
	if appsDir == "" {
		appsDir = "/opt/apps"
	}

	return &Collector{
		k8sHost:    k8sHost,
		k8sPort:    k8sPort,
		token:      token,
		client:     client,
		appsDir:    appsDir,
		gatewayURL: gatewayURL,
		cacheTTL:   3 * time.Second,
	}
}

func (c *Collector) k8sGet(path string) ([]byte, error) {
	// If in-cluster token is available, use HTTPS API directly
	if c.token != "" {
		reqURL := fmt.Sprintf("https://%s:%s%s", c.k8sHost, c.k8sPort, path)
		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				return body, nil
			}
		}
	}

	// Fallback to kubectl get --raw for local out-of-cluster execution
	out, err := exec.Command("kubectl", "get", "--raw", path).Output()
	if err == nil {
		return out, nil
	}
	return nil, fmt.Errorf("failed to query k8s API path %s: %v", path, err)
}

func (c *Collector) Collect() (*ClusterStatus, error) {
	status := &ClusterStatus{
		Timestamp: time.Now(),
		GitURL:    "https://git.fairfaxmedia.net/",
	}

	var wg sync.WaitGroup
	var nodesErr, metricsErr, ingressErr error
	var rawNodes, rawMetrics, rawIngresses []byte
	var rawCnpgDeploy, rawCnpgClusters, rawLonghornDs, rawLonghornDeploy, rawLonghornVols []byte
	var cnpgDeployErr, cnpgClustersErr, longhornDsErr, longhornDeployErr, longhornVolsErr error

	wg.Add(8)
	go func() {
		defer wg.Done()
		rawNodes, nodesErr = c.k8sGet("/api/v1/nodes")
	}()
	go func() {
		defer wg.Done()
		rawMetrics, metricsErr = c.k8sGet("/apis/metrics.k8s.io/v1beta1/nodes")
	}()
	go func() {
		defer wg.Done()
		rawIngresses, ingressErr = c.k8sGet("/apis/networking.k8s.io/v1/ingresses")
	}()
	go func() {
		defer wg.Done()
		rawCnpgDeploy, cnpgDeployErr = c.k8sGet("/apis/apps/v1/namespaces/cnpg-system/deployments")
	}()
	go func() {
		defer wg.Done()
		rawCnpgClusters, cnpgClustersErr = c.k8sGet("/apis/postgresql.cnpg.io/v1/namespaces/default/clusters")
	}()
	go func() {
		defer wg.Done()
		rawLonghornDs, longhornDsErr = c.k8sGet("/apis/apps/v1/namespaces/longhorn-system/daemonsets")
	}()
	go func() {
		defer wg.Done()
		rawLonghornDeploy, longhornDeployErr = c.k8sGet("/apis/apps/v1/namespaces/longhorn-system/deployments")
	}()
	go func() {
		defer wg.Done()
		rawLonghornVols, longhornVolsErr = c.k8sGet("/apis/longhorn.io/v1beta2/namespaces/longhorn-system/volumes")
	}()
	wg.Wait()

	if nodesErr != nil {
		return nil, fmt.Errorf("error collecting nodes: %v", nodesErr)
	}

	// Parse node metrics map (nodeName -> cpuNano, memKi)
	type metricUsage struct {
		cpuNano int64
		memKi   int64
	}
	nodeMetrics := make(map[string]metricUsage)
	if metricsErr == nil && len(rawMetrics) > 0 {
		var metricList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Usage struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawMetrics, &metricList); err == nil {
			for _, item := range metricList.Items {
				cpuStr := strings.TrimSuffix(item.Usage.CPU, "n")
				cpuNano, _ := strconv.ParseInt(cpuStr, 10, 64)
				memStr := strings.TrimSuffix(item.Usage.Memory, "Ki")
				memKi, _ := strconv.ParseInt(memStr, 10, 64)
				nodeMetrics[item.Metadata.Name] = metricUsage{cpuNano: cpuNano, memKi: memKi}
			}
		}
	}

	// Parse nodes
	var k8sNodeList struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Capacity struct {
					CPU              string `json:"cpu"`
					Memory           string `json:"memory"`
					EphemeralStorage string `json:"ephemeral-storage"`
				} `json:"capacity"`
				Allocatable struct {
					CPU              string `json:"cpu"`
					Memory           string `json:"memory"`
					EphemeralStorage string `json:"ephemeral-storage"`
				} `json:"allocatable"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					Architecture            string `json:"architecture"`
					ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
					KernelVersion           string `json:"kernelVersion"`
					KubeletVersion          string `json:"kubeletVersion"`
					OSImage                 string `json:"osImage"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(rawNodes, &k8sNodeList); err != nil {
		return nil, fmt.Errorf("error decoding nodes JSON: %v", err)
	}

	var totalCores int
	var totalUsedCores float64
	var totalRAMBytes int64
	var totalUsedRAMBytes int64
	var readyNodes int

	for _, n := range k8sNodeList.Items {
		nodeStat := NodeStatus{
			Name:            n.Metadata.Name,
			Status:          "NotReady",
			JoinedAt:        n.Metadata.CreationTimestamp,
			Uptime:          formatDuration(time.Since(n.Metadata.CreationTimestamp)),
			OSImage:         n.Status.NodeInfo.OSImage,
			KernelVersion:   n.Status.NodeInfo.KernelVersion,
			KubeletVersion:  n.Status.NodeInfo.KubeletVersion,
			ContainerEngine: n.Status.NodeInfo.ContainerRuntimeVersion,
			Architecture:    n.Status.NodeInfo.Architecture,
			Conditions:      make(map[string]string),
		}

		if status.ClusterVersion == "" {
			status.ClusterVersion = n.Status.NodeInfo.KubeletVersion
		}

		// Roles from labels
		for l := range n.Metadata.Labels {
			if strings.HasPrefix(l, "node-role.kubernetes.io/") {
				role := strings.TrimPrefix(l, "node-role.kubernetes.io/")
				nodeStat.Roles = append(nodeStat.Roles, role)
			} else if strings.Contains(l, "controlplane") {
				nodeStat.Roles = append(nodeStat.Roles, "control-plane")
			}
		}
		if len(nodeStat.Roles) == 0 {
			nodeStat.Roles = []string{"worker"}
		}

		for _, c := range n.Status.Conditions {
			nodeStat.Conditions[c.Type] = c.Status
			if c.Type == "Ready" && c.Status == "True" {
				nodeStat.Status = "Ready"
				readyNodes++
			}
		}

		// Capacity & Allocatable
		cores, _ := strconv.Atoi(n.Status.Capacity.CPU)
		nodeStat.CPUCores = cores
		totalCores += cores

		memKiStr := strings.TrimSuffix(n.Status.Capacity.Memory, "Ki")
		memKi, _ := strconv.ParseInt(memKiStr, 10, 64)
		memBytes := memKi * 1024
		nodeStat.RAMTotalBytes = memBytes
		totalRAMBytes += memBytes

		ephKiStr := strings.TrimSuffix(n.Status.Capacity.EphemeralStorage, "Ki")
		ephKi, _ := strconv.ParseInt(ephKiStr, 10, 64)
		nodeStat.EphemeralBytes = ephKi * 1024

		// Live usage if available
		if m, ok := nodeMetrics[n.Metadata.Name]; ok {
			usedCores := float64(m.cpuNano) / 1e9
			nodeStat.CPUUsedCores = usedCores
			totalUsedCores += usedCores
			if cores > 0 {
				nodeStat.CPUUsagePercent = (usedCores / float64(cores)) * 100.0
			}

			usedBytes := m.memKi * 1024
			nodeStat.RAMUsedBytes = usedBytes
			totalUsedRAMBytes += usedBytes
			if memBytes > 0 {
				nodeStat.RAMUsagePercent = (float64(usedBytes) / float64(memBytes)) * 100.0
			}
		}

		status.Nodes = append(status.Nodes, nodeStat)
	}

	status.Summary.TotalNodes = len(status.Nodes)
	status.Summary.ReadyNodes = readyNodes
	status.Summary.TotalCores = totalCores
	status.Summary.UsedCores = totalUsedCores
	if totalCores > 0 {
		status.Summary.CPUUsagePercent = (totalUsedCores / float64(totalCores)) * 100.0
	}
	status.Summary.TotalRAMBytes = totalRAMBytes
	status.Summary.UsedRAMBytes = totalUsedRAMBytes
	if totalRAMBytes > 0 {
		status.Summary.RAMUsagePercent = (float64(totalUsedRAMBytes) / float64(totalRAMBytes)) * 100.0
	}

	// Zero-scale apps discovery
	status.ZeroScaleApps = c.collectZeroScaleApps()
	status.Summary.TotalZeroApps = len(status.ZeroScaleApps)
	for _, a := range status.ZeroScaleApps {
		if a.State == "active" {
			status.Summary.ActiveZeroApps++
		}
	}

	// Other site links discovered from Ingresses
	if ingressErr == nil && len(rawIngresses) > 0 {
		status.OtherSites = c.parseIngressSites(rawIngresses)
		totalIng := 0
		for _, g := range status.OtherSites {
			totalIng += len(g.Sites)
		}
		status.Summary.TotalIngresses = totalIng
	}

	// Populate CloudNativePG Operator & Cluster info
	status.Operators.Postgres = PostgresOperatorInfo{
		Name:      "CloudNativePG",
		Namespace: "cnpg-system",
		Status:    "Healthy",
	}
	if cnpgDeployErr == nil && len(rawCnpgDeploy) > 0 {
		var deployList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Replicas int `json:"replicas"`
				} `json:"spec"`
				Status struct {
					ReadyReplicas int `json:"readyReplicas"`
					Replicas      int `json:"replicas"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawCnpgDeploy, &deployList); err == nil {
			for _, item := range deployList.Items {
				if strings.Contains(item.Metadata.Name, "cnpg") || strings.Contains(item.Metadata.Name, "cloudnative-pg") {
					status.Operators.Postgres.OperatorReplicas = item.Spec.Replicas
					status.Operators.Postgres.OperatorReady = item.Status.ReadyReplicas
					break
				}
			}
		}
	}
	if cnpgClustersErr == nil && len(rawCnpgClusters) > 0 {
		var clusterList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Instances int `json:"instances"`
				} `json:"spec"`
				Status struct {
					Instances      int    `json:"instances"`
					ReadyInstances int    `json:"readyInstances"`
					Phase          string `json:"phase"`
					TargetPrimary  string `json:"targetPrimary"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawCnpgClusters, &clusterList); err == nil && len(clusterList.Items) > 0 {
			cItem := clusterList.Items[0]
			status.Operators.Postgres.ClusterName = cItem.Metadata.Name
			status.Operators.Postgres.Instances = cItem.Spec.Instances
			status.Operators.Postgres.ReadyInstances = cItem.Status.ReadyInstances
			status.Operators.Postgres.Primary = cItem.Status.TargetPrimary
			if cItem.Status.Phase != "" {
				status.Operators.Postgres.Status = cItem.Status.Phase
			}
		}
	}

	// Populate Longhorn Storage Operator info
	status.Operators.Longhorn = LonghornOperatorInfo{
		Name:         "Longhorn",
		Namespace:    "longhorn-system",
		StorageClass: "longhorn-2-replicas",
		Status:       "Healthy",
	}
	if longhornDsErr == nil && len(rawLonghornDs) > 0 {
		var dsList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					DesiredNumberScheduled int `json:"desiredNumberScheduled"`
					NumberReady            int `json:"numberReady"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawLonghornDs, &dsList); err == nil {
			for _, item := range dsList.Items {
				if item.Metadata.Name == "longhorn-manager" {
					status.Operators.Longhorn.ManagerDesired = item.Status.DesiredNumberScheduled
					status.Operators.Longhorn.ManagerReady = item.Status.NumberReady
					break
				}
			}
		}
	}
	if longhornDeployErr == nil && len(rawLonghornDeploy) > 0 {
		var deployList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Replicas int `json:"replicas"`
				} `json:"spec"`
				Status struct {
					ReadyReplicas int `json:"readyReplicas"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawLonghornDeploy, &deployList); err == nil {
			for _, item := range deployList.Items {
				if item.Metadata.Name == "longhorn-ui" {
					status.Operators.Longhorn.UIDesired = item.Spec.Replicas
					status.Operators.Longhorn.UIReady = item.Status.ReadyReplicas
					break
				}
			}
		}
	}
	if longhornVolsErr == nil && len(rawLonghornVols) > 0 {
		var volList struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(rawLonghornVols, &volList); err == nil {
			status.Operators.Longhorn.TotalVolumes = len(volList.Items)
		}
	}
	if status.Operators.Longhorn.ManagerReady < status.Operators.Longhorn.ManagerDesired && status.Operators.Longhorn.ManagerDesired > 0 {
		status.Operators.Longhorn.Status = "Degraded"
	}

	return status, nil
}

// GetClusterStatus returns cached or newly gathered cluster status, with RBAC-protected fields masked if unauthenticated.
func (c *Collector) GetClusterStatus(token string) (*ClusterStatus, error) {
	c.cacheMu.RLock()
	if c.cachedRaw != nil && time.Since(c.cacheTime) < c.cacheTTL {
		raw := c.cachedRaw
		c.cacheMu.RUnlock()
		return c.formatForAuth(raw, token), nil
	}
	c.cacheMu.RUnlock()

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cachedRaw != nil && time.Since(c.cacheTime) < c.cacheTTL {
		return c.formatForAuth(c.cachedRaw, token), nil
	}

	raw, err := c.Collect()
	if err != nil {
		if c.cachedRaw != nil {
			return c.formatForAuth(c.cachedRaw, token), nil
		}
		return nil, err
	}
	c.cachedRaw = raw
	c.cacheTime = time.Now()
	return c.formatForAuth(raw, token), nil
}

func (c *Collector) formatForAuth(raw *ClusterStatus, token string) *ClusterStatus {
	authed := false
	token = strings.TrimSpace(token)
	if token != "" {
		valid, _, err := runner.VerifyKubeToken(token, "default")
		if err == nil && valid {
			authed = true
		}
	}

	clone := *raw
	clone.Authenticated = authed

	if !authed {
		clone.ClusterVersion = "v1.35.x (🔒 RBAC Protected)"
		cloneNodes := make([]NodeStatus, len(raw.Nodes))
		for i, n := range raw.Nodes {
			nodeClone := n
			nodeClone.KernelVersion = "🔒 Protected (RBAC Required)"
			nodeClone.KubeletVersion = "🔒 Protected (RBAC Required)"
			cloneNodes[i] = nodeClone
		}
		clone.Nodes = cloneNodes
	}

	return &clone
}

func (c *Collector) collectZeroScaleApps() []ZeroScaleApp {
	apps := []ZeroScaleApp{
		{
			Host:        "bdash2.kube.fairfaxmedia.net",
			URL:         "https://bdash2.kube.fairfaxmedia.net/",
			Type:        "elf",
			LogsURL:     "https://bdash2.kube.fairfaxmedia.net/_logs",
			Description: "High-Performance CloudNativePG Builds & Parts Dashboard",
		},
		{
			Host:        "bdash2.fairfaxmedia.net",
			URL:         "https://bdash2.fairfaxmedia.net/",
			Type:        "elf",
			LogsURL:     "https://bdash2.fairfaxmedia.net/_logs",
			Description: "Public Builds & Parts Dashboard Custom Domain",
		},
		{
			Host:        "hopscotch.kube.fairfaxmedia.net",
			URL:         "https://hopscotch.kube.fairfaxmedia.net/",
			Type:        "elf",
			LogsURL:     "https://hopscotch.kube.fairfaxmedia.net/_logs",
			Description: "Hoppscotch API Development & Testing Suite",
		},
		{
			Host:        "status.kube.fairfaxmedia.net",
			URL:         "https://status.kube.fairfaxmedia.net/",
			Type:        "elf",
			LogsURL:     "https://status.kube.fairfaxmedia.net/_logs",
			Description: "Zero-Scale Platform Cluster & Nodes Dashboard",
		},
		{
			Host:        "kstat.fairfaxmedia.net",
			URL:         "https://kstat.fairfaxmedia.net/",
			Type:        "elf",
			LogsURL:     "https://kstat.fairfaxmedia.net/_logs",
			Description: "Zero-Scale Platform Status Dashboard (kstat alias)",
		},
	}

	// Check if active by querying systemctl if local or checking transient service units
	for i := range apps {
		unit := fmt.Sprintf("app-%s", apps[i].Host)
		cmd := exec.Command("systemctl", "is-active", unit)
		if err := cmd.Run(); err == nil {
			apps[i].State = "active"
			continue
		}
		transientFile := fmt.Sprintf("/run/systemd/transient/%s.service", unit)
		if _, err := os.Stat(transientFile); err == nil {
			apps[i].State = "active"
			continue
		}
		apps[i].State = "idle"
	}

	return apps
}

func (c *Collector) parseIngressSites(raw []byte) []SiteGroup {
	var ingList struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Rules []struct {
					Host string `json:"host"`
				} `json:"rules"`
			} `json:"spec"`
		} `json:"items"`
	}

	_ = json.Unmarshal(raw, &ingList)

	categories := map[string][]SiteLink{
		"Core & Infrastructure": {
			{
				Name:        "Git Server (Gitea)",
				Host:        "git.fairfaxmedia.net",
				URL:         "https://git.fairfaxmedia.net/",
				Namespace:   "default",
				Category:    "Core & Infrastructure",
				Icon:        "📂",
				Description: "Internal Git repository server hosting open-cream-cheese and zero-scale-platform",
			},
			{
				Name:        "Kubernetes Dashboard",
				Host:        "kube.fairfaxmedia.net",
				URL:         "https://kube.fairfaxmedia.net/",
				Namespace:   "kube-system",
				Category:    "Core & Infrastructure",
				Icon:        "☸️",
				Description: "Full cluster management UI, workloads, secrets, storage, and web shell",
			},
			{
				Name:        "Prometheus Monitoring",
				Host:        "prom-serv.kube.fairfaxmedia.net",
				URL:         "https://prom-serv.kube.fairfaxmedia.net/",
				Namespace:   "default",
				Category:    "Core & Infrastructure",
				Icon:        "📈",
				Description: "Cluster metrics server, alerts, targets, and time-series graphing",
			},
		},
		"Applications & Services": {},
		"Storage & Data":          {},
		"Media & Social":          {},
	}

	seenHosts := make(map[string]bool)
	seenHosts["git.fairfaxmedia.net"] = true
	seenHosts["kube.fairfaxmedia.net"] = true
	seenHosts["prom-serv.kube.fairfaxmedia.net"] = true
	seenHosts["bdash2.kube.fairfaxmedia.net"] = true
	seenHosts["bdash2.fairfaxmedia.net"] = true
	seenHosts["hopscotch.kube.fairfaxmedia.net"] = true
	seenHosts["status.kube.fairfaxmedia.net"] = true
	seenHosts["status.fairfaxmedia.net"] = true
	seenHosts["kstat.fairfaxmedia.net"] = true
	seenHosts["kstat.kube.fairfaxmedia.net"] = true

	for _, item := range ingList.Items {
		ns := item.Metadata.Namespace
		for _, r := range item.Spec.Rules {
			host := r.Host
			if seenHosts[host] || host == "" {
				continue
			}
			seenHosts[host] = true

			link := SiteLink{
				Name:      formatSiteName(host, item.Metadata.Name),
				Host:      host,
				URL:       "https://" + host + "/",
				Namespace: ns,
			}

			if strings.Contains(host, "minio") {
				link.Category = "Storage & Data"
				link.Icon = "🪣"
				link.Description = "High-Performance S3 Compatible Distributed Object Store"
				categories["Storage & Data"] = append(categories["Storage & Data"], link)
			} else if strings.Contains(host, "sync") {
				link.Category = "Storage & Data"
				link.Icon = "🔄"
				link.Description = "Continuous multi-device decentralized file synchronization"
				categories["Storage & Data"] = append(categories["Storage & Data"], link)
			} else if strings.Contains(host, "nextcloud") {
				link.Category = "Applications & Services"
				link.Icon = "☁️"
				link.Description = "Private cloud productivity, files, calendars, and team collaboration"
				categories["Applications & Services"] = append(categories["Applications & Services"], link)
			} else if strings.Contains(host, "odoo") {
				link.Category = "Applications & Services"
				link.Icon = "💼"
				link.Description = "Enterprise resource planning, CRM, accounting, and inventory"
				categories["Applications & Services"] = append(categories["Applications & Services"], link)
			} else if strings.Contains(host, "papi") {
				link.Category = "Applications & Services"
				link.Icon = "🔌"
				link.Description = "Production platform API services and integration gateway"
				categories["Applications & Services"] = append(categories["Applications & Services"], link)
			} else if strings.Contains(host, "social") {
				link.Category = "Media & Social"
				link.Icon = "🐘"
				link.Description = "Decentralized federated social network server"
				categories["Media & Social"] = append(categories["Media & Social"], link)
			} else if strings.Contains(host, "search") {
				link.Category = "Applications & Services"
				link.Icon = "🔍"
				link.Description = "Decentralized web search engine and distributed web indexer"
				categories["Applications & Services"] = append(categories["Applications & Services"], link)
			} else {
				link.Category = "Applications & Services"
				link.Icon = "🌐"
				link.Description = "Kubernetes Ingress deployed service"
				categories["Applications & Services"] = append(categories["Applications & Services"], link)
			}
		}
	}

	order := []string{"Core & Infrastructure", "Applications & Services", "Storage & Data", "Media & Social"}
	var result []SiteGroup
	for _, cat := range order {
		if len(categories[cat]) > 0 {
			result = append(result, SiteGroup{Category: cat, Sites: categories[cat]})
		}
	}
	return result
}

func formatSiteName(host, ingName string) string {
	parts := strings.Split(host, ".")
	if len(parts) > 0 {
		first := parts[0]
		switch first {
		case "minio-console":
			return "MinIO Object Storage Console"
		case "minio":
			return "MinIO S3 API"
		case "nextcloud":
			return "Nextcloud Cloud Hub"
		case "odoo":
			return "Odoo ERP"
		case "social":
			return "Mastodon Social"
		case "search":
			return "YaCy Search Lab"
		case "sync":
			return "Syncthing File Sync"
		case "papi":
			return "PAPI Production API"
		default:
			return strings.Title(strings.ReplaceAll(first, "-", " "))
		}
	}
	return ingName
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}
