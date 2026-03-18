// Package vm provides VM lifecycle management.
package vm

// CreateRequest is the API payload for creating a VM.
type CreateRequest struct {
	VMID            string            `json:"vm_id"`
	Image           string            `json:"image"`
	VCPUs           int               `json:"vcpus"`
	MemoryMB        int               `json:"memory_mb"`
	StorageMB       int               `json:"storage_mb,omitempty"`
	AttachedStorage []string          `json:"attached_storage,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	BandwidthMbps   int               `json:"bandwidth_mbps,omitempty"`
	UploadMbps      int               `json:"upload_mbps,omitempty"`
	DiskIOPS        int               `json:"disk_iops,omitempty"`
	DiskBWMbps      int               `json:"disk_bw_mbps,omitempty"`
	// InjectInitSSH: inject /init and SSH key into rootfs so terminal/exec work.
	InjectInitSSH bool `json:"inject_init_ssh,omitempty"`
}

// CreateResponse is returned after VM creation.
type CreateResponse struct {
	VMID       string  `json:"vm_id"`
	Status     string  `json:"status"`
	IPAddress  string  `json:"ip_address"`
	BootMethod string  `json:"boot_method"`
	BootTimeMS int64   `json:"boot_time_ms"`
	SSHReady   bool    `json:"ssh_ready"`
	ImageSHA   *string `json:"image_sha,omitempty"`
	Message    *string `json:"message,omitempty"`
	Namespace  string  `json:"namespace,omitempty"`
}

// VMInfo holds runtime information about a VM.
type VMInfo struct {
	VMID      string `json:"vm_id"`
	PID       int    `json:"pid"`
	Namespace string `json:"namespace"`
	IP        string `json:"ip"`
	Status    string `json:"status"`
}

// ExecResult holds the result of command execution on a VM.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// IPResult holds IP discovery results.
type IPResult struct {
	VMID          string  `json:"vm_id"`
	IP            *string `json:"ip,omitempty"`
	Namespace     *string `json:"namespace,omitempty"`
	DiscoveredVia *string `json:"discovered_via,omitempty"`
}

// DeleteResponse is returned after VM deletion.
type DeleteResponse struct {
	VMID       string `json:"vm_id"`
	Terminated bool   `json:"terminated"`
}
