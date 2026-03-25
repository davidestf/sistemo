package vm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	agent "github.com/davidestf/sistemo/internal/agent"
	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/firecracker"
	"github.com/davidestf/sistemo/internal/agent/network"
	"go.uber.org/zap"
)

// reqLog returns a logger tagged with request_id (if in context) and vm_id.
func reqLog(ctx context.Context, fallback *zap.Logger, vmID string) *zap.Logger {
	l := agentmw.LoggerFromCtx(ctx, fallback)
	return l.With(zap.String("vm_id", vmID))
}

func createVM(ctx context.Context, m *Manager, req *CreateRequest) (*CreateResponse, error) {
	startTime := time.Now()

	if req.VMID == "" || len(req.VMID) > 64 || strings.ContainsAny(req.VMID, " /\\\n\t\r") {
		return nil, fmt.Errorf("invalid vm id")
	}
	maxVCPUs := m.cfg.MaxVCPUs
	if maxVCPUs <= 0 {
		maxVCPUs = 64
	}
	maxMemoryMB := m.cfg.MaxMemoryMB
	if maxMemoryMB <= 0 {
		maxMemoryMB = 262144
	}
	if req.VCPUs <= 0 || req.VCPUs > maxVCPUs {
		return nil, fmt.Errorf("invalid vcpu count (max %d)", maxVCPUs)
	}
	if req.MemoryMB < 128 || req.MemoryMB > maxMemoryMB {
		return nil, fmt.Errorf("invalid memory size (max %d MB)", maxMemoryMB)
	}
	maxStorageMB := m.cfg.MaxStorageMB
	if maxStorageMB <= 0 {
		maxStorageMB = 102400
	}
	if req.StorageMB > 0 && req.StorageMB > maxStorageMB {
		return nil, fmt.Errorf("storage_mb exceeds max %d MB", maxStorageMB)
	}

	return createFresh(ctx, m, req, startTime)
}

