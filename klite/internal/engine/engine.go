// Package engine orchestrates all KLite components
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/klite/klite/internal/container"
	"github.com/klite/klite/internal/dns"
	"github.com/klite/klite/internal/network"
	"github.com/klite/klite/internal/proxy"
	"github.com/klite/klite/internal/state"
	"github.com/klite/klite/pkg/types"
)

// Engine is the main orchestrator for KLite
type Engine struct {
	containerManager *container.Manager
	networkManager   *network.Manager
	proxyManager     *proxy.Manager
	dnsManager       *dns.Manager
	stateStore       state.Store
	activeManifest   *types.KLiteManifest
	mu           sync.RWMutex
}

// Config holds engine configuration
type Config struct {
	ProxyConfigDir string
	RedisAddr      string
}

// NewEngine creates a new KLite engine
func NewEngine(cfg Config) (*Engine, error) {

	home, err := os.UserHomeDir()
	cfg.ProxyConfigDir = filepath.Join(home, cfg.ProxyConfigDir)
	netMgr, err := network.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create network manager: %w", err)
	}

	containerMgr, err := container.NewManager()
	if err != nil {
		netMgr.Close()
		return nil, fmt.Errorf("failed to create container manager: %w", err)
	}

	proxyMgr, err := proxy.NewManager(cfg.ProxyConfigDir)
	if err != nil {
		containerMgr.Close()
		netMgr.Close()
		return nil, fmt.Errorf("failed to create proxy manager: %w", err)
	}

	dnsMgr, err := dns.NewManager(filepath.Join(cfg.ProxyConfigDir, "dns"))
	if err != nil {
		containerMgr.Close()
		netMgr.Close()
		return nil, fmt.Errorf("failed to create DNS manager: %w", err)
	}

	engine := &Engine{
		containerManager: containerMgr,
		networkManager:   netMgr,
		proxyManager:     proxyMgr,
		dnsManager:       dnsMgr,
	}

	redisStore, err := state.NewRedis(cfg.RedisAddr)
	if err != nil {
		engine.Close()
		return nil, fmt.Errorf("fatal error: failed to connect to Redis: %w", err)
	}
	engine.stateStore = redisStore

	return engine, nil
}

// Close cleans up engine resources
func (e *Engine) Close() error {
	if e.stateStore != nil {
		e.stateStore.Close()
	}
	e.containerManager.Close()
	e.networkManager.Close()
	return nil
}

