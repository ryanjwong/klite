package state

import (
	"github.com/klite/klite/pkg/types"
)

// Store defines the interface for KLite state persistence
type Store interface {
	SaveContainer(nodeName, containerName string, state *types.ContainerState) error
	GetContainer(nodeName, containerName string) (*types.ContainerState, error)
	ListContainers() ([]*types.ContainerState, error)
	DeleteContainer(nodeName, containerName string) error

	SaveNetwork(nodeName, networkID string) error
	GetNetwork(nodeName string) (string, error)
	ListNetworks() (map[string]string, error)
	DeleteNetwork(nodeName string) error

	SaveService(name string, state *types.ServiceState) error
	GetService(name string) (*types.ServiceState, error)
	ListServices() ([]*types.ServiceState, error)
	DeleteService(name string) error

	SaveManifestFromFile(name string, manifestPath string) error
	SaveManifest(name string, manifest *types.KLiteManifest) error
	GetManifest(name string) (*types.KLiteManifest, error)
	DeleteManifest(name string) error

	Close() error
}
