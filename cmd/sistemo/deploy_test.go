package main

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestArchSuffix(t *testing.T) {
	got := archSuffix()
	switch runtime.GOARCH {
	case "arm64":
		if got != "-arm64" {
			t.Errorf("archSuffix() on arm64 = %q, want %q", got, "-arm64")
		}
	default:
		if got != "" {
			t.Errorf("archSuffix() on %s = %q, want empty string", runtime.GOARCH, got)
		}
	}
}

func TestRegistryURL_Default(t *testing.T) {
	// Ensure env is clean
	t.Setenv("SISTEMO_REGISTRY_URL", "")
	got := registryURL()
	if got != defaultRegistryURL {
		t.Errorf("registryURL() default = %q, want %q", got, defaultRegistryURL)
	}
}

func TestRegistryURL_Override(t *testing.T) {
	t.Setenv("SISTEMO_REGISTRY_URL", "https://custom.example.com/imgs")
	got := registryURL()
	if got != "https://custom.example.com/imgs/" {
		t.Errorf("registryURL() with env = %q, want trailing slash", got)
	}
}

func TestRegistryURL_OverrideWithTrailingSlash(t *testing.T) {
	t.Setenv("SISTEMO_REGISTRY_URL", "https://custom.example.com/imgs/")
	got := registryURL()
	if got != "https://custom.example.com/imgs/" {
		t.Errorf("registryURL() with trailing slash = %q", got)
	}
}

func TestFindLocalImage_Exists(t *testing.T) {
	dataDir := t.TempDir()
	imagesDir := filepath.Join(dataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, "debian.rootfs.ext4"), []byte("rootfs"), 0644)

	got := findLocalImage(dataDir, "debian")
	if got == "" {
		t.Fatal("findLocalImage should find debian.rootfs.ext4")
	}
	if filepath.Base(got) != "debian.rootfs.ext4" {
		t.Errorf("findLocalImage returned %q, expected to end in debian.rootfs.ext4", got)
	}
}

func TestFindLocalImage_NotFound(t *testing.T) {
	dataDir := t.TempDir()
	os.MkdirAll(filepath.Join(dataDir, "images"), 0755)

	got := findLocalImage(dataDir, "nonexistent")
	if got != "" {
		t.Errorf("findLocalImage for nonexistent = %q, want empty string", got)
	}
}

func TestFindLocalImage_Ext4Variant(t *testing.T) {
	dataDir := t.TempDir()
	imagesDir := filepath.Join(dataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, "custom.ext4"), []byte("rootfs"), 0644)

	got := findLocalImage(dataDir, "custom")
	if got == "" {
		t.Fatal("findLocalImage should find custom.ext4")
	}
	if filepath.Base(got) != "custom.ext4" {
		t.Errorf("findLocalImage returned %q, expected to end in custom.ext4", got)
	}
}

func TestFindLocalImage_DockerTag(t *testing.T) {
	dataDir := t.TempDir()
	imagesDir := filepath.Join(dataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, "myapp.rootfs.ext4"), []byte("rootfs"), 0644)

	got := findLocalImage(dataDir, "myapp:latest")
	if got == "" {
		t.Fatal("findLocalImage should find myapp.rootfs.ext4 for myapp:latest")
	}
	if filepath.Base(got) != "myapp.rootfs.ext4" {
		t.Errorf("findLocalImage returned %q, expected myapp.rootfs.ext4", got)
	}
}

func TestFindLocalImage_EmptyImagesDir(t *testing.T) {
	dataDir := t.TempDir()
	// Don't create images subdir at all

	got := findLocalImage(dataDir, "debian")
	if got != "" {
		t.Errorf("findLocalImage with no images dir = %q, want empty", got)
	}
}

func TestImageName_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"myapp:v1.2", "myapp"},
		{"nginx:1.25", "nginx"},
		{"http://host/images/custom.rootfs.ext4", "custom"},
		{"/custom.ext4", "custom"},
		{"bare-name", "bare-name"},
	}
	for _, tt := range tests {
		got := imageName(tt.input)
		if got != tt.want {
			t.Errorf("imageName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseSizeMB_EdgeCases(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{"  512  ", 512, false},      // whitespace trimmed
		{"0", 0, false},              // zero is valid
		{"10M", 10, false},           // single-char M suffix
		{"10240M", 10240, false},     // large MB value
		{"0G", 0, false},             // zero GB
		{"100GB", 102400, false},     // large GB
		{"1.5G", 0, true},           // float not supported
		{"-1", -1, false},            // Atoi accepts negative numbers
		{"G", 0, true},              // suffix only
		{"M", 0, true},              // suffix only
	}
	for _, tt := range tests {
		got, err := parseSizeMB(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSizeMB(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseSizeMB(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
