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

// checkResponse reads the response and returns an error if status is not 2xx.
func checkResponse(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("daemon returned %d: %s", resp.StatusCode, errBody.String())
	}
	return nil
}

// CreateMachineRequest is the JSON body for POST /machines.
type CreateMachineRequest struct {
	MachineID       string   `json:"machine_id,omitempty"`
	Name            string   `json:"name,omitempty"`
	Image           string   `json:"image"`
	VCPUs           int      `json:"vcpus"`
	MemoryMB        int      `json:"memory_mb"`
	StorageMB       int      `json:"storage_mb,omitempty"`
	RootVolume      string   `json:"root_volume,omitempty"`
	AttachedStorage []string `json:"attached_storage,omitempty"`
	InjectInitSSH   bool     `json:"inject_init_ssh,omitempty"`
	NetworkBridge   string   `json:"network_bridge,omitempty"`
	NetworkSubnet   string   `json:"network_subnet,omitempty"`
}

// CreateMachineResponse is the response from POST /machines.
type CreateMachineResponse struct {
	MachineID  string `json:"machine_id"`
	Status     string `json:"status"`
	IPAddress  string `json:"ip_address"`
	Namespace  string `json:"namespace,omitempty"`
	BootTimeMS int64  `json:"boot_time_ms"`
	SSHReady   bool   `json:"ssh_ready"`
}

// CreateMachine calls POST /machines on the daemon.
func CreateMachine(baseURL string, req *CreateMachineRequest) (*CreateMachineResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/machines", bytes.NewReader(body))
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
	var out CreateMachineResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// DeleteMachine calls DELETE /machines/{machineID} on the daemon.
func DeleteMachine(baseURL, machineID string, preserveStorage bool) (bool, error) {
	ps := "false"
	if preserveStorage {
		ps = "true"
	}
	httpReq, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/machines/"+machineID+"?preserve_storage="+ps, nil)
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

// StopMachine calls POST /machines/{machineID}/stop on the daemon.
func StopMachine(baseURL, machineID string) (bool, error) {
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/machines/"+machineID+"/stop", nil)
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

// StartMachine calls POST /machines/{machineID}/start on the daemon.
func StartMachine(baseURL, machineID string) (*CreateMachineResponse, error) {
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/machines/"+machineID+"/start", nil)
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
	var out CreateMachineResponse
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

// ExposePort calls POST /api/v1/machines/{machineID}/expose on the daemon.
func ExposePort(baseURL, machineID string, hostPort, machinePort int, protocol string) error {
	body := struct {
		HostPort    int    `json:"host_port"`
		MachinePort int    `json:"machine_port"`
		Protocol    string `json:"protocol"`
	}{HostPort: hostPort, MachinePort: machinePort, Protocol: protocol}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/machines/"+machineID+"/expose", bytes.NewReader(data))
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

// UnexposePort calls DELETE /machines/{machineID}/expose/{hostPort} on the daemon.
func UnexposePort(baseURL, machineID string, hostPort int) error {
	httpReq, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/v1/machines/%s/expose/%d", baseURL, machineID, hostPort), nil)
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
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/networks", bytes.NewReader(data))
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
	httpReq, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/networks/"+name, nil)
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

// ExecResult is the response from POST /machines/{id}/exec.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Exec calls POST /machines/{machineID}/exec with the given script.
func Exec(baseURL, machineID, script string, timeoutSec int) (*ExecResult, error) {
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
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/machines/"+machineID+"/exec", bytes.NewReader(data))
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

// VolumeResponse is the response from volume API endpoints.
type VolumeResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	SizeMB          int    `json:"size_mb"`
	Path            string `json:"path"`
	Status          string `json:"status"`
	Role            string `json:"role,omitempty"`
	MachineID       string `json:"machine_id,omitempty"`
	Created         string `json:"created,omitempty"`
	LastStateChange string `json:"last_state_change,omitempty"`
}

// CreateVolume calls POST /volumes on the daemon.
func CreateVolume(baseURL string, sizeMB int, name string) (*VolumeResponse, error) {
	body := struct {
		SizeMB int    `json:"size_mb"`
		Name   string `json:"name"`
	}{SizeMB: sizeMB, Name: name}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/volumes", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(httpReq, 60*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var out VolumeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// ListVolumes calls GET /volumes on the daemon.
func ListVolumes(baseURL string) ([]VolumeResponse, error) {
	httpReq, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/volumes", nil)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(httpReq, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var out []VolumeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// GetVolume calls GET /volumes/{idOrName} on the daemon.
func GetVolume(baseURL, idOrName string) (*VolumeResponse, error) {
	httpReq, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/volumes/"+idOrName, nil)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(httpReq, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var out VolumeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// DeleteVolume calls DELETE /volumes/{idOrName} on the daemon.
func DeleteVolume(baseURL, idOrName string) error {
	httpReq, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/volumes/"+idOrName, nil)
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

// ResizeVolume calls POST /volumes/{idOrName}/resize on the daemon.
func ResizeVolume(baseURL, idOrName string, sizeMB int) error {
	body := struct {
		SizeMB int `json:"size_mb"`
	}{SizeMB: sizeMB}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/volumes/"+idOrName+"/resize", bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(httpReq, 120*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}

// AttachVolume calls POST /machines/{machineID}/volume/attach on the daemon.
func AttachVolume(baseURL, machineID, volumeIDOrName string) error {
	body := struct {
		Volume string `json:"volume"`
	}{Volume: volumeIDOrName}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/machines/%s/volume/attach", baseURL, machineID), bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(httpReq, 10*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}

// DetachVolume calls POST /machines/{machineID}/volume/detach on the daemon.
func DetachVolume(baseURL, machineID, volumeIDOrName string) error {
	body := struct {
		Volume string `json:"volume"`
	}{Volume: volumeIDOrName}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/machines/%s/volume/detach", baseURL, machineID), bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(httpReq, 10*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}
