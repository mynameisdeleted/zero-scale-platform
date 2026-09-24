package main

import "time"

type ClusterStatus struct {
	Timestamp      time.Time       `json:"timestamp"`
	ClusterVersion string          `json:"cluster_version"`
	Summary        ClusterSummary  `json:"summary"`
	Nodes          []NodeStatus    `json:"nodes"`
	ZeroScaleApps  []ZeroScaleApp  `json:"zero_scale_apps"`
	OtherSites     []SiteGroup     `json:"other_sites"`
	GitURL         string          `json:"git_url"`
}

type ClusterSummary struct {
	TotalNodes      int     `json:"total_nodes"`
	ReadyNodes      int     `json:"ready_nodes"`
	TotalCores      int     `json:"total_cores"`
	UsedCores       float64 `json:"used_cores"`
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
	TotalRAMBytes   int64   `json:"total_ram_bytes"`
	UsedRAMBytes    int64   `json:"used_ram_bytes"`
	RAMUsagePercent float64 `json:"ram_usage_percent"`
	TotalIngresses  int     `json:"total_ingresses"`
	TotalZeroApps   int     `json:"total_zero_apps"`
	ActiveZeroApps  int     `json:"active_zero_apps"`
}

type NodeStatus struct {
	Name            string            `json:"name"`
	Status          string            `json:"status"` // Ready, NotReady
	Roles           []string          `json:"roles"`
	Uptime          string            `json:"uptime"`
	JoinedAt        time.Time         `json:"joined_at"`
	OSImage         string            `json:"os_image"`
	KernelVersion   string            `json:"kernel_version"`
	KubeletVersion  string            `json:"kubelet_version"`
	ContainerEngine string            `json:"container_engine"`
	Architecture    string            `json:"architecture"`
	CPUCores        int               `json:"cpu_cores"`
	CPUUsedCores    float64           `json:"cpu_used_cores"`
	CPUUsagePercent float64           `json:"cpu_usage_percent"`
	RAMTotalBytes   int64             `json:"ram_total_bytes"`
	RAMUsedBytes    int64             `json:"ram_used_bytes"`
	RAMUsagePercent float64           `json:"ram_usage_percent"`
	EphemeralBytes  int64             `json:"ephemeral_bytes"`
	Conditions      map[string]string `json:"conditions"`
}

type ZeroScaleApp struct {
	Host        string `json:"host"`
	URL         string `json:"url"`
	Type        string `json:"type"`  // elf, wasm
	State       string `json:"state"` // active, idle
	Port        int    `json:"port,omitempty"`
	LogsURL     string `json:"logs_url"`
	Description string `json:"description,omitempty"`
}

type SiteLink struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	URL         string `json:"url"`
	Namespace   string `json:"namespace"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

type SiteGroup struct {
	Category string     `json:"category"`
	Sites    []SiteLink `json:"sites"`
}
