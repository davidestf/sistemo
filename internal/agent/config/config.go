// Package config holds the centralized configuration struct.
package config

import (
	"net"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Port   int    `envconfig:"PORT" default:"8080"`
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
		// Skip veth, docker, bridge interfaces
		name := iface.Name
		if strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
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