func createFresh(ctx context.Context, m *Manager, req *CreateRequest, startTime time.Time) (*CreateResponse, error) {
	log := reqLog(ctx, m.logger, req.VMID)
	log.Info("create_request", zap.Int("vcpus", req.VCPUs), zap.Int("memory_mb", req.MemoryMB), zap.Int("storage_mb", req.StorageMB))

	// Allocate a unique IP from the appropriate subnet
	var vmIP string
	var allocErr error
	if req.NetworkSubnet != "" {
		vmIP, allocErr = network.AllocateIPInSubnet(m.db, req.VMID, req.NetworkSubnet)
	} else {
		vmIP, allocErr = network.AllocateIP(m.db, req.VMID)
	}
	if allocErr != nil {
		return nil, fmt.Errorf("allocate IP: %v", allocErr)
	}

	t := time.Now()
	net := network.NewVMNetwork(req.VMID, vmIP, m.logger, req.NetworkBridge)
	net.BlockSMTP = m.cfg.BlockSMTP
	if err := net.Create(); err != nil {
		network.ReleaseIP(m.db, req.VMID)
		return nil, fmt.Errorf("failed to create network namespace: %v", err)
	}
	log.Info("phase: network_setup", zap.Duration("elapsed", time.Since(t)), zap.String("vm_ip", vmIP))

	var sha *string
	kernelPath := m.cfg.KernelImagePath
	initrdPath := m.cfg.KernelInitrdPath

	// cleanup releases network and IP on error
	cleanup := func() {
		net.Cleanup(m.cfg.HostInterface)
		network.ReleaseIP(m.db, req.VMID)
	}

	// Pre-flight disk space check
	if err := checkDiskSpace(m.cfg.VMBaseDir, m.cfg.MinDiskFreeMB); err != nil {
		cleanup()
		return nil, err
	}

	vmDir := filepath.Join(m.cfg.VMBaseDir, req.VMID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to create VM directory: %v", err)
	}

	// Determine rootfs destination: use root volume path if provided, else vmDir/rootfs.ext4
	vmRootfs := filepath.Join(vmDir, "rootfs.ext4")
	if req.RootVolumePath != "" {
		vmRootfs = req.RootVolumePath
	}

	if req.UseExistingVolume {
		// Booting from existing volume — skip image copy and resize.
		// The volume already has data from a previous deployment.
		log.Info("phase: using_existing_volume", zap.String("path", vmRootfs))
	} else {
		// Fresh deployment — resolve image, copy, resize
		rootfs := req.Image

		// Download from HTTP(S) URL if needed
		if hasHTTPPrefix(rootfs) {
			dest := cachePathForURL(m.cfg.ImageCacheDir, rootfs)
			p, s, err := downloadFromURL(rootfs, dest)
			if err != nil {
				os.RemoveAll(vmDir)
				cleanup()
				return nil, err
			}
			rootfs = p
			sha = s
		}

		// Otherwise require an existing local path
		if !filepath.IsAbs(rootfs) && !hasHTTPPrefix(rootfs) {
			if _, err := os.Stat(rootfs); err != nil {
				os.RemoveAll(vmDir)
				cleanup()
				return nil, fmt.Errorf("image %q is not available: pass an HTTP(S) URL to a rootfs.ext4 or an absolute path to a rootfs.ext4 file", req.Image)
			}
		}

		t = time.Now()
		if err := copyFile(rootfs, vmRootfs); err != nil {
			os.RemoveAll(vmDir)
			cleanup()
			return nil, fmt.Errorf("failed to copy rootfs: %v", err)
		}
		log.Info("phase: copy_rootfs", zap.Duration("elapsed", time.Since(t)))

		if req.StorageMB > 0 {
			t = time.Now()
			if err := resizeRootfsTo(vmRootfs, req.StorageMB, log); err != nil {
				os.RemoveAll(vmDir)
				cleanup()
				return nil, fmt.Errorf("resize rootfs to %d MB: %w", req.StorageMB, err)
			}
			log.Info("phase: resize_rootfs", zap.Duration("elapsed", time.Since(t)), zap.Int("storage_mb", req.StorageMB))
		}
	}

	// Always inject /init and SSH key — needed for terminal/exec even on existing volumes.
	{
		if err := verifyExt4Superblock(vmRootfs); err != nil {
			os.RemoveAll(vmDir)
			cleanup()
			return nil, fmt.Errorf("rootfs is not a valid ext4 image (incomplete or wrong file?): %w", err)
		}
		t = time.Now()
		pubKey := m.cfg.SSHKeyPath + ".pub"
		if err := injectRootfs(vmRootfs, pubKey, m.logger); err != nil {
			os.RemoveAll(vmDir)
			cleanup()
			return nil, fmt.Errorf("inject init/SSH into rootfs: %v", err)
		}
		log.Info("phase: inject_rootfs", zap.Duration("elapsed", time.Since(t)))
	}

	guestMAC := generateDeterministicMAC(req.VMID)
	ipBootArgs := network.GetBootArgs(vmIP)
	if req.NetworkSubnet != "" {
		ipBootArgs = network.GetBootArgsForSubnet(vmIP, req.NetworkSubnet)
	}
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 quiet loglevel=0 pci=off acpi=off root=/dev/vda rw rootfstype=ext4 init=/init i8042.noaux raid=noautodetect 8250.nr_uarts=1 net.ifnames=0 %s", ipBootArgs)

	drives := []firecracker.Drive{{DriveID: "rootfs", PathOnHost: vmRootfs, IsRootDevice: true, IsReadOnly: false}}
	driveLetters := []string{"vdb", "vdc", "vdd", "vde", "vdf", "vdg", "vdh"}

	// Append attached volumes
	drives = appendAttachedStorage(drives, req.AttachedStorage, driveLetters, m.logger)

	nics := []firecracker.NetworkInterface{
		{IfaceID: "eth0", GuestMAC: guestMAC, HostDevName: net.TapName},
	}

	// Apply rate limiters (asymmetric: download vs upload)
	rxRL, txRL, diskRL := buildRateLimiters(m.cfg, req)
	nics = applyNetRateLimiters(nics, rxRL, txRL)
	drives = applyDiskRateLimiter(drives, diskRL)

	cfg := firecracker.VMConfig{
		BootSource:        firecracker.BootSource{KernelImagePath: kernelPath, BootArgs: bootArgs, InitrdPath: initrdPath},
		Drives:            drives,
		MachineConfig:     firecracker.MachineConfig{VCPUCount: req.VCPUs, MemSizeMiB: req.MemoryMB},
		NetworkInterfaces: nics,
	}

	t = time.Now()
	pid, err := firecracker.LaunchInNamespace(m.cfg.VMBaseDir, req.VMID, m.cfg.FirecrackerBin, cfg, net.NamespaceName, req.VCPUs, req.MemoryMB, m.logger)
	if err != nil {
		os.RemoveAll(vmDir)
		cleanup()
		return nil, fmt.Errorf("failed to launch firecracker: %v", err)
	}
	log.Info("phase: firecracker_launch", zap.Duration("elapsed", time.Since(t)), zap.Int("pid", pid))

	if err := os.WriteFile(filepath.Join(vmDir, "tap_name"), []byte(net.TapName), 0644); err != nil {
		log.Warn("failed to write tap_name", zap.Error(err))
	}
	spec := struct {
		VCPUs           int      `json:"vcpus"`
		MemoryMB        int      `json:"memory_mb"`
		RootVolumePath  string   `json:"root_volume_path,omitempty"`
		NetworkBridge   string   `json:"network_bridge,omitempty"`
		NetworkSubnet   string   `json:"network_subnet,omitempty"`
		AttachedStorage []string `json:"attached_storage,omitempty"`
		BandwidthMbps   int      `json:"bandwidth_mbps,omitempty"`
		UploadMbps      int      `json:"upload_mbps,omitempty"`
		DiskIOPS        int      `json:"disk_iops,omitempty"`
		DiskBWMbps      int      `json:"disk_bw_mbps,omitempty"`
	}{req.VCPUs, req.MemoryMB, req.RootVolumePath, req.NetworkBridge, req.NetworkSubnet, req.AttachedStorage,
		req.BandwidthMbps, req.UploadMbps, req.DiskIOPS, req.DiskBWMbps}
	specData, err := json.Marshal(spec)
	if err != nil {
		syscall.Kill(-pid, syscall.SIGKILL) // kill orphaned Firecracker process
		os.RemoveAll(vmDir)
		cleanup()
		return nil, fmt.Errorf("marshal vm_spec: %w", err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, "vm_spec.json"), specData, 0644); err != nil {
		syscall.Kill(-pid, syscall.SIGKILL) // kill orphaned Firecracker process
		os.RemoveAll(vmDir)
		cleanup()
		return nil, fmt.Errorf("write vm_spec.json: %w", err)
	}

	m.registerVM(&VMInfo{VMID: req.VMID, Namespace: net.NamespaceName, IP: vmIP, Status: "running", PID: pid, NetworkBridge: req.NetworkBridge})

	t = time.Now()
	sshTimeout := agent.DefaultSSHTimeout
	if m.cfg.SSHTimeoutSec > 0 {
		sshTimeout = time.Duration(m.cfg.SSHTimeoutSec) * time.Second
	}
	sshReady := m.waitForSSH(vmIP, sshTimeout)
	log.Info("phase: ssh_wait", zap.Duration("elapsed", time.Since(t)), zap.Bool("ready", sshReady))

	log.Info("create complete", zap.Duration("total", time.Since(startTime)), zap.String("boot_method", "fresh"))

	msg := fmt.Sprintf("pid=%d, namespace=%s", pid, net.NamespaceName)
	return &CreateResponse{
		VMID: req.VMID, Status: "running", IPAddress: vmIP,
		BootMethod: "fresh", BootTimeMS: time.Since(startTime).Milliseconds(),
		SSHReady: sshReady, ImageSHA: sha, Message: &msg, Namespace: net.NamespaceName,
	}, nil
}

