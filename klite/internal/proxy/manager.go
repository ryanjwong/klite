// Package proxy manages the nginx reverse proxy for routing external traffic
package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/klite/klite/pkg/types"
)

const (
	// NginxContainerName is the name of the nginx proxy container
	NginxContainerName = "nginx-proxy"
	// NginxImage is the Docker image for nginx
	NginxImage = "nginx:alpine"
	// DefaultNginxPort is the default HTTP port
	DefaultNginxPort = 80
)

// Manager handles nginx proxy configuration and lifecycle
type Manager struct {
	configDir string
}

// RouteConfig represents a routing configuration for nginx (HTTP expose routes)
type RouteConfig struct {
	Host          string 
	Path          string // e.g., "/" or "/api"
	UpstreamName  string // Container DNS name
	UpstreamPort  int    // Container port
	ContainerName string // For reference
	NodeName      string // For reference
}

// ServiceRoute represents internal service routing (TCP stream proxying)
type ServiceRoute struct {
	ServiceName         string // e.g., "api"
	Port                int    // Service port (e.g., 5678)
	TargetNode          string // Target node name
	TargetContainer     string // Target container name
	TargetPort          int    // Target container port
	IsLocalService      bool   // true if target is on same node
	TargetNodeProxyPort int    // For remote routing via nginx
}

// NewManager creates a new proxy manager
func NewManager(configDir string) (*Manager, error) {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	return &Manager{
		configDir: configDir,
	}, nil
}

// GenerateConfigForNode creates nginx.conf for a specific node
// Returns the path to the config directory (for volume mounting)
func (m *Manager) GenerateConfigForNode(nodeName string, containers []types.ContainerSpec, services []ServiceRoute) (string, error) {
	nodeConfigDir := filepath.Join(m.configDir, nodeName)
	if err := os.MkdirAll(nodeConfigDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create node config directory: %w", err)
	}

	configPath := filepath.Join(nodeConfigDir, "nginx.conf")

	tmpl, err := template.New("nginx").Parse(nginxConfigTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	f, err := os.Create(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to create config file: %w", err)
	}
	defer f.Close()

	routes := m.buildRoutesForNode(nodeName, containers)

	data := struct {
		Routes        []RouteConfig
		ServiceRoutes []ServiceRoute
	}{
		Routes:        routes,
		ServiceRoutes: services,
	}

	if err := tmpl.Execute(f, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return nodeConfigDir, nil
}

// buildRoutesForNode extracts routes from containers with Expose config
func (m *Manager) buildRoutesForNode(nodeName string, containers []types.ContainerSpec) []RouteConfig {
	var routes []RouteConfig

	for _, container := range containers {
		if container.Expose != nil {
			routes = append(routes, RouteConfig{
				Host:          container.Expose.Host,
				Path:          container.Expose.Path,
				UpstreamName:  fmt.Sprintf("%s.%s.klite", container.Name, nodeName),
				UpstreamPort:  container.Expose.Port,
				ContainerName: container.Name,
				NodeName:      nodeName,
			})
		}
	}

	return routes
}

// nginx configuration template
const nginxConfigTemplate = `
# KLite Nginx Proxy Configuration
# Auto-generated - Do not edit manually

worker_processes auto;

events {
    worker_connections 1024;
}

# Stream module for service-based TCP proxying
stream {
    {{range .ServiceRoutes}}
    # Service: {{.ServiceName}} -> {{.TargetContainer}}.{{.TargetNode}}
    upstream {{.ServiceName}}_backend {
        {{if .IsLocalService}}
        # Local service: direct connection via Docker DNS
        server {{.TargetContainer}}.{{.TargetNode}}.klite:{{.TargetPort}};
        {{else}}
        # Remote service: proxy through target node's nginx
        server host.docker.internal:{{.TargetNodeProxyPort}};
        {{end}}
    }

    server {
        listen {{.Port}};
        proxy_pass {{.ServiceName}}_backend;
    }
    {{end}}
}

# HTTP module for external expose-based routing
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;

    log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                    '$status $body_bytes_sent "$http_referer" '
                    '"$http_user_agent" "$http_x_forwarded_for"';

    access_log /var/log/nginx/access.log main;
    error_log  /var/log/nginx/error.log warn;

    sendfile        on;
    keepalive_timeout  65;

    {{range .Routes}}
    # Route: {{.ContainerName}} on {{.NodeName}}
    upstream {{.ContainerName}}_{{.NodeName}}_upstream {
        server {{.UpstreamName}}:{{.UpstreamPort}};
    }
    {{end}}

    server {
        listen 80;
        server_name _;

        {{range .Routes}}
        # Route: {{.Host}}{{.Path}} -> {{.ContainerName}}
        location {{.Path}} {
            proxy_pass http://{{.ContainerName}}_{{.NodeName}}_upstream/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
        {{end}}
    }
}
`
