package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	manager "go.miloapis.com/email-provider-loops/cmd/manager"
	version "go.miloapis.com/email-provider-loops/cmd/version"
	"go.miloapis.com/email-provider-loops/cmd/webhook"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "email-provider-loops",
		Short: "Loops is the email provider for Milo",
		Long:  "A Kubernetes controller that manages email provider for Milo.",
	}

	rootCmd.AddCommand(manager.CreateManagerCommand())
	rootCmd.AddCommand(version.NewVersionCommand())
	rootCmd.AddCommand(webhook.CreateWebhookCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
