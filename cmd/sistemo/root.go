package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/davidestf/sistemo/internal/db"
)

const defaultDataDir = ""

type contextKey string

const (
	contextKeyLogger  contextKey = "logger"
	contextKeyDataDir contextKey = "dataDir"
	contextKeyDB      contextKey = "db"
)

func newLogger() *zap.Logger {
	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.DisableStacktrace = true
	logger, _ := cfg.Build()
	return logger
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sistemo",
		Short: "Self-hosted Firecracker VM runner",
		Long:  "Sistemo lets you run real Firecracker microVMs on your own Linux machine. Use 'sistemo up' to start the daemon, then 'sistemo vm deploy <image>' to create a VM.",
	}
	var dataDir string
	cmd.PersistentFlags().StringVar(&dataDir, "data-dir", defaultDataDir, "Data directory (default: ~/.sistemo)")
	cmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		logger := newLogger()
		dir := getDataDir(dataDir)
		ctx := context.Background()
		ctx = context.WithValue(ctx, contextKeyLogger, logger)
		ctx = context.WithValue(ctx, contextKeyDataDir, dir)
		cmd.SetContext(ctx)
		return nil
	}
	return cmd
}

func getLogger(cmd *cobra.Command) *zap.Logger {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Context() != nil {
			if v := c.Context().Value(contextKeyLogger); v != nil {
				return v.(*zap.Logger)
			}
		}
	}
	return newLogger()
}

func getDataDirFromCmd(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Context() != nil {
			if v := c.Context().Value(contextKeyDataDir); v != nil {
				return v.(string)
			}
		}
	}
	return getDataDir("")
}

func getDataDir(dataDir string) string {
	if dataDir != "" {
		return dataDir
	}
	if syscall.Geteuid() == 0 && os.Getenv("SUDO_USER") != "" {
		if home, ok := os.LookupEnv("SUDO_HOME"); ok && home != "" {
			return filepath.Join(home, ".sistemo")
		}
		return filepath.Join("/home", os.Getenv("SUDO_USER"), ".sistemo")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".sistemo")
}

func getDB(dataDir string) (*sql.DB, error) {
	return db.New(dataDir)
}

func getDBFromCmd(cmd *cobra.Command) *sql.DB {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Context() != nil {
			if v := c.Context().Value(contextKeyDB); v != nil {
				return v.(*sql.DB)
			}
		}
	}
	return nil
}

func syncLogger(cmd *cobra.Command) {
	if l := cmd.Context().Value(contextKeyLogger); l != nil {
		_ = l.(*zap.Logger).Sync()
	}
}

var versionStr = "dev"

func Execute() {
	root := rootCmd()
	root.SilenceUsage = true
	root.CompletionOptions.DisableDefaultCmd = true
	root.Version = versionStr

	// Backward compatibility: sistemo --up runs the daemon
	var up bool
	var showVersion bool
	root.Flags().BoolVar(&up, "up", false, "Start the daemon (HTTP server)")
	root.Flags().BoolVarP(&showVersion, "version", "v", false, "Print version and exit")
	root.PreRunE = func(cmd *cobra.Command, args []string) error {
		if showVersion {
			fmt.Println(versionStr)
			os.Exit(0)
		}
		if up {
			runDaemon(getLogger(cmd), getDataDirFromCmd(cmd))
			os.Exit(0)
		}
		return nil
	}

	root.AddCommand(upCmd())
	root.AddCommand(installCmd())
	root.AddCommand(sshKeyCmd())
	root.AddCommand(imageCmd())
	root.AddCommand(volumeCmd())
	root.AddCommand(vmCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