// Apply applies a manifest configuration
// Additionally it builds the routing mapping service names to the nginx server, then configures the nginx server to route services to external nodes
func (e *Engine) Apply(ctx context.Context, manifest *types.KLiteManifest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	

	serviceRoutes := e.buildServiceRoutingTable(manifest)

	var errs []error
	for _, node := range manifest.Spec.Nodes {
		networkID, err := e.networkManager.EnsureNetworkForNode(ctx, node.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to ensure network for node %s: %w", node.Name, err))
			continue
		}

		if err := e.stateStore.SaveNetwork(node.Name, networkID); err != nil {
			errs = append(errs, fmt.Errorf("failed to save network state for node %s: %w", node.Name, err))
		}

		subnet, ok := e.networkManager.GetNodeSubnet(node.Name)
		if !ok {
			errs = append(errs, fmt.Errorf("failed to get subnet for node %s", node.Name))
			continue
		}

		serviceMappings := e.buildServiceMappings(manifest)
		hostsPath, err := e.dnsManager.GenerateHostsFile(node.Name, subnet, serviceMappings)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to generate DNS hosts for node %s: %w", node.Name, err))
			continue
		}

		nodeServiceRoutes := serviceRoutes[node.Name]

		nginxConfigDir, err := e.proxyManager.GenerateConfigForNode(node.Name, node.Containers, nodeServiceRoutes)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to generate nginx config for node %s: %w", node.Name, err))
			continue
		}

		networkName := e.networkManager.GetNetworkNameForNode(node.Name)

		containersToApply := e.injectRoutingContainers(node, hostsPath, nginxConfigDir, nodeServiceRoutes)

		dnsServers := []string{dns.GetDnsmasqIP(subnet)}

		for _, containerSpec := range containersToApply {
			existing, err := e.containerManager.GetContainerByName(ctx, node.Name, containerSpec.Name)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to check container %s.%s: %w", node.Name, containerSpec.Name, err))
				continue
			}

			var containerState *types.ContainerState
			needsRecreate := false

			if existing != nil {
				if containerSpec.Name == proxy.NginxContainerName {
					needsRecreate = true
					fmt.Printf("⟳ Recreating nginx-proxy to apply config changes...\n")
				} else if existing.Status != types.StatusRunning {
					needsRecreate = true
					fmt.Printf("⟳ Container %s.%s not running, recreating...\n", node.Name, containerSpec.Name)
				} else {
					matches, err := e.containerManager.SpecMatches(ctx, containerSpec, existing)
					if err != nil {
						errs = append(errs, fmt.Errorf("failed to compare spec for %s.%s: %w", node.Name, containerSpec.Name, err))
						continue
					}

					if matches {
						containerState = existing
						fmt.Printf("✓ Container %s.%s matches spec, no changes needed\n", node.Name, containerSpec.Name)
					} else {
						needsRecreate = true
						fmt.Printf("⟳ Container %s.%s spec changed, recreating...\n", node.Name, containerSpec.Name)
					}
				}

				if needsRecreate {
					if err := e.containerManager.StopContainer(ctx, existing.ID); err != nil {
						fmt.Printf("  Warning: failed to stop %s.%s: %v\n", node.Name, containerSpec.Name, err)
					}
					if err := e.containerManager.RemoveContainer(ctx, existing.ID); err != nil {
						errs = append(errs, fmt.Errorf("failed to remove %s.%s: %w", node.Name, containerSpec.Name, err))
						continue
					}
				}
			} else {
				fmt.Printf("+ Creating container %s.%s...\n", node.Name, containerSpec.Name)
			}

			if existing == nil || needsRecreate {
				var containerDNS []string
				var staticIP string
				
				// Ensure DNS and Nginx get static IPs
				// Nginx and application containers should use our custom DNS
				switch containerSpec.Name {
				case dns.DnsmasqContainerName:
					staticIP = dns.GetDnsmasqIP(subnet)
					// dnsmasq uses default Docker DNS (no custom DNS)
				case proxy.NginxContainerName:
					staticIP = dns.GetNginxIP(subnet)
					containerDNS = dnsServers
				default:
					containerDNS = dnsServers
				}

				containerState, err = e.containerManager.CreateContainer(ctx, node.Name, containerSpec, networkName, containerDNS, staticIP)
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to create container %s.%s: %w", node.Name, containerSpec.Name, err))
					continue
				}

				if err := e.containerManager.StartContainer(ctx, containerState.ID); err != nil {
					errs = append(errs, fmt.Errorf("failed to start container %s.%s: %w", node.Name, containerSpec.Name, err))
					continue
				}

				fullState, err := e.containerManager.GetContainerState(ctx, containerState.ID)
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to get state for %s.%s: %w", node.Name, containerSpec.Name, err))
					fullState = containerState
				}
				fullState.NodeName = node.Name
				containerState = fullState

				fmt.Printf("  ✓ Container %s.%s started successfully\n", node.Name, containerSpec.Name)
			}

			if err := e.stateStore.SaveContainer(node.Name, containerSpec.Name, containerState); err != nil {
				errs = append(errs, fmt.Errorf("failed to save container state %s.%s: %w", node.Name, containerSpec.Name, err))
			}
		}
	}

	for _, svc := range manifest.Spec.Services {
		// Parse target (format: "Node/Container")
		parts := strings.Split(svc.Target, "/")
		if len(parts) != 2 {
			continue 
		}

		serviceState := &types.ServiceState{
			Name:            svc.Name,
			TargetNode:      parts[0],
			TargetContainer: parts[1],
			Port:            svc.Port,
			DNSName:         svc.Name,
		}

		if err := e.stateStore.SaveService(svc.Name, serviceState); err != nil {
			errs = append(errs, fmt.Errorf("failed to save service state %s: %w", svc.Name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("applied manifest with %d errors: %v", len(errs), errs)
	}

	if err := e.stateStore.SaveManifest("active", manifest); err != nil {
		return fmt.Errorf("failed to save manifest: %w", err)
	}

	e.activeManifest = manifest
	return nil
}

// Delete removes all resources from a manifest
func (e *Engine) Delete(ctx context.Context, manifest *types.KLiteManifest) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var errs []error
	for _, node := range manifest.Spec.Nodes {
		var containersToDelete []string

		// Add routing containers
		containersToDelete = append(containersToDelete,
			e.containerManager.GetContainerName(node.Name, dns.DnsmasqContainerName),
			e.containerManager.GetContainerName(node.Name, proxy.NginxContainerName),
		)

		// Add user containers
		for _, containerSpec := range node.Containers {
			containersToDelete = append(containersToDelete,
				e.containerManager.GetContainerName(node.Name, containerSpec.Name),
			)
		}

		for _, containerName := range containersToDelete {
			if err := e.containerManager.StopContainer(ctx, containerName); err != nil {
				errs = append(errs, fmt.Errorf("failed to stop %s: %w", containerName, err))
			}
			if err := e.containerManager.RemoveContainer(ctx, containerName); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove %s: %w", containerName, err))
			}
		}

		if err := e.stateStore.DeleteContainer(node.Name, dns.DnsmasqContainerName); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete dnsmasq state: %w", err))
		}
		if err := e.stateStore.DeleteContainer(node.Name, proxy.NginxContainerName); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete nginx state: %w", err))
		}
		for _, containerSpec := range node.Containers {
			if err := e.stateStore.DeleteContainer(node.Name, containerSpec.Name); err != nil {
				errs = append(errs, fmt.Errorf("failed to delete container state %s: %w", containerSpec.Name, err))
			}
		}

		for _, svc := range manifest.Spec.Services {
			if err := e.stateStore.DeleteService(svc.Name); err != nil {
				errs = append(errs, fmt.Errorf("failed to delete service state %s: %w", svc.Name, err))
			}
		}

		if err := e.networkManager.RemoveNetworkForNode(ctx, node.Name); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove network for node %s: %w", node.Name, err))
		}
		if err := e.stateStore.DeleteNetwork(node.Name); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete network state for node %s: %w", node.Name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("deleted resources with %d errors: %v", len(errs), errs)
	}
	if err := e.stateStore.DeleteManifest("active"); err != nil {
		return fmt.Errorf("failed to delete manifest: %w", err)
	}

	return nil
}

