// Package config holds the centralized configuration struct.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Port   int    `envconfig:"PORT" default:"7777"`
	APIKey string `envconfig:"HOST_API_KEY"`

	// Directories (overridden by daemon startup to ~/.sistemo/*)
	VMBaseDir     string `envconfig:"VM_BASE_DIR"`
	ImageCacheDir string `envconfig:"IMAGE_CACHE_DIR"`

	// Firecracker
	FirecrackerBin   string `envconfig:"FIRECRACKER_BINARY_PATH"`
	KernelImagePath  string `envconfig:"KERNEL_IMAGE_PATH"`
	KernelInitrdPath string `envconfig:"KERNEL_INITRD_PATH"`

	// SSH
	SSHKeyPath    string `envconfig:"SSH_KEY_PATH"`
	SSHUser       string `envconfig:"SSH_USER" default:"root"`
	HostInterface string `envconfig:"HOST_INTERFACE" default:"eth0"`

	// Limits (self-hosted: your machine, generous defaults)
	MaxVCPUs      int   `envconfig:"MAX_VCPUS" default:"64"`
	MaxMemoryMB   int   `envconfig:"MAX_MEMORY_MB" default:"262144"` // 256 GB
	MaxStorageMB  int   `envconfig:"MAX_STORAGE_MB" default:"1048576"` // 1 TB
	MinDiskFreeMB int64 `envconfig:"MIN_DISK_FREE_MB" default:"512"`

	// Network rate limiting (0 = no limit; set to cap VM bandwidth if needed)
	DefaultBandwidthMbps int `envconfig:"DEFAULT_BANDWIDTH_MBPS" default:"0"`
	DefaultUploadMbps    int `envconfig:"DEFAULT_UPLOAD_MBPS" default:"0"`

	// Disk I/O rate limiting (0 = no limit; set to cap VM disk I/O if needed)
	DefaultIOPS       int `envconfig:"DEFAULT_IOPS" default:"0"`
	DefaultDiskBWMbps int `envconfig:"DEFAULT_DISK_BW_MBPS" default:"0"`

	// Reconciler interval in seconds (how often to check for dead VM processes).
	// Default: 30 seconds. Set higher to reduce overhead on large fleets.
	ReconcilerIntervalSec int `envconfig:"RECONCILER_INTERVAL_SEC" default:"30"`

	// SSH readiness timeout in seconds (how long to wait for SSH after boot).
	// Default: 20 seconds. Increase if VMs take longer to boot.
	SSHTimeoutSec int `envconfig:"SSH_TIMEOUT_SEC" default:"20"`

	// Block outbound SMTP from VMs (ports 25, 465, 587) to prevent spam.
	// Default true. Set to false only if your VMs need to send email directly.
	BlockSMTP bool `envconfig:"BLOCK_SMTP" default:"true"`

	// Bridge subnet for VM networking (default: 10.200.0.0/16)
	// Change if it conflicts with your VPN, Kubernetes, or other networks.
	BridgeSubnet string `envconfig:"BRIDGE_SUBNET" default:"10.200.0.0/16"`

	// API rate limiting per IP (0 = disabled)
	RateLimitRPS   int `envconfig:"RATE_LIMIT_RPS" default:"100"`
	RateLimitBurst int `envconfig:"RATE_LIMIT_BURST" default:"200"`

	// Image build timeout in minutes. Default: 60 minutes.
	// Large Docker images may need more time. Set to 0 for no timeout.
	BuildTimeoutMin int `envconfig:"BUILD_TIMEOUT_MIN" default:"60"`

	// Dashboard session timeout in hours. Default: 8 hours.
	SessionTimeoutHours int `envconfig:"SESSION_TIMEOUT_HOURS" default:"8"`

	// DataDir is the root data directory (~/.sistemo). Set by the daemon at startup.
	DataDir string `envconfig:"-"`
}

// yamlConfig mirrors Config fields for YAML parsing. Only non-zero values override.
type yamlConfig struct {
	Port                 *int    `yaml:"port"`
	HostInterface        *string `yaml:"host_interface"`
	MaxVCPUs             *int    `yaml:"max_vcpus"`
	MaxMemoryMB          *int    `yaml:"max_memory_mb"`
	MaxStorageMB         *int    `yaml:"max_storage_mb"`
	MinDiskFreeMB        *int64  `yaml:"min_disk_free_mb"`
	DefaultBandwidthMbps *int    `yaml:"default_bandwidth_mbps"`
	DefaultUploadMbps    *int    `yaml:"default_upload_mbps"`
	DefaultIOPS          *int    `yaml:"default_iops"`
	DefaultDiskBWMbps    *int    `yaml:"default_disk_bw_mbps"`
	ReconcilerIntervalSec *int  `yaml:"reconciler_interval_sec"`
	SSHTimeoutSec        *int    `yaml:"ssh_timeout_sec"`
	BlockSMTP            *bool   `yaml:"block_smtp"`
	BridgeSubnet         *string `yaml:"bridge_subnet"`
	RateLimitRPS         *int    `yaml:"rate_limit_rps"`
	RateLimitBurst       *int    `yaml:"rate_limit_burst"`
}