// Helper functions

func appendAttachedStorage(drives []firecracker.Drive, storagePaths []string, driveLetters []string, logger *zap.Logger) []firecracker.Drive {
	nonRootCount := 0
	for _, d := range drives {
		if !d.IsRootDevice {
			nonRootCount++
		}
	}
	for _, filePath := range storagePaths {
		idx := nonRootCount
		if idx >= len(driveLetters) {
			break
		}
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			logger.Warn("skipping attached storage: file not found", zap.String("path", filePath))
			continue
		}
		drives = append(drives, firecracker.Drive{DriveID: driveLetters[idx], PathOnHost: filePath, IsRootDevice: false, IsReadOnly: false})
		nonRootCount++
	}
	return drives
}

// validateImageURL checks that a URL is safe to fetch (no SSRF to internal networks).
func validateImageURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q: only http and https allowed", u.Scheme)
	}
	host := u.Hostname()
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("cannot resolve %q: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("URL %q resolves to private/loopback IP %s: blocked for security", rawURL, ipStr)
		}
	}
	return nil
}

// ssrfSafeClient returns an HTTP client that rejects connections to private/loopback IPs
// at dial time, preventing DNS rebinding attacks.
func ssrfSafeClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupHost(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, ipStr := range ips {
					ip := net.ParseIP(ipStr)
					if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()) {
						return nil, fmt.Errorf("blocked: %s resolves to private IP %s", host, ipStr)
					}
				}
				dialer := &net.Dialer{}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
			},
		},
	}
}