// Status returns the current status of the engine
func (e *Engine) Status(ctx context.Context) (*Status, error) {
	status := &Status{
		NodeNetworks: make(map[string]string),
		Containers:   make([]*types.ContainerState, 0),
	}

	if e.stateStore != nil {
		containers, err := e.stateStore.ListContainers()
		if err != nil {
			return nil, fmt.Errorf("failed to list containers from store: %w", err)
		}
		status.Containers = containers

		networks, err := e.stateStore.ListNetworks()
		if err != nil {
			return nil, fmt.Errorf("failed to list networks from store: %w", err)
		}
		status.NodeNetworks = networks
	} else {
		status.NodeNetworks = e.networkManager.ListNodeNetworks()
	}

	status.TotalContainers = len(status.Containers)
	status.ActiveNodes = len(status.NodeNetworks)

	return status, nil
}

// Status represents the overall engine status
type Status struct {
	NodeNetworks    map[string]string
	Containers      []*types.ContainerState
	ActiveNodes     int
	TotalContainers int
}

// GetServices returns all services from the state store
func (e *Engine) GetServices(ctx context.Context) ([]*types.ServiceState, error) {
	if e.stateStore == nil {
		return nil, fmt.Errorf("state store not available")
	}
	return e.stateStore.ListServices()
}

// GetPersistedManifest retrieves the persisted manifest from state store
func (e *Engine) GetPersistedManifest() (*types.KLiteManifest, error) {
	if e.stateStore == nil {
		return nil, fmt.Errorf("state store not available")
	}
	return e.stateStore.GetManifest("active")
}


// SaveManifestFromFile saves a manifest to persistent storage from filepath
func (e *Engine) SaveManifestFromFile(manifestPath string) error {
	if e.stateStore == nil {
		return fmt.Errorf("state store not available")
	}
	return e.stateStore.SaveManifestFromFile("active", manifestPath)
}

// SaveManifest saves a manifest to persistent storage
func (e *Engine) SaveManifest(manifest *types.KLiteManifest) error {
	if e.stateStore == nil {
		return fmt.Errorf("state store not available")
	}
	return e.stateStore.SaveManifest("active", manifest)
}

// DeletePersistedManifest removes the persisted manifest
func (e *Engine) DeletePersistedManifest() error {
	if e.stateStore == nil {
		return fmt.Errorf("state store not available")
	}
	return e.stateStore.DeleteManifest("active")
}

// GetContainer returns the state of a specific container
func (e *Engine) GetContainer(nodeName, containerName string) (*types.ContainerState, bool) {
	container, err := e.stateStore.GetContainer(nodeName, containerName)
	if err == nil && container != nil {
		return container, true
	}
	return nil, false
}

// Logs retrieves logs from a container
func (e *Engine) Logs(ctx context.Context, nodeName, containerName string, follow bool) (io.ReadCloser, error) {
	state, ok := e.GetContainer(nodeName, containerName)
	if !ok {
		return nil, fmt.Errorf("container %s.%s not found", nodeName, containerName)
	}

	return e.containerManager.StreamContainerLogs(ctx, state.ID, follow)
}

