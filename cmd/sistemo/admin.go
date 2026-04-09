package main

import (
	"fmt"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/davidestf/sistemo/internal/db"
)

func adminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Manage dashboard admin account",
	}
	cmd.AddCommand(adminResetPasswordCmd())
	return cmd
}

func adminResetPasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-password",
		Short: "Reset the admin dashboard password",
		Long:  "Reset the admin password for the web dashboard. Does not require the daemon to be running.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir := getDataDirFromCmd(cmd)
			database, err := db.New(dataDir)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			exists, err := db.AdminExists(database)
			if err != nil {
				return fmt.Errorf("check admin: %w", err)
			}
			if !exists {
				return fmt.Errorf("no admin account exists — visit the dashboard to create one")
			}

			var username string
			if err := database.QueryRow("SELECT username FROM admin_user LIMIT 1").Scan(&username); err != nil {
				return fmt.Errorf("lookup admin username: %w", err)
			}

			fmt.Printf("Resetting password for: %s\n", username)
			fmt.Print("New password (min 8 chars): ")
			pw, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			if len(pw) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}
			if len(pw) > 72 {
				return fmt.Errorf("password must be at most 72 characters (bcrypt limit)")
			}

			fmt.Print("Confirm password: ")
			pw2, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return fmt.Errorf("read confirmation: %w", err)
			}
			if string(pw) != string(pw2) {
				return fmt.Errorf("passwords do not match")
			}

			if err := db.ResetAdminPassword(database, username, string(pw)); err != nil {
				return fmt.Errorf("reset password: %w", err)
			}
			fmt.Println("Password reset successfully.")
			return nil
		},
	}
}

// getDataDirFromCmd is defined in root.go
