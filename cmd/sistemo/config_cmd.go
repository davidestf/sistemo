package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/davidestf/sistemo/internal/agent/config"
)

func configShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir := getDataDirFromCmd(cmd)
			configPath := filepath.Join(dataDir, "config.yml")
			cfg, err := config.LoadWithFile(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			// Output as YAML for readability
			out := map[string]interface{}{
				"port":                   cfg.Port,
				"host_interface":         cfg.HostInterface,
				"max_vcpus":              cfg.MaxVCPUs,
				"max_memory_mb":          cfg.MaxMemoryMB,
				"max_storage_mb":         cfg.MaxStorageMB,
				"min_disk_free_mb":       cfg.MinDiskFreeMB,
				"default_bandwidth_mbps": cfg.DefaultBandwidthMbps,
				"default_upload_mbps":    cfg.DefaultUploadMbps,
				"default_iops":           cfg.DefaultIOPS,
				"default_disk_bw_mbps":   cfg.DefaultDiskBWMbps,
			}
			data, err := yaml.Marshal(out)
			if err != nil {
				return err
			}
			fmt.Printf("# Effective config (file: %s, env vars override)\n", configPath)
			fmt.Print(string(data))
			return nil
		},
	}
	return cmd
}