// Exec executes a command in a container
func (e *Engine) Exec(ctx context.Context, nodeName, containerName string, cmd []string) (string, error) {
	state, ok := e.GetContainer(nodeName, containerName)
	if !ok {
		return "", fmt.Errorf("container %s.%s not found", nodeName, containerName)
	}

	return e.containerManager.ExecInContainer(ctx, state.ID, cmd)
}

// injectRoutingContainers injects dnsmasq and nginx infrastructure containers
func (e *Engine) injectRoutingContainers(node types.NodeSpec, hostsPath string, nginxConfigDir string, serviceRoutes []proxy.ServiceRoute) []types.ContainerSpec {
	var containers []types.ContainerSpec

	dnsmasqSpec := types.ContainerSpec{
		Name:  dns.DnsmasqContainerName,
		Image: dns.DnsmasqImage,
		Volumes: []types.VolumeMount{
			{
				HostPath:      filepath.Dir(hostsPath),
				ContainerPath: "/etc/dnsmasq.d",
				ReadOnly:      true,
			},
		},
		Command: []string{
			"--no-daemon",
			"--log-queries",
			"--server=127.0.0.11",
			"--addn-hosts=/etc/dnsmasq.d/dnsmasq.hosts",
			"--cache-size=0",
		},
	}

	nginxPorts := []types.PortMapping{
		{
			ContainerPort: proxy.DefaultNginxPort,
			HostPort:      node.ProxyPort,
		},
	}

	// Expose all local service ports so remote nodes can reach them via host.docker.internal
	// Use port offset to avoid conflicts (base proxyPort + 10000 + service port)
	portOffset := node.ProxyPort + 10000
	for _, route := range serviceRoutes {
		if route.IsLocalService {
			nginxPorts = append(nginxPorts, types.PortMapping{
				ContainerPort: route.Port,
				HostPort:      portOffset + route.Port,
			})
		}
	}

	nginxSpec := types.ContainerSpec{
		Name:  proxy.NginxContainerName,
		Image: proxy.NginxImage,
		Ports: nginxPorts,
		Volumes: []types.VolumeMount{
			{
				HostPath:      nginxConfigDir,
				ContainerPath: "/etc/nginx/klite",
				ReadOnly:      true,
			},
		},
		Command: []string{"nginx", "-c", "/etc/nginx/klite/nginx.conf", "-g", "daemon off;"},
		ExtraHosts: []string{"host.docker.internal:host-gateway"}, // Enable host.docker.internal resolution
	}

	// Order: dnsmasq first, then user containers, then nginx last
	// dnsmasq must start first so containers can use it for DNS
	// nginx must start last so it can resolve application container DNS names
	containers = append(containers, dnsmasqSpec)
	containers = append(containers, node.Containers...)
	containers = append(containers, nginxSpec)

	return containers
}

// buildServiceRoutingTable creates service route entries for all nodes
func (e *Engine) buildServiceRoutingTable(manifest *types.KLiteManifest) map[string][]proxy.ServiceRoute {
	routesByNode := make(map[string][]proxy.ServiceRoute)

	nodeProxyPorts := make(map[string]int)
	for _, node := range manifest.Spec.Nodes {
		nodeProxyPorts[node.Name] = node.ProxyPort
	}

	for _, svc := range manifest.Spec.Services {
		parts := strings.Split(svc.Target, "/")
		if len(parts) != 2 {
			continue
		}
		targetNode := parts[0]
		targetContainer := parts[1]

		// Add this service route to ALL nodes
		for _, node := range manifest.Spec.Nodes {
			// Target node's nginx exposes service on: targetNodeProxyPort + 10000 + servicePort
			targetNodeProxyPort := nodeProxyPorts[targetNode] + 10000 + svc.Port

			route := proxy.ServiceRoute{
				ServiceName:         svc.Name,
				Port:                svc.Port,
				TargetNode:          targetNode,
				TargetContainer:     targetContainer,
				TargetPort:          svc.Port,
				IsLocalService:      node.Name == targetNode,
				TargetNodeProxyPort: targetNodeProxyPort,
			}
			routesByNode[node.Name] = append(routesByNode[node.Name], route)
		}
	}

	return routesByNode
}

// buildServiceMappings creates DNS mappings for all services
func (e *Engine) buildServiceMappings(manifest *types.KLiteManifest) []dns.ServiceMapping {
	var mappings []dns.ServiceMapping

	for _, svc := range manifest.Spec.Services {
		mappings = append(mappings, dns.ServiceMapping{
			ServiceName: svc.Name,
			Port: svc.Port,
		})
	}

	return mappings
}
