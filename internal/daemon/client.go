// Package daemon provides an HTTP client for the local Sistemo daemon (agent API).
package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// DefaultURL is the default daemon base URL when SISTEMO_DAEMON_URL is not set.
const DefaultURL = "http://127.0.0.1:7777"

// URL returns the daemon base URL from env or default.
// Validates that SISTEMO_DAEMON_URL uses http/https scheme to prevent redirect to untrusted hosts.
func URL() string {
	if u := os.Getenv("SISTEMO_DAEMON_URL"); u != "" {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			fmt.Fprintf(os.Stderr, "Warning: SISTEMO_DAEMON_URL %q is invalid (must be http/https), using default\n", u)
			return DefaultURL
		}
		return u
	}
	return DefaultURL
}

// setAuthHeaders adds API key header if HOST_API_KEY is set.
func setAuthHeaders(req *http.Request) {
	if key := os.Getenv("HOST_API_KEY"); key != "" {
		req.Header.Set("X-API-Key", key)
	}
}

// doRequest is a helper that sets auth headers, executes the request, and checks for errors.
func doRequest(req *http.Request, timeout time.Duration) (*http.Response, error) {
	setAuthHeaders(req)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	return resp, nil
}

// checkResponse reads the response and returns an error if status is not OK (200).
func checkResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return nil
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
	resp, err := doRequest(httpReq, 600*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return nil, err
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
	resp, err := doRequest(httpReq, 30*time.Second)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := checkResponse(resp); err != nil {
		return false, err
	}
	return true, nil
}

// StopVM calls POST /vms/{vmID}/stop on the daemon.
func StopVM(baseURL, vmID string) (bool, error) {
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms/"+vmID+"/stop", nil)
	if err != nil {
		return false, err
	}
	resp, err := doRequest(httpReq, 30*time.Second)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := checkResponse(resp); err != nil {
		return false, err
	}
	return true, nil
}

// StartVM calls POST /vms/{vmID}/start on the daemon.
func StartVM(baseURL, vmID string) (*CreateVMResponse, error) {
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/vms/"+vmID+"/start", nil)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(httpReq, 120*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var out CreateVMResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// Health calls GET /health on the daemon.
func Health(baseURL string) error {
	httpReq, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := doRequest(httpReq, 5*time.Second)
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
	resp, err := doRequest(httpReq, 30*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}

// UnexposePort calls DELETE /vms/{vmID}/expose/{hostPort} on the daemon.
func UnexposePort(baseURL, vmID string, hostPort int) error {
	httpReq, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/vms/%s/expose/%d", baseURL, vmID, hostPort), nil)
	if err != nil {
		return err
	}
	resp, err := doRequest(httpReq, 30*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponse(resp)
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
	resp, err := doRequest(httpReq, 30*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}

// DeleteNetwork calls DELETE /networks/{name} on the daemon.
func DeleteNetwork(baseURL, name string) error {
	httpReq, err := http.NewRequest(http.MethodDelete, baseURL+"/networks/"+name, nil)
	if err != nil {
		return err
	}
	resp, err := doRequest(httpReq, 30*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return checkResponse(resp)
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
	resp, err := doRequest(httpReq, time.Duration(timeoutSec+5)*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var out ExecResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}