// httpStatusError represents an HTTP response with a non-2xx status code.
// It carries the status code so callers can distinguish retryable (5xx) from
// non-retryable (4xx) failures.
type httpStatusError struct {
	StatusCode int
	Status     string
}

func (e *httpStatusError) Error() string { return e.Status }

// isRetryable returns true for errors that are worth retrying: network errors
// and HTTP 5xx responses. HTTP 4xx and other validation errors are not retried.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// HTTP 5xx → retry; HTTP 4xx → don't
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode/100 == 5
	}
	// Network-level errors (DNS, connection refused, timeout, etc.) → retry
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// I/O errors during body read (connection reset, unexpected EOF) → retry
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	return false
}

// doDownload performs a single HTTP GET of rawurl, writing the (possibly gzip-
// decompressed) content to dest. Returns the final path, an optional SHA-256
// hex digest, and any error encountered.
func doDownload(rawurl, dest string) (string, *string, error) {
	client := ssrfSafeClient()
	resp, err := client.Get(rawurl)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", nil, &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}

	// Write to temp file, rename on success (prevents corrupt cache from partial downloads).
	// Use random suffix to avoid races between concurrent downloads of the same URL.
	tmpDest := fmt.Sprintf("%s.downloading.%d", dest, time.Now().UnixNano())
	f, err := os.Create(tmpDest)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		f.Close()
		os.Remove(tmpDest) // cleanup temp file if we didn't rename it
	}()
	h := sha256.New()

	// Limit download size to prevent disk exhaustion.
	// Apply limit to BOTH compressed body AND decompressed output (gzip bomb protection).
	const maxDownloadSize = 50 * 1024 * 1024 * 1024 // 50 GB
	body := io.LimitReader(resp.Body, maxDownloadSize)

	var src io.Reader
	// If response is gzip (by header or magic), decompress so we write a valid ext4 image for mount/inject.
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return "", nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer zr.Close()
		src = io.LimitReader(zr, maxDownloadSize) // limit decompressed size too
	} else {
		peek := make([]byte, 2)
		n, _ := io.ReadFull(body, peek)
		if n == 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			zr, err := gzip.NewReader(io.MultiReader(bytes.NewReader(peek), body))
			if err != nil {
				return "", nil, fmt.Errorf("gzip reader: %w", err)
			}
			defer zr.Close()
			src = io.LimitReader(zr, maxDownloadSize) // limit decompressed size too
		} else {
			src = io.MultiReader(bytes.NewReader(peek[:n]), body)
		}
	}
	if _, err := io.Copy(io.MultiWriter(f, h), src); err != nil {
		return "", nil, err
	}
	// Flush and close before rename
	if err := f.Close(); err != nil {
		return "", nil, fmt.Errorf("flush download: %w", err)
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		return "", nil, fmt.Errorf("finalize download: %w", err)
	}
	shaStr := hex.EncodeToString(h.Sum(nil))
	return dest, &shaStr, nil
}

