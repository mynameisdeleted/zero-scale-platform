package main

import "time"

type ClusterStatus struct {
	Timestamp      time.Time              `json:"timestamp"`
	ClusterVersion string                 `json:"cluster_version"`
	Authenticated  bool                   `json:"authenticated"`
	Summary        ClusterSummary         `json:"summary"`
	Efficiency     ArchitectureEfficiency `json:"efficiency"`
	StackTotal     ZeroScaleStackTotal    `json:"stack_total"`
	Operators      PlatformOperators      `json:"operators"`
	Nodes          []NodeStatus           `json:"nodes"`
	ZeroScaleApps  []ZeroScaleApp         `json:"zero_scale_apps"`
	OtherSites     []SiteGroup            `json:"other_sites"`
	GitURL         string                 `json:"git_url"`
}

type ArchitectureEfficiency struct {
	TraditionalBaselineMB        int64   `json:"traditional_baseline_mb"`
	CurrentActualMB              int64   `json:"current_actual_mb"`
	SavedMemoryMB                int64   `json:"saved_memory_mb"`
	SavedMemoryPercent           float64 `json:"saved_memory_percent"`
	TotalAppsCount               int     `json:"total_apps_count"`
	ActiveZeroApps               int     `json:"active_zero_apps"`
	IdleZeroApps                 int     `json:"idle_zero_apps"`
	DatabaseSavingsMB            int64   `json:"database_savings_mb"`
	DatabaseActualMB             int64   `json:"database_actual_mb"`
	DatabaseBaselineMB           int64   `json:"database_baseline_mb"`
	ServerlessSavingsMB          int64   `json:"serverless_savings_mb"`
	TotalZeroScaleFootprintBytes int64   `json:"total_zero_scale_footprint_bytes"`
	TotalZeroScaleFootprintMB    float64 `json:"total_zero_scale_footprint_mb"`
	ZeroScaleClusterPercent      float64 `json:"zero_scale_cluster_percent"`
	StorageBenefit               string  `json:"storage_benefit"`
}

type ZeroScaleStackTotal struct {
	ZeroScaleRAMBytes int64   `json:"zero_scale_ram_bytes"`
	ZeroScaleCPUCores float64 `json:"zero_scale_cpu_cores"`
	PostgresRAMBytes  int64   `json:"postgres_ram_bytes"`
	PostgresCPUCores  float64 `json:"postgres_cpu_cores"`
	LonghornRAMBytes  int64   `json:"longhorn_ram_bytes"`
	LonghornCPUCores  float64 `json:"longhorn_cpu_cores"`
	TotalRAMBytes     int64   `json:"total_ram_bytes"`
	TotalCPUCores     float64 `json:"total_cpu_cores"`
	ClusterRAMPercent float64 `json:"cluster_ram_percent"`
	ClusterCPUPercent float64 `json:"cluster_cpu_percent"`
}

type PlatformOperators struct {
	Postgres PostgresOperatorInfo `json:"postgres"`
	Longhorn LonghornOperatorInfo `json:"longhorn"`
}

type PostgresOperatorInfo struct {
	Name             string  `json:"name"`
	Namespace        string  `json:"namespace"`
	Status           string  `json:"status"`
	OperatorReplicas int     `json:"operator_replicas"`
	OperatorReady    int     `json:"operator_ready"`
	OperatorRAMBytes int64   `json:"operator_ram_bytes"`
	OperatorCPUCores float64 `json:"operator_cpu_cores"`
	ClusterName      string  `json:"cluster_name"`
	Instances        int     `json:"instances"`
	ReadyInstances   int     `json:"ready_instances"`
	Primary          string  `json:"primary"`
	DatabaseRAMBytes int64   `json:"database_ram_bytes"`
	DatabaseCPUCores float64 `json:"database_cpu_cores"`
	TotalRAMBytes    int64   `json:"total_ram_bytes"`
	TotalCPUCores    float64 `json:"total_cpu_cores"`
}

type LonghornOperatorInfo struct {
	Name            string  `json:"name"`
	Namespace       string  `json:"namespace"`
	Status          string  `json:"status"`
	ManagerDesired  int     `json:"manager_desired"`
	ManagerReady    int     `json:"manager_ready"`
	UIDesired       int     `json:"ui_desired"`
	UIReady         int     `json:"ui_ready"`
	StorageClass    string  `json:"storage_class"`
	TotalVolumes    int     `json:"total_volumes"`
	ManagerRAMBytes int64   `json:"manager_ram_bytes"`
	ManagerCPUCores float64 `json:"manager_cpu_cores"`
	TotalRAMBytes   int64   `json:"total_ram_bytes"`
	TotalCPUCores   float64 `json:"total_cpu_cores"`
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
	RAMTotalBytes         int64             `json:"ram_total_bytes"`
	RAMUsedBytes          int64             `json:"ram_used_bytes"`
	RAMUsagePercent       float64           `json:"ram_usage_percent"`
	ZeroScaleRAMBytes     int64             `json:"zero_scale_ram_bytes"`
	ZeroScaleRAMPercent   float64           `json:"zero_scale_ram_percent"`
	SupportingRAMBytes    int64             `json:"supporting_ram_bytes"`
	LegacyRAMBytes        int64             `json:"legacy_ram_bytes"`
	OtherWorkloadRAMBytes int64             `json:"other_workload_ram_bytes"`
	TopMemoryConsumers    []MemoryHog       `json:"top_memory_consumers"`
	ZeroScaleSupporting   []MemoryHog       `json:"zero_scale_supporting,omitempty"`
	LegacyMonoliths       []MemoryHog       `json:"legacy_monoliths,omitempty"`
	ZeroScaleServices     []ZeroScaleApp    `json:"zero_scale_services,omitempty"`
	EphemeralBytes        int64             `json:"ephemeral_bytes"`
	Conditions            map[string]string `json:"conditions"`
}

type MemoryHog struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	RAMBytes  int64  `json:"ram_bytes"`
	Display   string `json:"display"`
	Category  string `json:"category,omitempty"` // "supporting" or "legacy"
}

type ZeroScaleApp struct {
	Host        string  `json:"host"`
	URL         string  `json:"url"`
	Type        string  `json:"type"`  // elf, wasm
	State       string  `json:"state"` // active, idle
	Port        int     `json:"port,omitempty"`
	LogsURL     string  `json:"logs_url"`
	Description string  `json:"description,omitempty"`
	RAMBytes    int64   `json:"ram_bytes"`
	CPUCores    float64 `json:"cpu_cores"`
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
