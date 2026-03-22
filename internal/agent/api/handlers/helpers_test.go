package handlers

import "testing"

func TestIsValidSafeID_ValidIDs(t *testing.T) {
	valid := []string{
		"debian",
		"my-vm",
		"vm_1",
		"a2640ee4-64c3-4d70-a644-ad0cccaed86f", // UUID
		"node-1.0",
		"my.vm.name",
		"Web-Server_v2.1",
		"a",
	}
	for _, id := range valid {
		if !isValidSafeID(id) {
			t.Errorf("isValidSafeID(%q) = false, want true", id)
		}
	}
}

func TestIsValidSafeID_PathTraversal(t *testing.T) {
	attacks := []string{
		"../etc/passwd",
		"..",
		"..%2f",
		"a/../b",
		"foo..bar", // contains ..
	}
	for _, id := range attacks {
		if isValidSafeID(id) {
			t.Errorf("isValidSafeID(%q) = true, want false (path traversal)", id)
		}
	}
}

func TestIsValidSafeID_SpecialChars(t *testing.T) {
	bad := []string{
		"hello world",   // space
		"vm/name",       // slash
		"vm\\name",      // backslash
		"vm;name",       // semicolon
		"vm\x00name",    // null byte
		"vm\nname",      // newline
		"vm\tname",      // tab
		"vm`name",       // backtick
		"vm$name",       // dollar
		"vm|name",       // pipe
		"vm>name",       // redirect
		"vm<name",       // redirect
		"vm&name",       // ampersand
		"(vm)",          // parens
		"vm name",       // space
	}
	for _, id := range bad {
		if isValidSafeID(id) {
			t.Errorf("isValidSafeID(%q) = true, want false", id)
		}
	}
}

func TestIsValidSafeID_Empty(t *testing.T) {
	if isValidSafeID("") {
		t.Error("isValidSafeID(\"\") = true, want false")
	}

	// Too long (>256 chars)
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	if isValidSafeID(string(long)) {
		t.Error("isValidSafeID(257 chars) = true, want false")
	}

	// 256 chars should be ok
	ok := make([]byte, 256)
	for i := range ok {
		ok[i] = 'a'
	}
	if !isValidSafeID(string(ok)) {
		t.Error("isValidSafeID(256 chars) = false, want true")
	}
}

func TestIsValidSafeID_DotEdgeCases(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"node-1.0", true},
		{"my.vm", true},
		{"a.b.c", true},
		{".hidden", false},    // starts with dot
		{"a..b", false},       // contains ..
		{"test.", true},       // ends with dot (allowed, no traversal risk)
		{"1.2.3.4", true},    // IP-like (allowed)
	}
	for _, tt := range tests {
		got := isValidSafeID(tt.id)
		if got != tt.want {
			t.Errorf("isValidSafeID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
