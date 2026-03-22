package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateImageURL_PublicIP(t *testing.T) {
	// These should pass (public IPs)
	urls := []string{
		"https://example.com/image.ext4",
		"https://get.sistemo.io/images/debian.ext4",
		"http://releases.ubuntu.com/22.04/image.ext4",
	}
	for _, u := range urls {
		if err := validateImageURL(u); err != nil {
			t.Errorf("validateImageURL(%q) = %v, want nil", u, err)
		}
	}
}

func TestValidateImageURL_PrivateIP(t *testing.T) {
	// These should be blocked (private/loopback)
	urls := []string{
		"http://10.0.0.1/image.ext4",
		"http://192.168.1.1/image.ext4",
		"http://172.16.0.1/image.ext4",
		"http://127.0.0.1/image.ext4",
		"http://[::1]/image.ext4",
	}
	for _, u := range urls {
		if err := validateImageURL(u); err == nil {
			t.Errorf("validateImageURL(%q) = nil, want error (private IP)", u)
		}
	}
}

func TestValidateImageURL_NonHTTP(t *testing.T) {
	urls := []string{
		"ftp://example.com/image.ext4",
		"file:///etc/passwd",
		"ssh://host/image",
		"javascript:alert(1)",
	}
	for _, u := range urls {
		if err := validateImageURL(u); err == nil {
			t.Errorf("validateImageURL(%q) = nil, want error (non-HTTP)", u)
		}
	}
}

func TestValidateImageURL_InvalidURL(t *testing.T) {
	if err := validateImageURL("not a url"); err == nil {
		t.Error("validateImageURL(\"not a url\") = nil, want error")
	}
}

func TestVerifyExt4Superblock_Valid(t *testing.T) {
	// Create a file with valid ext4 magic at offset 1080
	tmp := filepath.Join(t.TempDir(), "test.ext4")
	data := make([]byte, 2048)
	data[1080] = 0x53 // ext4 magic
	data[1081] = 0xEF
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := verifyExt4Superblock(tmp); err != nil {
		t.Errorf("verifyExt4Superblock(valid) = %v, want nil", err)
	}
}

func TestVerifyExt4Superblock_Invalid(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.ext4")
	data := make([]byte, 2048) // all zeros — wrong magic
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := verifyExt4Superblock(tmp); err == nil {
		t.Error("verifyExt4Superblock(invalid) = nil, want error")
	}
}

func TestVerifyExt4Superblock_TooSmall(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.ext4")
	data := make([]byte, 100) // too small for superblock
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := verifyExt4Superblock(tmp); err == nil {
		t.Error("verifyExt4Superblock(too small) = nil, want error")
	}
}

func TestVerifyExt4Superblock_Missing(t *testing.T) {
	if err := verifyExt4Superblock("/nonexistent/file"); err == nil {
		t.Error("verifyExt4Superblock(missing) = nil, want error")
	}
}