// downloadFromURL downloads url to dest (creates parent dirs). Decompresses
// gzip if needed. Returns path and optional SHA256 hex. Transient failures
// (network errors, HTTP 5xx) are retried with exponential backoff.
func downloadFromURL(rawurl, dest string) (string, *string, error) {
	if err := validateImageURL(rawurl); err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(dest); err == nil {
		// Verify cached file is valid ext4 before returning
		if verifyExt4Superblock(dest) != nil {
			os.Remove(dest) // corrupt cache, re-download
		} else {
			return dest, nil, nil
		}
	}
	os.MkdirAll(filepath.Dir(dest), 0755)

	var lastErr error
	for attempt := 0; attempt < agent.DefaultMaxDownloadRetries; attempt++ {
		if attempt > 0 {
			delay := agent.DefaultDownloadBaseDelay * (1 << attempt)
			if delay > agent.MaxDownloadBackoff {
				delay = agent.MaxDownloadBackoff
			}
			time.Sleep(delay)
		}

		path, sha, err := doDownload(rawurl, dest)
		if err == nil {
			return path, sha, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("image download failed after %d attempts: %w", agent.DefaultMaxDownloadRetries, lastErr)
}

func hasHTTPPrefix(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// cachePathForURL returns a cache file path that includes a hash of the URL so different URLs never share the same file.
func cachePathForURL(cacheDir, url string) string {
	h := sha256.Sum256([]byte(url))
	hexHash := hex.EncodeToString(h[:])[:16]
	return filepath.Join(cacheDir, hexHash+"_rootfs.ext4")
}

// verifyExt4Superblock reads the ext4 magic at offset 1080 (superblock s_magic). Returns nil if valid.
func verifyExt4Superblock(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Ext4 superblock is at offset 1024; s_magic is at offset 56 within it = 1080.
	if _, err := f.Seek(1080, io.SeekStart); err != nil {
		return err
	}
	b := make([]byte, 2)
	if _, err := io.ReadFull(f, b); err != nil {
		return err
	}
	if b[0] != 0x53 || b[1] != 0xEF {
		return fmt.Errorf("ext4 magic at offset 1080 is 0x%02x%02x, expected 0x53EF", b[0], b[1])
	}
	return nil
}

// startVM starts a stopped VM from its existing vmDir (rootfs.ext4 and optional vm_spec.json).
func startVM(ctx context.Context, m *Manager, vmID string) (*CreateResponse, error) {
	startTime := time.Now()
	log := reqLog(ctx, m.logger, vmID)
	vmDir := filepath.Join(m.cfg.VMBaseDir, vmID)
	vmRootfs := filepath.Join(vmDir, "rootfs.ext4")

	vcpus, memoryMB := 2, 512
	var netBridge, netSubnet string
	var savedReq CreateRequest // for rate limiters and other saved fields
	if data, err := os.ReadFile(filepath.Join(vmDir, "vm_spec.json")); err == nil {
		var spec struct {
			VCPUs          int    `json:"vcpus"`
			MemoryMB       int    `json:"memory_mb"`
			RootVolumePath string `json:"root_volume_path"`
			NetworkBridge  string `json:"network_bridge"`
			NetworkSubnet  string `json:"network_subnet"`
			BandwidthMbps  int    `json:"bandwidth_mbps"`
			UploadMbps     int    `json:"upload_mbps"`
			DiskIOPS       int    `json:"disk_iops"`
			DiskBWMbps     int    `json:"disk_bw_mbps"`
		}
		if json.Unmarshal(data, &spec) == nil {
			if spec.VCPUs > 0 && spec.MemoryMB >= 128 {
				vcpus, memoryMB = spec.VCPUs, spec.MemoryMB
			}
			if spec.RootVolumePath != "" {
				// Verify the root volume is still attached to THIS VM before using it.
				// If detached (e.g. deployed to another VM), fall back to local rootfs copy.
				useVolume := true
				if m.db != nil {
					var attached sql.NullString
					err := m.db.QueryRow("SELECT attached FROM volume WHERE path = ?", spec.RootVolumePath).Scan(&attached)
					if err != nil || !attached.Valid || attached.String != vmID {
						log.Warn("root volume no longer attached to this VM, using local rootfs",
							zap.String("volume_path", spec.RootVolumePath))
						useVolume = false
					}
				}
				if useVolume {
					vmRootfs = spec.RootVolumePath
				}
			}
			netBridge = spec.NetworkBridge
			netSubnet = spec.NetworkSubnet
			savedReq.BandwidthMbps = spec.BandwidthMbps
			savedReq.UploadMbps = spec.UploadMbps
			savedReq.DiskIOPS = spec.DiskIOPS
			savedReq.DiskBWMbps = spec.DiskBWMbps
		}
	}

	if _, err := os.Stat(vmRootfs); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("VM %s has no rootfs (run deploy first)", vmID)
		}
		return nil, err
	}

	// Re-use existing IP or allocate new one
	vmIP := network.GetAllocatedIP(m.db, vmID)
	freshlyAllocated := false
	if vmIP == "" {
		var allocErr error
		if netSubnet != "" {
			vmIP, allocErr = network.AllocateIPInSubnet(m.db, vmID, netSubnet)
		} else {
			vmIP, allocErr = network.AllocateIP(m.db, vmID)
		}
		if allocErr != nil {
			return nil, fmt.Errorf("allocate IP: %v", allocErr)
		}
		freshlyAllocated = true
	}

	t := time.Now()
	net := network.NewVMNetwork(vmID, vmIP, m.logger, netBridge)
	net.BlockSMTP = m.cfg.BlockSMTP
	if err := net.Create(); err != nil {
		if freshlyAllocated {
			network.ReleaseIP(m.db, vmID)
		}
		return nil, fmt.Errorf("failed to create network namespace: %v", err)
	}
	log.Info("phase: network_setup", zap.Duration("elapsed", time.Since(t)), zap.String("vm_ip", vmIP))

	guestMAC := generateDeterministicMAC(vmID)
	kernelPath := m.cfg.KernelImagePath
	initrdPath := m.cfg.KernelInitrdPath
	startIPBootArgs := network.GetBootArgs(vmIP)
	if netSubnet != "" {
		startIPBootArgs = network.GetBootArgsForSubnet(vmIP, netSubnet)
	}
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 quiet loglevel=0 pci=off acpi=off root=/dev/vda rw rootfstype=ext4 init=/init i8042.noaux raid=noautodetect 8250.nr_uarts=1 net.ifnames=0 %s", startIPBootArgs)

	drives := []firecracker.Drive{{DriveID: "rootfs", PathOnHost: vmRootfs, IsRootDevice: true, IsReadOnly: false}}
	driveLetters := []string{"vdb", "vdc", "vdd", "vde", "vdf", "vdg", "vdh"}

	// Re-attach data volumes from DB (attached via API or --attach at deploy)
	if m.db != nil {
		volRows, err := m.db.Query(
			"SELECT path FROM volume WHERE attached = ? AND role != 'root' AND status = 'attached'", vmID)
		if err == nil {
			var dbVolPaths []string
			for volRows.Next() {
				var p string
				if volRows.Scan(&p) == nil {
					dbVolPaths = append(dbVolPaths, p)
				}
			}
			volRows.Close()
			if len(dbVolPaths) > 0 {
				drives = appendAttachedStorage(drives, dbVolPaths, driveLetters, m.logger)
			}
		}
	}

	// Fallback: also check vm_spec.json for backward compat (old VMs without DB volumes)
	if m.db == nil {
		if specData, err := os.ReadFile(filepath.Join(vmDir, "vm_spec.json")); err == nil {
			var spec struct {
				AttachedStorage []string `json:"attached_storage"`
			}
			if json.Unmarshal(specData, &spec) == nil {
				drives = appendAttachedStorage(drives, spec.AttachedStorage, driveLetters, m.logger)
			}
		}
	}

	nics := []firecracker.NetworkInterface{
		{IfaceID: "eth0", GuestMAC: guestMAC, HostDevName: net.TapName},
	}

	// Restore rate limiters from saved spec
	rxRL, txRL, diskRL := buildRateLimiters(m.cfg, &savedReq)
	nics = applyNetRateLimiters(nics, rxRL, txRL)
	drives = applyDiskRateLimiter(drives, diskRL)

	cfg := firecracker.VMConfig{
		BootSource:        firecracker.BootSource{KernelImagePath: kernelPath, BootArgs: bootArgs, InitrdPath: initrdPath},
		Drives:            drives,
		MachineConfig:     firecracker.MachineConfig{VCPUCount: vcpus, MemSizeMiB: memoryMB},
		NetworkInterfaces: nics,
	}

	t = time.Now()
	pid, err := firecracker.LaunchInNamespace(m.cfg.VMBaseDir, vmID, m.cfg.FirecrackerBin, cfg, net.NamespaceName, vcpus, memoryMB, m.logger)
	if err != nil {
		net.Cleanup(m.cfg.HostInterface)
		if freshlyAllocated {
			network.ReleaseIP(m.db, vmID)
		}
		return nil, fmt.Errorf("failed to launch firecracker: %v", err)
	}
	log.Info("phase: firecracker_launch", zap.Duration("elapsed", time.Since(t)), zap.Int("pid", pid))

	_ = os.WriteFile(filepath.Join(vmDir, "tap_name"), []byte(net.TapName), 0644)

	m.registerVM(&VMInfo{VMID: vmID, Namespace: net.NamespaceName, IP: vmIP, Status: "running", PID: pid, NetworkBridge: netBridge})

	// Restore port rules from DB (stop cleaned iptables but kept DB rows)
	if m.db != nil {
		prRows, prErr := m.db.Query("SELECT host_port, vm_port, protocol FROM port_rule WHERE vm_id = ?", vmID)
		if prErr == nil {
			restored := 0
			for prRows.Next() {
				var hp, vp int
				var proto string
				if prRows.Scan(&hp, &vp, &proto) == nil {
					n := network.NewVMNetwork(vmID, vmIP, m.logger, netBridge)
					if n.ExposePort(m.cfg.HostInterface, hp, vp, proto) == nil {
						restored++
					}
				}
			}
			prRows.Close()
			if restored > 0 {
				log.Info("restored port rules after start", zap.Int("count", restored))
			}
		}
	}

	t = time.Now()
	sshTimeout := agent.DefaultSSHTimeout
	if m.cfg.SSHTimeoutSec > 0 {
		sshTimeout = time.Duration(m.cfg.SSHTimeoutSec) * time.Second
	}
	sshReady := m.waitForSSH(vmIP, sshTimeout)
	log.Info("phase: ssh_wait", zap.Duration("elapsed", time.Since(t)), zap.Bool("ready", sshReady))

	msg := fmt.Sprintf("pid=%d, namespace=%s (started from existing rootfs)", pid, net.NamespaceName)
	return &CreateResponse{
		VMID: vmID, Status: "running", IPAddress: vmIP,
		BootMethod: "start", BootTimeMS: time.Since(startTime).Milliseconds(),
		SSHReady: sshReady, Message: &msg, Namespace: net.NamespaceName,
	}, nil
}

// checkDiskSpace verifies that the filesystem at dir has at least minFreeMB megabytes free.
func checkDiskSpace(dir string, minFreeMB int64) error {
	if minFreeMB <= 0 {
		return nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return fmt.Errorf("check disk space: %w", err)
	}
	freeMB := int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024)
	if freeMB < minFreeMB {
		return fmt.Errorf("insufficient disk space: %d MB free, need at least %d MB", freeMB, minFreeMB)
	}
	return nil
}