// LoadFromFile reads a YAML config file and applies non-zero values to cfg.
// Should be called BEFORE envconfig.Process so env vars take final precedence.
func LoadFromFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var yc yamlConfig
	if err := yaml.Unmarshal(data, &yc); err != nil {
		return err
	}
	if yc.Port != nil {
		cfg.Port = *yc.Port
	}
	if yc.HostInterface != nil {
		cfg.HostInterface = *yc.HostInterface
	}
	if yc.MaxVCPUs != nil {
		cfg.MaxVCPUs = *yc.MaxVCPUs
	}
	if yc.MaxMemoryMB != nil {
		cfg.MaxMemoryMB = *yc.MaxMemoryMB
	}
	if yc.MaxStorageMB != nil {
		cfg.MaxStorageMB = *yc.MaxStorageMB
	}
	if yc.MinDiskFreeMB != nil {
		cfg.MinDiskFreeMB = *yc.MinDiskFreeMB
	}
	if yc.DefaultBandwidthMbps != nil {
		cfg.DefaultBandwidthMbps = *yc.DefaultBandwidthMbps
	}
	if yc.DefaultUploadMbps != nil {
		cfg.DefaultUploadMbps = *yc.DefaultUploadMbps
	}
	if yc.DefaultIOPS != nil {
		cfg.DefaultIOPS = *yc.DefaultIOPS
	}
	if yc.DefaultDiskBWMbps != nil {
		cfg.DefaultDiskBWMbps = *yc.DefaultDiskBWMbps
	}
	if yc.ReconcilerIntervalSec != nil {
		cfg.ReconcilerIntervalSec = *yc.ReconcilerIntervalSec
	}
	if yc.SSHTimeoutSec != nil {
		cfg.SSHTimeoutSec = *yc.SSHTimeoutSec
	}
	if yc.BlockSMTP != nil {
		cfg.BlockSMTP = *yc.BlockSMTP
	}
	if yc.BridgeSubnet != nil {
		cfg.BridgeSubnet = *yc.BridgeSubnet
	}
	if yc.RateLimitRPS != nil {
		cfg.RateLimitRPS = *yc.RateLimitRPS
	}
	if yc.RateLimitBurst != nil {
		cfg.RateLimitBurst = *yc.RateLimitBurst
	}
	return nil
}

func Load() (*Config, error) {
	var cfg Config
	err := envconfig.Process("", &cfg)
	if err != nil {
		return &cfg, err
	}
	// Auto-detect host interface if still default "eth0" and eth0 doesn't exist
	if cfg.HostInterface == "eth0" {
		if detected := detectDefaultInterface(); detected != "" {
			cfg.HostInterface = detected
		}
	}
	return &cfg, nil
}

// LoadWithFile loads config with priority: env vars > YAML file > struct defaults.
func LoadWithFile(configPath string) (*Config, error) {
	// 1. Start with envconfig defaults + any env vars
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	// 2. Apply YAML file on top (overrides envconfig defaults, but not explicit env vars)
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			if err := LoadFromFile(configPath, &cfg); err != nil {
				return nil, err
			}
		}
	}
	// 3. Re-apply explicit env vars so they take final precedence over YAML
	applyExplicitEnvOverrides(&cfg)

	if cfg.HostInterface == "eth0" {
		if detected := detectDefaultInterface(); detected != "" {
			cfg.HostInterface = detected
		}
	}
	return &cfg, nil
}

// applyExplicitEnvOverrides re-applies env vars that are explicitly set (not defaults).
// This ensures explicitly-set env vars take precedence over YAML file values.
func applyExplicitEnvOverrides(cfg *Config) {
	if v := os.Getenv("PORT"); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("HOST_INTERFACE"); v != "" {
		cfg.HostInterface = v
	}
	if v := os.Getenv("HOST_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("BLOCK_SMTP"); v != "" {
		cfg.BlockSMTP = v == "true" || v == "1"
	}
	if v := os.Getenv("BRIDGE_SUBNET"); v != "" {
		cfg.BridgeSubnet = v
	}
	applyIntEnvOverride("MAX_VCPUS", &cfg.MaxVCPUs)
	applyIntEnvOverride("MAX_MEMORY_MB", &cfg.MaxMemoryMB)
	applyIntEnvOverride("MAX_STORAGE_MB", &cfg.MaxStorageMB)
	applyIntEnvOverride("DEFAULT_BANDWIDTH_MBPS", &cfg.DefaultBandwidthMbps)
	applyIntEnvOverride("DEFAULT_UPLOAD_MBPS", &cfg.DefaultUploadMbps)
	applyIntEnvOverride("DEFAULT_IOPS", &cfg.DefaultIOPS)
	applyIntEnvOverride("DEFAULT_DISK_BW_MBPS", &cfg.DefaultDiskBWMbps)
	applyIntEnvOverride("RECONCILER_INTERVAL_SEC", &cfg.ReconcilerIntervalSec)
	applyIntEnvOverride("SSH_TIMEOUT_SEC", &cfg.SSHTimeoutSec)
	applyIntEnvOverride("RATE_LIMIT_RPS", &cfg.RateLimitRPS)
	applyIntEnvOverride("RATE_LIMIT_BURST", &cfg.RateLimitBurst)
	if v := os.Getenv("MIN_DISK_FREE_MB"); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			cfg.MinDiskFreeMB = n
		}
	}
}

func applyIntEnvOverride(envKey string, target *int) {
	if v := os.Getenv(envKey); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			*target = n
		}
	}
}

// detectDefaultInterface returns the network interface used for the default route.
// Falls back to the first non-loopback interface with an IPv4 address.
func detectDefaultInterface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		// Skip veth, docker, bridge, sistemo interfaces
		name := iface.Name
		if strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") ||
			strings.HasPrefix(name, "sistemo") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				return name
			}
		}
	}
	return ""
}
