package machine

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.uber.org/zap"
)

// initScriptContent is the Sistemo microVM /init script embedded from vm-init.sh.
//
//go:embed vm-init.sh
var initScriptContent []byte

// injectRootfs mounts rootfsExt4 at a temp dir, writes /init and appends pubKey to /root/.ssh/authorized_keys, then unmounts.
func injectRootfs(rootfsExt4, pubKeyPath string, logger *zap.Logger) error {
	mnt, err := os.MkdirTemp("", "sistemo-inject-")
	if err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}
	defer os.RemoveAll(mnt)

	// Mount as ext4 with loop so the kernel accepts the file; rw so we can inject /init and authorized_keys.
	cmd := exec.Command("mount", "-t", "ext4", "-o", "loop", rootfsExt4, mnt)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount rootfs (is the file a valid ext4 image?): %w (%s)", err, string(out))
	}
	unmount := func() {
		if err := exec.Command("umount", mnt).Run(); err != nil {
			// Lazy unmount as fallback to prevent stale mounts
			_ = exec.Command("umount", "-l", mnt).Run()
		}
	}
	defer unmount()

	// /init
	initPath := filepath.Join(mnt, "init")
	if err := os.WriteFile(initPath, initScriptContent, 0755); err != nil {
		return fmt.Errorf("write /init: %w", err)
	}
	// /sbin/init -> /init (so kernel init=/init finds it)
	sbinInit := filepath.Join(mnt, "sbin", "init")
	_ = os.MkdirAll(filepath.Dir(sbinInit), 0755)
	_ = os.Remove(sbinInit)
	if err := os.Symlink("/init", sbinInit); err != nil {
		// some images may have read-only or existing sbin/init; copy as fallback
		_ = os.WriteFile(sbinInit, initScriptContent, 0755)
	}

	// /root/.ssh/authorized_keys
	pubKey, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return fmt.Errorf("read public key %s: %w", pubKeyPath, err)
	}
	sshDir := filepath.Join(mnt, "root", ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("create /root/.ssh: %w", err)
	}
	authKeys := filepath.Join(sshDir, "authorized_keys")
	f, err := os.OpenFile(authKeys, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open authorized_keys: %w", err)
	}
	if _, err := f.Write(pubKey); err != nil {
		f.Close()
		return fmt.Errorf("write authorized_keys: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}

	logger.Info("injected /init and SSH key into URL rootfs", zap.String("rootfs", rootfsExt4))
	return nil
}
