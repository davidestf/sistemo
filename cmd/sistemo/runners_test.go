package main

import (
	"testing"
)

func TestParseSizeMB(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{"1024", 1024, false},
		{"2G", 2048, false},
		{"2GB", 2048, false},
		{"512M", 512, false},
		{"512MB", 512, false},
		{"1g", 1024, false},
		{"", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSizeMB(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSizeMB(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseSizeMB(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestImageName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"nginx:latest", "nginx"},
		{"debian", "debian"},
		{"/path/to/rootfs.ext4", "rootfs"},
		{"/path/to/debian.rootfs.ext4", "debian"},
		{"https://example.com/images/debian.rootfs.ext4", "debian"},
		{"", "vm"},
	}
	for _, tt := range tests {
		got := imageName(tt.input)
		if got != tt.want {
			t.Errorf("imageName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRegistryURL(t *testing.T) {
	// Default
	got := registryURL()
	if got != defaultRegistryURL {
		t.Errorf("registryURL() default = %q, want %q", got, defaultRegistryURL)
	}

	// Override
	t.Setenv("SISTEMO_REGISTRY_URL", "https://custom.example.com/imgs")
	got = registryURL()
	if got != "https://custom.example.com/imgs/" {
		t.Errorf("registryURL() with env = %q, want trailing slash", got)
	}

	// Override with trailing slash
	t.Setenv("SISTEMO_REGISTRY_URL", "https://custom.example.com/imgs/")
	got = registryURL()
	if got != "https://custom.example.com/imgs/" {
		t.Errorf("registryURL() with trailing slash = %q", got)
	}
}
