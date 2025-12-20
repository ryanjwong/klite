// Package dns manages DNS configuration for service discovery via dnsmasq
package dns

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DnsmasqContainerName is the name of the dnsmasq container
	DnsmasqContainerName = "dnsmasq"
	// DnsmasqImage is the Docker image for dnsmasq
	DnsmasqImage = "jpillora/dnsmasq:latest"
	// DnsmasqIP is the last octet for dnsmasq static IP (e.g., 172.28.1.254)
	DnsmasqIP = ".254"
	// NginxServiceIP is the last octet for nginx static IP (e.g., 172.28.1.253)
	NginxServiceIP = ".253"
)

// Manager handles dnsmasq configuration and lifecycle
type Manager struct {
	configDir string
}

// ServiceMapping represents a service name to IP mapping for DNS
type ServiceMapping struct {
	ServiceName string // e.g., "api"
	TargetIP    string // nginx IP: "172.28.1.253"
	Port        int    // Service port (for reference, not used in DNS)
}

// NewManager creates a new DNS manager
func NewManager(configDir string) (*Manager, error) {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create DNS config directory: %w", err)
	}
	return &Manager{configDir: configDir}, nil
}

// GenerateHostsFile creates a dnsmasq hosts file mapping service names to nginx proxy IP
// Returns the path to the hosts file
func (m *Manager) GenerateHostsFile(nodeName string, nodeSubnet string, services []ServiceMapping) (string, error) {
	nodeConfigDir := filepath.Join(m.configDir, nodeName)
	if err := os.MkdirAll(nodeConfigDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create node DNS config directory: %w", err)
	}

	hostsPath := filepath.Join(nodeConfigDir, "dnsmasq.hosts")
	f, err := os.Create(hostsPath)
	if err != nil {
		return "", fmt.Errorf("failed to create hosts file: %w", err)
	}
	defer f.Close()

	// Extract network prefix (e.g., "172.28.1" from "172.28.1.0/24")
	prefix := nodeSubnet[:len(nodeSubnet)-len(".0/24")]
	nginxIP := prefix + NginxServiceIP

	fmt.Fprintf(f, "# KLite DNS Service Mappings for node: %s\n", nodeName)
	fmt.Fprintf(f, "# Auto-generated - Do not edit manually\n\n")

	// All service names resolve to nginx proxy IP
	for _, svc := range services {
		// Format: IP_ADDRESS SERVICE_NAME
		// e.g., 172.28.1.253 api
		fmt.Fprintf(f, "%s\t%s\n", nginxIP, svc.ServiceName)
	}

	return hostsPath, nil
}

// GenerateConfig creates a dnsmasq.conf file with upstream DNS fallback
// Returns the path to the config file
func (m *Manager) GenerateConfig(nodeName string) (string, error) {
	nodeConfigDir := filepath.Join(m.configDir, nodeName)
	if err := os.MkdirAll(nodeConfigDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create node DNS config directory: %w", err)
	}

	confPath := filepath.Join(nodeConfigDir, "dnsmasq.conf")
	f, err := os.Create(confPath)
	if err != nil {
		return "", fmt.Errorf("failed to create dnsmasq config: %w", err)
	}
	defer f.Close()

	// Configuration:
	// - Read hosts from custom file
	// - Forward non-matching queries to Docker DNS (127.0.0.11)
	// - No caching for container resolution freshness
	// - Log queries for debugging
	fmt.Fprintf(f, `# KLite dnsmasq configuration for node: %s
# Auto-generated - Do not edit manually

# Don't read /etc/resolv.conf
no-resolv

# No DNS caching (containers may move)
cache-size=0

# Forward unknown queries to Docker's embedded DNS
server=127.0.0.11

# Read additional hosts from our custom file
addn-hosts=/etc/dnsmasq.d/dnsmasq.hosts

# Enable query logging for debugging
log-queries
log-facility=-
`, nodeName)

	return confPath, nil
}

// GetDnsmasqIP returns the full IP address for dnsmasq in a given subnet
// Example: GetDnsmasqIP("172.28.1.0/24") returns "172.28.1.254"
func GetDnsmasqIP(nodeSubnet string) string {
	prefix := nodeSubnet[:len(nodeSubnet)-len(".0/24")]
	return prefix + DnsmasqIP
}

// GetNginxIP returns the full IP address for nginx in a given subnet
// Example: GetNginxIP("172.28.1.0/24") returns "172.28.1.253"
func GetNginxIP(nodeSubnet string) string {
	prefix := nodeSubnet[:len(nodeSubnet)-len(".0/24")]
	return prefix + NginxServiceIP
}
