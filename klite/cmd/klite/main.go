// Package main is the entry point for the KLite CLI
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/klite/klite/internal/config"
	"github.com/klite/klite/internal/engine"
	"github.com/klite/klite/internal/web"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	redisAddr = "localhost:6379"
	proxyConfigDir = "/klite-config"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "klite",
	Short: "KLite - A lightweight Kubernetes-like container orchestration engine",
	Long: `KLite is a lightweight container orchestration engine that allows you to:
  - Define containers and their relationships in YAML
  - Enable inter-container communication via DNS
  - Route external traffic through nginx proxy
  
Example:
  klite apply -f deployment.yaml
  klite status
  klite logs Mom/A`,
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a KLite manifest",
	Long:  "Apply a KLite manifest to create or update containers and routing",
	RunE:  runApply,
}

var deleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete resources from a KLite manifest",
	Long:  "Delete all resources defined in a KLite manifest",
	RunE:  runDelete,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of KLite resources",
	Long:  "Display the current status of all managed containers and the proxy",
	RunE:  runStatus,
}

var logsCmd = &cobra.Command{
	Use:   "logs [node/container]",
	Short: "View container logs",
	Long:  "View logs from a specific container. Use format: node/container (e.g., Mom/A)",
	Args:  cobra.ExactArgs(1),
	RunE:  runLogs,
}

var execCmd = &cobra.Command{
	Use:   "exec",
	Short: "Execute a command in a container",
	Long:  "Execute a command in a running container. Example: klite exec -n Frontend -c test-client -e \"curl api\"",
	RunE:  runExec,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start web UI server",
	Long:  "Start the KLite web dashboard for managing deployments",
	RunE:  runServe,
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start KLite daemon with reconciliation loop",
	Long:  "Start KLite as a daemon process that continuously reconciles desired state with actual state. Includes web UI for management.",
	RunE:  runDaemon,
}

var dnsCmd = &cobra.Command{
	Use:   "dns [node/container]",
	Short: "Debug DNS configuration for a container",
	Long:  "Test DNS resolution and show DNS configuration for a specific container. Example: klite dns Frontend/webapp",
	Args:  cobra.ExactArgs(1),
	RunE:  runDNS,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("KLite version %s (commit: %s)\n", version, commit)
	},
}

// Flags
var (
	manifestFile      string
	followLogs        bool
	webAddr           string
	execNode          string
	execContainer     string
	execCommand       string
	daemonInterval    time.Duration
	daemonWebAddr     string
)

func init() {
	// Apply command flags
	applyCmd.Flags().StringVarP(&manifestFile, "file", "f", "", "Path to the manifest file (required)")
	applyCmd.MarkFlagRequired("file")

	// Delete command flags
	deleteCmd.Flags().StringVarP(&manifestFile, "file", "f", "", "Path to the manifest file (required)")
	deleteCmd.MarkFlagRequired("file")

	// Logs command flags
	logsCmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "Follow log output")

	// Exec command flags
	execCmd.Flags().StringVarP(&execNode, "node", "n", "", "Node name (required)")
	execCmd.MarkFlagRequired("node")
	execCmd.Flags().StringVarP(&execContainer, "container", "c", "", "Container name (required)")
	execCmd.MarkFlagRequired("container")
	execCmd.Flags().StringVarP(&execCommand, "exec", "e", "", "Command to execute (required)")
	execCmd.MarkFlagRequired("exec")

	// Serve command flags
	serveCmd.Flags().StringVar(&webAddr, "addr", "localhost:8000", "Web server address")

	// Daemon command flags
	daemonCmd.Flags().DurationVar(&daemonInterval, "interval", 10*time.Second, "Reconciliation interval")
	daemonCmd.Flags().StringVar(&daemonWebAddr, "addr", "localhost:8000", "Web server address")

	// Add commands
	rootCmd.AddCommand(applyCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(execCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(dnsCmd)
	rootCmd.AddCommand(versionCmd)
}

func createEngine() (*engine.Engine, error) {
	cfg := engine.Config{
		ProxyConfigDir: proxyConfigDir,
		RedisAddr:      redisAddr,
	}

	return engine.NewEngine(cfg)
}

func runApply(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt
	setupSignalHandler(cancel)

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	fmt.Printf("Applying manifest: %s\n", manifestFile)
	manifest, err := config.ParseFile(manifestFile)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}
	if err := eng.Apply(ctx, manifest); err != nil {
		return fmt.Errorf("failed to apply manifest: %w", err)
	}

	fmt.Println("✓ Manifest applied successfully")
	return nil
}

