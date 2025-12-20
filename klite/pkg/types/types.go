// Package types defines the core data structures for KLite engine
package types

// KLiteManifest is the root configuration structure parsed from YAML
type KLiteManifest struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

// Metadata contains manifest metadata
type Metadata struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

// Spec defines the desired state of the deployment
type Spec struct {
	Services []ServiceSpec `yaml:"services,omitempty"`
	Nodes    []NodeSpec    `yaml:"nodes"`
}

// ServiceSpec defines a Kubernetes-like service abstraction
type ServiceSpec struct {
	Name   string `yaml:"name"`   // e.g., "api" (simple name, no prefix)
	Target string `yaml:"target"` // e.g., "Dad/D" (Node/Container format)
	Port   int    `yaml:"port"`   // e.g., 5678
}

// NodeSpec defines a node (logical grouping of containers)
type NodeSpec struct {
	Name       string          `yaml:"name"`
	ProxyPort  int             `yaml:"proxyPort"`
	Containers []ContainerSpec `yaml:"containers"`
}

// ContainerSpec defines a container within a node
type ContainerSpec struct {
	Name       string            `yaml:"name"`  // e.g., "A", "B", "C"
	Image      string            `yaml:"image"` // Docker image
	Ports      []PortMapping     `yaml:"ports,omitempty"`
	Env        map[string]string `yaml:"env,omitempty"`
	Command    []string          `yaml:"command,omitempty"`
	Args       []string          `yaml:"args,omitempty"`
	Expose     *ExposeConfig     `yaml:"expose,omitempty"`  // For nginx routing
	Volumes    []VolumeMount     `yaml:"volumes,omitempty"`
	ExtraHosts []string          `yaml:"extra_hosts,omitempty"` // Additional /etc/hosts entries
}

// PortMapping defines port mappings for a container
type PortMapping struct {
	ContainerPort int    `yaml:"containerPort"`
	HostPort      int    `yaml:"hostPort,omitempty"`
	Protocol      string `yaml:"protocol,omitempty"` // tcp/udp, defaults to tcp
}

// ExposeConfig defines how a container is exposed via nginx
type ExposeConfig struct {
	Host string `yaml:"host"` 
	Path string `yaml:"path"` // e.g., "/" or "/api"
	Port int    `yaml:"port"` // Internal port to route to
}

// VolumeMount defines a volume mount for a container
type VolumeMount struct {
	HostPath      string `yaml:"hostPath"`
	ContainerPath string `yaml:"containerPath"`
	ReadOnly      bool   `yaml:"readOnly,omitempty"`
}

// ContainerState represents the runtime state of a container
type ContainerState struct {
	ID            string
	Name          string
	NodeName      string
	IPAddress     string
	AccessAddress string
	Status        ContainerStatus
	Ports         []PortMapping
}

// ServiceState represents the runtime state of a service
type ServiceState struct {
	Name            string
	TargetNode      string
	TargetContainer string
	Port            int
	DNSName         string 
}

// ContainerStatus represents the lifecycle status of a container
type ContainerStatus string

const (
	StatusPending ContainerStatus = "Pending"
	StatusRunning ContainerStatus = "Running"
	StatusStopped ContainerStatus = "Stopped"
	StatusFailed  ContainerStatus = "Failed"
	StatusUnknown ContainerStatus = "Unknown"
)

// ConvertDockerStatus maps Docker state to ContainerStatus
func ConvertDockerStatus(dockerState string) ContainerStatus {
	switch dockerState {
	case "running":
		return StatusRunning
	case "exited", "dead":
		return StatusStopped
	case "created", "restarting":
		return StatusPending
	default:
		return StatusUnknown
	}
}