package config

import "testing"

func TestValidate_Defaults(t *testing.T) {
	cfg := &Config{
		Port:         7777,
		MaxVCPUs:     64,
		MaxMemoryMB:  262144,
		MaxStorageMB: 1048576,
		BridgeSubnet: "10.200.0.0/16",
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestValidate_BadPort(t *testing.T) {
	cfg := &Config{
		Port:         0,
		MaxVCPUs:     4,
		MaxMemoryMB:  1024,
		MaxStorageMB: 1024,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("port 0 should be invalid")
	}

	cfg.Port = 70000
	if err := Validate(cfg); err == nil {
		t.Fatal("port 70000 should be invalid")
	}
}

func TestValidate_BadVCPUs(t *testing.T) {
	cfg := &Config{
		Port:         7777,
		MaxVCPUs:     -1,
		MaxMemoryMB:  1024,
		MaxStorageMB: 1024,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("negative vcpus should be invalid")
	}
}

func TestValidate_BadSubnet(t *testing.T) {
	cfg := &Config{
		Port:         7777,
		MaxVCPUs:     4,
		MaxMemoryMB:  1024,
		MaxStorageMB: 1024,
		BridgeSubnet: "not-a-cidr",
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("invalid CIDR should be rejected")
	}
}

func TestValidate_SubnetTooSmall(t *testing.T) {
	cfg := &Config{
		Port:         7777,
		MaxVCPUs:     4,
		MaxMemoryMB:  1024,
		MaxStorageMB: 1024,
		BridgeSubnet: "10.200.0.0/28",
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("/28 subnet should be too small")
	}
}