func runDelete(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setupSignalHandler(cancel)

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	fmt.Printf("Deleting resources from manifest: %s\n", manifestFile)
	manifest, err := config.ParseFile(manifestFile)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}
	if err := eng.Delete(ctx, manifest); err != nil {
		return fmt.Errorf("failed to delete resources: %w", err)
	}

	fmt.Println("✓ Resources deleted successfully")
	return nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	status, err := eng.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	fmt.Println("KLite Status")
	fmt.Println("============")
	fmt.Printf("Active Nodes: %d\n", len(status.NodeNetworks))
	fmt.Printf("Total Containers: %d\n", status.TotalContainers)
	fmt.Println()

	if len(status.NodeNetworks) > 0 {
		fmt.Println("Node Networks:")
		fmt.Printf("%-15s %-40s\n", "NODE", "NETWORK ID")
		fmt.Println("-------------------------------------------------------")
		for nodeName, networkID := range status.NodeNetworks {
			fmt.Printf("%-15s %-40s\n", nodeName, networkID)
		}
		fmt.Println()
	}

	if len(status.Containers) > 0 {
		fmt.Println("Containers:")
		fmt.Printf("%-25s %-10s %-10s %-15s %-18s\n", "NAME", "NODE", "STATUS", "INTERNAL IP", "ACCESS")
		fmt.Println("------------------------------------------------------------------------------------")
		for _, c := range status.Containers {
			access := c.AccessAddress
			if access == "" {
				access = "-"
			}
			fmt.Printf("%-25s %-10s %-10s %-15s %-18s\n", c.Name, c.NodeName, c.Status, c.IPAddress, access)
		}
		fmt.Println()
	}

	return nil
}

func runLogs(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setupSignalHandler(cancel)

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	nodeName, containerName := parseContainerRef(args[0])

	reader, err := eng.Logs(ctx, nodeName, containerName, followLogs)
	if err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
	}
	defer reader.Close()

	if _, err := io.Copy(os.Stdout, reader); err != nil {
		return fmt.Errorf("failed to stream logs: %w", err)
	}

	return nil
}

func runExec(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setupSignalHandler(cancel)

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	// Simple split by spaces - for complex commands, user can quote the string
	commandArgs := strings.Fields(execCommand)
	if len(commandArgs) == 0 {
		return fmt.Errorf("command cannot be empty")
	}

	output, err := eng.Exec(ctx, execNode, execContainer, commandArgs)
	if err != nil {
		return fmt.Errorf("failed to execute command: %w", err)
	}

	fmt.Print(output)
	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	server := web.NewServer(eng, webAddr)
	fmt.Printf("Starting KLite web server on %s\n", webAddr)
	return server.Start()
}

func runDaemon(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setupSignalHandler(cancel)

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	// Create reconciler
	reconciler := engine.NewReconciler(eng, daemonInterval)

	server := web.NewServer(eng, daemonWebAddr)
	server.SetReconciler(reconciler)
	go func() {
		fmt.Printf("Starting KLite web server on %s\n", daemonWebAddr)
		if err := server.Start(); err != nil {
			fmt.Printf("Web server error: %v\n", err)
		}
	}()

	fmt.Printf("Starting KLite daemon (interval: %v)\n", daemonInterval)
	if err := reconciler.Run(ctx); err != nil {
		return fmt.Errorf("reconciler error: %w", err)
	}

	return nil
}

