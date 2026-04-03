package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/davidestf/sistemo/internal/db"
)

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
	logger, err := cfg.Build()
	if err != nil {
		// Fallback to nop logger — should never happen with production config
		return zap.NewNop()
	}
	return logger
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sistemo",
		Short: "Self-hosted Firecracker machine runner",
		Long:  "Sistemo lets you run real Firecracker microVMs on your own Linux machine.\nUse 'sistemo up' to start the daemon, then 'sistemo machine deploy <image>' to create a machine.",
	}
	var dataDir string
	cmd.PersistentFlags().StringVar(&dataDir, "data-dir", "", "Data directory (default: ~/.sistemo)")
	cmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", "Output format: text or json")
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

var versionStr = "dev"

func Execute() {
	root := rootCmd()
	root.SilenceUsage = true
	root.CompletionOptions.DisableDefaultCmd = true // we provide our own completion command
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
			if err := runDaemon(getLogger(cmd), getDataDirFromCmd(cmd)); err != nil {
				return err
			}
			os.Exit(0)
		}
		return nil
	}

	root.AddCommand(upCmd())
	root.AddCommand(installCmd())
	root.AddCommand(sshKeyCmd())
	root.AddCommand(imageCmd())
	root.AddCommand(volumeCmd())
	root.AddCommand(machineCmd())
	root.AddCommand(networkCmd())
	root.AddCommand(serviceCmd())
	root.AddCommand(configShowCmd())
	root.AddCommand(doctorCmd())
	root.AddCommand(historyCmd())
	root.AddCommand(completionCmd())
	root.AddCommand(adminCmd())

	if err := root.Execute(); err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(1)
	}
}
