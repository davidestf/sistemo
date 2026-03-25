package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
)

func runList(logger *zap.Logger, database *sql.DB) error {
	rows, err := database.Query(`
		SELECT v.id, v.name, v.status, v.image, v.ip_address, COALESCE(n.name, 'default')
		FROM vm v LEFT JOIN network n ON v.network_id = n.id
		WHERE v.status != 'deleted'`)
	if err != nil {
		return fmt.Errorf("query vms: %w", err)
	}
	defer rows.Close()
	var rowsData []struct{ id, name, status, image, ip, network string }
	for rows.Next() {
		var id, name, status, image, ip, net sql.NullString
		if err := rows.Scan(&id, &name, &status, &image, &ip, &net); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		img := image.String
		if img != "" && len(img) > 40 {
			img = "..." + filepath.Base(img)
		}
		rowsData = append(rowsData, struct{ id, name, status, image, ip, network string }{
			id.String, name.String, status.String, img, ip.String, net.String,
		})
	}
	if len(rowsData) == 0 {
		if isJSON() {
			printJSON([]interface{}{})
			return nil
		}
		fmt.Println("No VMs. Deploy one with: sistemo vm deploy <image>")
		return nil
	}
	// Build volume count map
	volCounts := make(map[string]int)
	volRows, err := database.Query("SELECT attached, COUNT(*) FROM volume WHERE attached IS NOT NULL GROUP BY attached")
	if err == nil {
		defer volRows.Close()
		for volRows.Next() {
			var vmID string
			var cnt int
			if volRows.Scan(&vmID, &cnt) == nil {
				volCounts[vmID] = cnt
			}
		}
	}

	if isJSON() {
		var result []map[string]interface{}
		for _, r := range rowsData {
			result = append(result, map[string]interface{}{
				"id":      r.id,
				"name":    r.name,
				"status":  r.status,
				"image":   r.image,
				"ip":      r.ip,
				"network": r.network,
				"volumes": volCounts[r.id],
			})
		}
		printJSON(result)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tIMAGE\tIP\tNETWORK\tVOLUMES")
	for _, r := range rowsData {
		vc := volCounts[r.id]
		volStr := "-"
		if vc > 0 {
			volStr = fmt.Sprintf("%d", vc)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.id, r.name, colorStatus(r.status), r.image, r.ip, r.network, volStr)
	}
	tw.Flush()
	return nil
}

func runStatus(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	row := database.QueryRow(
		"SELECT id, name, status, image, ip_address, namespace, created_at, network_id FROM vm WHERE id = ?",
		vmID,
	)
	var id, name, status, image, ip, ns, created, networkID sql.NullString
	if err := row.Scan(&id, &name, &status, &image, &ip, &ns, &created, &networkID); err != nil {
		return fmt.Errorf("lookup vm details: %w", err)
	}

	// Resolve network name
	netName := "default"
	if networkID.Valid && networkID.String != "" {
		var nn string
		if database.QueryRow("SELECT name FROM network WHERE id = ?", networkID.String).Scan(&nn) == nil {
			netName = nn
		}
	}

	// Collect volumes
	type volInfo struct {
		Name string `json:"name"`
		Role string `json:"role"`
		Size int    `json:"size_mb"`
		Path string `json:"path"`
	}
	var volumes []volInfo
	volRows, err := database.Query("SELECT name, size_mb, role, path FROM volume WHERE attached = ?", id.String)
	if err == nil {
		defer volRows.Close()
		for volRows.Next() {
			var v volInfo
			if volRows.Scan(&v.Name, &v.Size, &v.Role, &v.Path) == nil {
				volumes = append(volumes, v)
			}
		}
	}

	// Collect ports
	type portInfo struct {
		HostPort int    `json:"host_port"`
		VMPort   int    `json:"vm_port"`
		Protocol string `json:"protocol"`
	}
	var ports []portInfo
	portRows, err := database.Query("SELECT host_port, vm_port, protocol FROM port_rule WHERE vm_id = ?", id.String)
	if err == nil {
		defer portRows.Close()
		for portRows.Next() {
			var p portInfo
			if portRows.Scan(&p.HostPort, &p.VMPort, &p.Protocol) == nil {
				ports = append(ports, p)
			}
		}
	}

	if isJSON() {
		result := map[string]interface{}{
			"id":        id.String,
			"name":      name.String,
			"status":    status.String,
			"image":     image.String,
			"ip":        ip.String,
			"namespace": ns.String,
			"created":   created.String,
			"network":   netName,
			"volumes":   volumes,
			"ports":     ports,
		}
		printJSON(result)
		return nil
	}

	fmt.Printf("ID:        %s\n", id.String)
	fmt.Printf("Name:      %s\n", name.String)
	fmt.Printf("Status:    %s\n", colorStatus(status.String))
	fmt.Printf("Image:     %s\n", image.String)
	fmt.Printf("IP:        %s\n", ip.String)
	fmt.Printf("Namespace: %s\n", ns.String)
	fmt.Printf("Created:   %s\n", created.String)
	fmt.Printf("Network:   %s\n", netName)

	if len(volumes) > 0 {
		fmt.Printf("Volumes:\n")
		for _, v := range volumes {
			fmt.Printf("  %s (%s, %d MB) %s\n", v.Name, v.Role, v.Size, v.Path)
		}
	}

	if len(ports) > 0 {
		fmt.Printf("Ports:\n")
		for _, p := range ports {
			fmt.Printf("  host:%d -> VM:%d (%s)\n", p.HostPort, p.VMPort, p.Protocol)
		}
	}
	return nil
}

func runLogs(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	baseURL := daemon.URL()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(baseURL + "/vms/" + vmID + "/logs")
	if err != nil {
		return fmt.Errorf("fetch logs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("logs request failed: HTTP %d", resp.StatusCode)
	}
	io.Copy(os.Stdout, resp.Body)
	return nil
}

func runImageList(dataDir string) {
	imagesDir := filepath.Join(dataDir, "images")

	// Local images
	entries, _ := os.ReadDir(imagesDir)
	var localImages []struct{ name, file string; sizeMB int64 }
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ext4") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".rootfs.ext4")
		name = strings.TrimSuffix(name, ".ext4")
		var sizeMB int64
		if info, err := e.Info(); err == nil {
			sizeMB = info.Size() / (1024 * 1024)
		}
		localImages = append(localImages, struct{ name, file string; sizeMB int64 }{name, e.Name(), sizeMB})
	}

	if len(localImages) > 0 {
		fmt.Println("Local images:")
		fmt.Printf("  %-20s  %-10s  %s\n", "NAME", "SIZE", "FILE")
		for _, img := range localImages {
			fmt.Printf("  %-20s  %-10s  %s\n", img.name, fmt.Sprintf("%d MB", img.sizeMB), img.file)
		}
		fmt.Println()
	} else {
		fmt.Println("No local images.")
		fmt.Println()
	}

	// Remote images
	fmt.Printf("Registry: %s\n", registryURL())
	localNames := make(map[string]bool)
	for _, img := range localImages {
		localNames[strings.ToLower(img.name)] = true
	}

	idx := fetchRegistryIndex()
	if idx != nil && len(idx.Images) > 0 {
		fmt.Println("Available:")
		for _, img := range idx.Images {
			status := fmt.Sprintf("sistemo image pull %s", img.Name)
			if localNames[strings.ToLower(img.Name)] {
				status = "(cached)"
			}
			if img.Description != "" {
				fmt.Printf("  %-15s  %-25s  %s\n", img.Name, img.Description, status)
			} else {
				fmt.Printf("  %-15s  %s\n", img.Name, status)
			}
		}
	} else {
		// Fallback to hardcoded list if registry is unreachable
		fmt.Println("Available:")
		for _, name := range knownRemoteImages {
			if localNames[name] {
				fmt.Printf("  %-15s  (cached)\n", name)
			} else {
				fmt.Printf("  %-15s  sistemo image pull %s\n", name, name)
			}
		}
	}

	fmt.Println()
	fmt.Println("Deploy:  sistemo vm deploy <name>")
	fmt.Println("Pull:    sistemo image pull <name>")
	fmt.Println("Build:   sudo sistemo image build <docker-image>")
}

func runImagePull(logger *zap.Logger, dataDir, name string) error {
	imagesDir := filepath.Join(dataDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return fmt.Errorf("create images dir: %w", err)
	}

	dest := filepath.Join(imagesDir, name+".rootfs.ext4")
	suffix := archSuffix()

	fmt.Printf("Pulling %s from %s... ", name, registryURL())
	err := downloadImageToFile(registryURL()+name+suffix+".rootfs.ext4.gz", dest)
	if err != nil {
		err = downloadImageToFile(registryURL()+name+suffix+".rootfs.ext4", dest)
	}
	if err != nil {
		fmt.Printf("failed: %v\n", err)
		return fmt.Errorf("pull image %s: %w", name, err)
	}

	info, _ := os.Stat(dest)
	sizeMB := int64(0)
	if info != nil {
		sizeMB = info.Size() / (1024 * 1024)
	}
	fmt.Printf("done (%d MB)\n", sizeMB)
	fmt.Printf("Deploy with: sistemo vm deploy %s\n", name)
	return nil
}
