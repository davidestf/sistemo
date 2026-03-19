// Package daemon provides an HTTP client for the local Sistemo daemon (agent API).
package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// DefaultURL is the default daemon base URL when SISTEMO_DAEMON_URL is not set.
const DefaultURL = "http://127.0.0.1:7777"

// URL returns the daemon base URL from env or default.
func URL() string {
	if u := os.Getenv("SISTEMO_DAEMON_URL"); u != "" {
		return u
	}
	return DefaultURL
}

// CreateVMRequest is the JSON body for POST /vms.
type CreateVMRequest struct {
	VMID            string   `json:"vm_id,omitempty"`
	Name            string   `json:"name,omitempty"`
	Image           string   `json:"image"`
	VCPUs           int      `json:"vcpus"`
	MemoryMB        int      `json:"memory_mb"`
	StorageMB       int      `json:"storage_mb,omitempty"`
	AttachedStorage []string `json:"attached_storage,omitempty"`
	InjectInitSSH   bool     `json:"inject_init_ssh,omitempty"`
	NetworkBridge   string   `json:"network_bridge,omitempty"`
	NetworkSubnet   string   `json:"network_subnet,omitempty"`
}

// CreateVMResponse is the response from POST /vms.
type CreateVMResponse struct {
	VMID       string `json:"vm_id"`
	Status     string `json:"status"`
	IPAddress  string `json:"ip_address"`
	Namespace  string `json:"namespace,omitempty"`
	BootTimeMS int64  `json:"boot_time_ms"`
	SSHReady   bool   `json:"ssh_ready"`
}

// CreateVM calls POST /vms on the daemon.
func CreateVM(baseURL string, req *CreateVMRequest) (*CreateVMResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Create can take a long time: download rootfs from URL, copy, boot, wait for SSH (templates may be hundreds of MB).
	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	var out CreateVMResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// DeleteVM calls DELETE /vms/{vmID} on the daemon.
func DeleteVM(baseURL, vmID string) (bool, error) {
	httpReq, err := http.NewRequest(http.MethodDelete, baseURL+"/vms/"+vmID+"?preserve_storage=false", nil)
	if err != nil {
		return false, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return false, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return true, nil
}

// StopVM calls POST /vms/{vmID}/stop on the daemon. VM is stopped but vmDir is kept.
func StopVM(baseURL, vmID string) (bool, error) {
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms/"+vmID+"/stop", nil)
	if err != nil {
		return false, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return false, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return true, nil
}

// StartVM calls POST /vms/{vmID}/start on the daemon. Returns create-like response with namespace and IP.
func StartVM(baseURL, vmID string) (*CreateVMResponse, error) {
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms/"+vmID+"/start", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	var out CreateVMResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// Health calls GET /health on the daemon.
func Health(baseURL string) error {
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %d", resp.StatusCode)
	}
	return nil
}

// ExposePort calls POST /vms/{vmID}/expose on the daemon.
func ExposePort(baseURL, vmID string, hostPort, vmPort int, protocol string) error {
	body := struct {
		HostPort int    `json:"host_port"`
		VMPort   int    `json:"vm_port"`
		Protocol string `json:"protocol"`
	}{HostPort: hostPort, VMPort: vmPort, Protocol: protocol}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms/"+vmID+"/expose", bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return nil
}

// UnexposePort calls DELETE /vms/{vmID}/expose/{hostPort} on the daemon.
func UnexposePort(baseURL, vmID string, hostPort int) error {
	httpReq, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/vms/%s/expose/%d", baseURL, vmID, hostPort), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return nil
}

// CreateNetwork calls POST /networks on the daemon.
func CreateNetwork(baseURL, name, subnet, bridgeName string) error {
	body := struct {
		Name       string `json:"name"`
		Subnet     string `json:"subnet"`
		BridgeName string `json:"bridge_name"`
	}{Name: name, Subnet: subnet, BridgeName: bridgeName}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/networks", bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return nil
}

// DeleteNetwork calls DELETE /networks/{name} on the daemon.
func DeleteNetwork(baseURL, name string) error {
	httpReq, err := http.NewRequest(http.MethodDelete, baseURL+"/networks/"+name, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return nil
}

// ExecResult is the response from POST /vms/{id}/exec.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Exec calls POST /vms/{vmID}/exec with the given script.
func Exec(baseURL, vmID, script string, timeoutSec int) (*ExecResult, error) {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	body := struct {
		Script     string `json:"script"`
		TimeoutSec int    `json:"timeout_sec"`
	}{Script: script, TimeoutSec: timeoutSec}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms/"+vmID+"/exec", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(timeoutSec+5) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	var out ExecResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}