// isE2fsckSuccess returns true for exit code 0 (no errors) or 1 (errors corrected).
func isE2fsckSuccess(exitCode int) bool {
	return exitCode == 0 || exitCode == 1
}

// resizeRootfsTo grows the ext4 image at path to at least storageMB MiB when the file is smaller.
// Requires e2fsck and resize2fs (e2fsprogs). No-op if storageMB <= 0 or file is already >= storageMB.
func resizeRootfsTo(path string, storageMB int, log *zap.Logger) error {
	if storageMB <= 0 {
		return nil
	}
	if _, err := exec.LookPath("e2fsck"); err != nil {
		return fmt.Errorf("e2fsck not found (install e2fsprogs): %w", err)
	}
	if _, err := exec.LookPath("resize2fs"); err != nil {
		return fmt.Errorf("resize2fs not found (install e2fsprogs): %w", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	currentMB := int(fi.Size() / (1024 * 1024))
	if currentMB >= storageMB {
		return nil
	}
	sizeBytes := int64(storageMB) * 1024 * 1024
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open rootfs for resize: %w", err)
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return fmt.Errorf("truncate rootfs: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close rootfs: %w", err)
	}
	// Run e2fsck before resize to ensure filesystem consistency
	if out, err := exec.Command("e2fsck", "-f", "-y", path).CombinedOutput(); err != nil {
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if !isE2fsckSuccess(exitCode) {
			return fmt.Errorf("e2fsck: %w (output: %s)", err, bytes.TrimSpace(out))
		}
		if log != nil && exitCode == 1 {
			log.Info("e2fsck applied fixes before resize", zap.String("path", path), zap.String("output", string(bytes.TrimSpace(out))))
		}
	}
	// resize2fs with one argument expands the filesystem to fill the device/file
	if out, err := exec.Command("resize2fs", path).CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs: %w (output: %s)", err, bytes.TrimSpace(out))
	}
	if log != nil {
		log.Info("resized rootfs", zap.Int("target_mb", storageMB), zap.Int64("size_bytes", sizeBytes))
	}
	return nil
}

