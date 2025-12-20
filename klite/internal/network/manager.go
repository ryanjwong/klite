// Package network manages Docker networking and DNS for inter-container communication
package network

import (
	"context"
	"fmt"
	"maps"

	networktypes "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

const (
	// BaseSubnetPrefix is the base for generating per-node subnets
	// Each node gets 172.28.X.0/24 where X is based on node index
	BaseSubnetPrefix = "172.28"
)

// Manager handles Docker network operations
type Manager struct {
	client       *client.Client
	nodeNetworks map[string]string // nodeName -> networkID
	nodeSubnets  map[string]string // nodeName -> subnet (e.g., "172.28.1.0/24")
	subnetIndex  int               // Counter for generating unique subnets
}

// NewManager creates a new network manager
func NewManager() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &Manager{
		client:       cli,
		nodeNetworks: make(map[string]string),
		nodeSubnets:  make(map[string]string),
		subnetIndex:  1,
	}, nil
}

// Close closes the Docker client connection
func (m *Manager) Close() error {
	return m.client.Close()
}

// EnsureNetworkForNode creates a network for a specific node if it doesn't exist
func (m *Manager) EnsureNetworkForNode(ctx context.Context, nodeName string) (string, error) {
	if networkID, ok := m.nodeNetworks[nodeName]; ok {
		return networkID, nil
	}

	networkName := m.GetNetworkNameForNode(nodeName)

	networks, err := m.client.NetworkList(ctx, networktypes.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list networks: %w", err)
	}

	for _, n := range networks {
		if n.Name == networkName {
			m.nodeNetworks[nodeName] = n.ID
			if len(n.IPAM.Config) > 0 {
				m.nodeSubnets[nodeName] = n.IPAM.Config[0].Subnet
			}
			return n.ID, nil
		}
	}

	networkID, err := m.CreateNetworkForNode(ctx, nodeName)
	if err != nil {
		return "", err
	}

	return networkID, nil
}

// CreateNetworkForNode creates a new Docker network for a specific node
func (m *Manager) CreateNetworkForNode(ctx context.Context, nodeName string) (string, error) {
	networkName := m.GetNetworkNameForNode(nodeName)
	subnet, gateway := m.generateSubnet()

	ipamConfig := []networktypes.IPAMConfig{
		{
			Subnet:  subnet,
			Gateway: gateway,
		},
	}

	opt := networktypes.CreateOptions{
		Driver: "bridge",
		IPAM: &networktypes.IPAM{
			Config: ipamConfig,
		},
		Labels: map[string]string{
			"klite.managed": "true",
			"klite.node":    nodeName,
		},
	}

	resp, err := m.client.NetworkCreate(ctx, networkName, opt)
	if err != nil {
		return "", fmt.Errorf("failed to create network for node %s: %w", nodeName, err)
	}

	m.nodeNetworks[nodeName] = resp.ID
	m.nodeSubnets[nodeName] = subnet
	return resp.ID, nil
}

// RemoveNetworkForNode removes the network for a specific node
func (m *Manager) RemoveNetworkForNode(ctx context.Context, nodeName string) error {
	networkID, ok := m.nodeNetworks[nodeName]
	if !ok {
		return nil
	}

	if err := m.client.NetworkRemove(ctx, networkID); err != nil {
		return fmt.Errorf("failed to remove network for node %s: %w", nodeName, err)
	}

	delete(m.nodeNetworks, nodeName)
	return nil
}

// GetNetworkNameForNode returns the network name for a specific node
func (m *Manager) GetNetworkNameForNode(nodeName string) string {
	return fmt.Sprintf("klite-%s-network", nodeName)
}

// GetNetworkIDForNode returns the network ID for a specific node
func (m *Manager) GetNetworkIDForNode(nodeName string) (string, bool) {
	id, ok := m.nodeNetworks[nodeName]
	return id, ok
}

// GetNodeSubnet returns the subnet for a specific node
func (m *Manager) GetNodeSubnet(nodeName string) (string, bool) {
	subnet, ok := m.nodeSubnets[nodeName]
	return subnet, ok
}

// generateSubnet generates a unique subnet for each node
func (m *Manager) generateSubnet() (subnet string, gateway string) {
	subnet = fmt.Sprintf("%s.%d.0/24", BaseSubnetPrefix, m.subnetIndex)
	gateway = fmt.Sprintf("%s.%d.1", BaseSubnetPrefix, m.subnetIndex)
	m.subnetIndex++
	return subnet, gateway
}

// ConnectContainer connects a container to a node's network
func (m *Manager) ConnectContainer(ctx context.Context, nodeName, containerID string, aliases []string) error {
	networkID, ok := m.nodeNetworks[nodeName]
	if !ok {
		return fmt.Errorf("network for node %s not found", nodeName)
	}

	endpointConfig := &networktypes.EndpointSettings{
		Aliases: aliases,
	}
	return m.client.NetworkConnect(ctx, networkID, containerID, endpointConfig)
}

// DisconnectContainer disconnects a container from a node's network
func (m *Manager) DisconnectContainer(ctx context.Context, nodeName, containerID string) error {
	networkID, ok := m.nodeNetworks[nodeName]
	if !ok {
		return nil
	}
	return m.client.NetworkDisconnect(ctx, networkID, containerID, false)
}

// ListNodeNetworks returns all managed node networks
func (m *Manager) ListNodeNetworks() map[string]string {
	result := make(map[string]string)
	maps.Copy(result, m.nodeNetworks)
	return result
}