func runDNS(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	eng, err := createEngine()
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	defer eng.Close()

	// Parse node/container from args[0]
	parts := strings.Split(args[0], "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid format, use: node/container (e.g., Frontend/webapp)")
	}
	nodeName, containerName := parts[0], parts[1]

	fmt.Printf("DNS Debug for %s/%s\n", nodeName, containerName)
	fmt.Println(strings.Repeat("=", 50))

	// 1. Show DNS server configuration
	if err := showDNSConfig(ctx, eng, nodeName, containerName); err != nil {
		fmt.Printf("\n✗ Failed to get DNS config: %v\n", err)
	}

	// 2. Test resolution of services
	if err := testServiceResolution(ctx, eng, nodeName, containerName); err != nil {
		fmt.Printf("\n✗ Failed to test service resolution: %v\n", err)
	}

	// 3. Show dnsmasq configuration
	if err := showDnsmasqConfig(nodeName); err != nil {
		fmt.Printf("\n✗ Failed to get dnsmasq config: %v\n", err)
	}

	return nil
}

func showDNSConfig(ctx context.Context, eng *engine.Engine, nodeName, containerName string) error {
	fmt.Println("\n1. DNS Configuration (/etc/resolv.conf)")
	fmt.Println("   " + strings.Repeat("-", 40))

	// Exec into container to check /etc/resolv.conf
	output, err := eng.Exec(ctx, nodeName, containerName, []string{"cat", "/etc/resolv.conf"})
	if err != nil {
		return fmt.Errorf("failed to read resolv.conf: %w", err)
	}

	// Indent output for readability
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		fmt.Printf("   %s\n", line)
	}

	return nil
}

func testServiceResolution(ctx context.Context, eng *engine.Engine, nodeName, containerName string) error {
	fmt.Println("\n2. Service Resolution Tests")
	fmt.Println("   " + strings.Repeat("-", 40))

	// Get all services from state store
	services, err := eng.GetServices(ctx)
	if err != nil {
		return err
	}

	if len(services) == 0 {
		fmt.Println("   No services configured")
		return nil
	}

	// Test each service DNS resolution
	for _, svc := range services {
		fmt.Printf("\n   Testing service: %s\n", svc.Name)

		output, err := eng.Exec(ctx, nodeName, containerName, []string{"getent", "hosts", svc.Name})

		if err != nil {
			fmt.Printf("   ✗ Failed to resolve: %v\n", err)
			continue
		}

		// Show resolved info
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				fmt.Printf("   %s\n", line)
			}
		}
	}

	return nil
}

func showDnsmasqConfig(nodeName string) error {
	fmt.Println("\n3. dnsmasq Configuration & Status")
	fmt.Println("   " + strings.Repeat("-", 40))

	// Check if dnsmasq container is running
	eng, _ := createEngine()
	if eng != nil {
		defer eng.Close()

		dnsmasqState, found := eng.GetContainer(nodeName, "dnsmasq")
		if !found {
			fmt.Printf("   ✗ dnsmasq container not found\n")
		} else if dnsmasqState.Status != "Running" {
			fmt.Printf("   ✗ dnsmasq is %s (not running!)\n", dnsmasqState.Status)
		} else {
			fmt.Printf("   ✓ dnsmasq is running (IP: %s)\n", dnsmasqState.IPAddress)
		}
	}


	return nil
}

func parseContainerRef(ref string) (nodeName, containerName string) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		fmt.Fprintf(os.Stderr, "Invalid format, use: node/container (e.g., Mom/A)\n")
		os.Exit(1)
	}
	return parts[0], parts[1]
}

func setupSignalHandler(cancel context.CancelFunc) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nReceived interrupt signal, shutting down...")
		cancel()
	}()
}