// mbpsToRL builds a bandwidth RateLimiter from a Mbps value. Returns nil if mbps <= 0.
func mbpsToRL(mbps int) *firecracker.RateLimiter {
	if mbps <= 0 {
		return nil
	}
	bwBytes := int64(mbps) * 1_000_000 / 8
	return &firecracker.RateLimiter{
		Bandwidth: &firecracker.TokenBucket{
			Size:         bwBytes,
			OneTimeBurst: bwBytes,
			RefillTime:   1000,
		},
	}
}

// buildRateLimiters resolves effective limits (request > 0 wins, else config default)
// and returns Firecracker rate limiters for network (rx/tx separate) and disk.
func buildRateLimiters(cfg *config.Config, req *CreateRequest) (rxRL, txRL, diskRL *firecracker.RateLimiter) {
	// Network download (rx = into VM)
	dlMbps := req.BandwidthMbps
	if dlMbps <= 0 {
		dlMbps = cfg.DefaultBandwidthMbps
	}
	rxRL = mbpsToRL(dlMbps)

	// Network upload (tx = out of VM) — separate, lower limit
	ulMbps := req.UploadMbps
	if ulMbps <= 0 {
		ulMbps = cfg.DefaultUploadMbps
	}
	txRL = mbpsToRL(ulMbps)

	// Disk I/O
	iops := req.DiskIOPS
	if iops <= 0 {
		iops = cfg.DefaultIOPS
	}
	diskBWMbps := req.DiskBWMbps
	if diskBWMbps <= 0 {
		diskBWMbps = cfg.DefaultDiskBWMbps
	}
	if iops > 0 || diskBWMbps > 0 {
		diskRL = &firecracker.RateLimiter{}
		if diskBWMbps > 0 {
			bwBytes := int64(diskBWMbps) * 1_048_576 // MB/s → bytes per refill period
			diskRL.Bandwidth = &firecracker.TokenBucket{
				Size:         bwBytes,
				OneTimeBurst: bwBytes * 50, // large burst so boot I/O isn't throttled
				RefillTime:   1000,
			}
		}
		if iops > 0 {
			diskRL.Ops = &firecracker.TokenBucket{
				Size:         int64(iops),
				OneTimeBurst: int64(iops) * 50, // large burst so boot I/O isn't throttled
				RefillTime:   1000,
			}
		}
	}

	return rxRL, txRL, diskRL
}

// applyNetRateLimiters sets separate rx (download) and tx (upload) rate limiters on all NICs.
func applyNetRateLimiters(nics []firecracker.NetworkInterface, rxRL, txRL *firecracker.RateLimiter) []firecracker.NetworkInterface {
	for i := range nics {
		nics[i].RxRateLimiter = rxRL
		nics[i].TxRateLimiter = txRL
	}
	return nics
}

// applyDiskRateLimiter sets rate limiter on all drives.
func applyDiskRateLimiter(drives []firecracker.Drive, rl *firecracker.RateLimiter) []firecracker.Drive {
	if rl == nil {
		return drives
	}
	for i := range drives {
		drives[i].RateLimiter = rl
	}
	return drives
}
