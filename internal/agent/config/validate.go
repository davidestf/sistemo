package config

import (
	"fmt"
	"net"
	"runtime"
)

// Validate checks all config values and returns the first error found.
// Called at daemon startup after loading config. Fatal errors should be
// caught here before anything is created.
func Validate(cfg *Config) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("PORT must be 1-65535 (got %d)", cfg.Port)
	}

	if cfg.MaxVCPUs < 1 {
		return fmt.Errorf("MAX_VCPUS must be > 0 (got %d)", cfg.MaxVCPUs)
	}
	hostCPUs := runtime.NumCPU()
	if cfg.MaxVCPUs > hostCPUs*4 {
		// Warning, not error — user might be overcommitting intentionally
		fmt.Printf("Warning: MAX_VCPUS=%d exceeds 4x host CPUs (%d). This may cause performance issues.\n", cfg.MaxVCPUs, hostCPUs)
	}

	if cfg.MaxMemoryMB < 1 {
		return fmt.Errorf("MAX_MEMORY_MB must be > 0 (got %d)", cfg.MaxMemoryMB)
	}

	if cfg.MaxStorageMB < 1 {
		return fmt.Errorf("MAX_STORAGE_MB must be > 0 (got %d)", cfg.MaxStorageMB)
	}

	if cfg.MinDiskFreeMB < 0 {
		return fmt.Errorf("MIN_DISK_FREE_MB must be >= 0 (got %d)", cfg.MinDiskFreeMB)
	}

	if cfg.BridgeSubnet != "" {
		_, ipNet, err := net.ParseCIDR(cfg.BridgeSubnet)
		if err != nil {
			return fmt.Errorf("BRIDGE_SUBNET %q is not valid CIDR: %w", cfg.BridgeSubnet, err)
		}
		ones, _ := ipNet.Mask.Size()
		if ones > 24 {
			return fmt.Errorf("BRIDGE_SUBNET %q is too small (must be /24 or larger, got /%d)", cfg.BridgeSubnet, ones)
		}
	}

	// Validate HOST_INTERFACE if set (auto-detected values are already verified to exist)
	if cfg.HostInterface != "" {
		iface, err := net.InterfaceByName(cfg.HostInterface)
		if err != nil {
			return fmt.Errorf("HOST_INTERFACE %q does not exist on this system", cfg.HostInterface)
		}
		if iface.Flags&net.FlagUp == 0 {
			fmt.Printf("Warning: HOST_INTERFACE %q exists but is not UP\n", cfg.HostInterface)
		}
	}

	return nil
}
