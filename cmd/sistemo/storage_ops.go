package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
)

func runStorageCreate(logger *zap.Logger, sizeMB int, name string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vol, err := daemon.CreateVolume(baseURL, sizeMB, name)
	if err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	fmt.Printf("Created volume %s (%s) %d MB\n", vol.Name, vol.ID, vol.SizeMB)
	return nil
}

func runStorageList(logger *zap.Logger) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	list, err := daemon.ListVolumes(baseURL)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	if len(list) == 0 {
		if isJSON() {
			printJSON([]interface{}{})
			return nil
		}
		fmt.Println("No volumes. Create one with: sistemo volume create <size_mb> [--name=myvol]")
		return nil
	}
	if isJSON() {
		printJSON(list)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSIZE\tSTATUS\tATTACHED TO")
	for _, v := range list {
		attached := v.MachineID
		if attached == "" {
			attached = "(none)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d MB\t%s\t%s\n", v.ID, v.Name, v.SizeMB, colorStatus(v.Status), attached)
	}
	tw.Flush()
	return nil
}

func runStorageDelete(logger *zap.Logger, idOrName string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	if err := daemon.DeleteVolume(baseURL, idOrName); err != nil {
		return fmt.Errorf("delete volume: %w", err)
	}
	fmt.Printf("Deleted volume %s\n", idOrName)
	return nil
}

func runStorageShow(logger *zap.Logger, idOrName string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable: %w", err)
	}
	vol, err := daemon.GetVolume(baseURL, idOrName)
	if err != nil {
		return fmt.Errorf("get volume: %w", err)
	}
	if isJSON() {
		printJSON(vol)
		return nil
	}
	fmt.Printf("ID:         %s\n", vol.ID)
	fmt.Printf("Name:       %s\n", vol.Name)
	fmt.Printf("Size:       %d MB\n", vol.SizeMB)
	fmt.Printf("Status:     %s\n", colorStatus(vol.Status))
	fmt.Printf("Path:       %s\n", vol.Path)
	if vol.MachineID != "" {
		fmt.Printf("Attached:   %s\n", vol.MachineID)
	}
	if vol.Created != "" {
		fmt.Printf("Created:    %s\n", vol.Created)
	}
	return nil
}

func runStorageResize(logger *zap.Logger, idOrName string, sizeMB int) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	if err := daemon.ResizeVolume(baseURL, idOrName, sizeMB); err != nil {
		return fmt.Errorf("resize volume: %w", err)
	}
	fmt.Printf("Resized volume %s to %d MB\n", idOrName, sizeMB)
	return nil
}

func runStorageAttach(logger *zap.Logger, vmIDOrName, volumeIDOrName string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	if err := daemon.AttachVolume(baseURL, vmIDOrName, volumeIDOrName); err != nil {
		return fmt.Errorf("attach volume: %w", err)
	}
	fmt.Printf("Attached volume %s to VM %s\n", volumeIDOrName, vmIDOrName)
	return nil
}

func runStorageDetach(logger *zap.Logger, vmIDOrName, volumeIDOrName string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	if err := daemon.DetachVolume(baseURL, vmIDOrName, volumeIDOrName); err != nil {
		return fmt.Errorf("detach volume: %w", err)
	}
	fmt.Printf("Detached volume %s\n", volumeIDOrName)
	return nil
}
