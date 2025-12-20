// Package config handles parsing and validation of KLite YAML manifests
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/klite/klite/pkg/types"
	"gopkg.in/yaml.v3"
)

// ParseFile reads and parses a YAML manifest from a file path
func ParseFile(path string) (*types.KLiteManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	return Parse(data)
}

// Parse parses YAML data into a KLiteManifest
func Parse(data []byte) (*types.KLiteManifest, error) {
	var manifest types.KLiteManifest

	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if err := validate(&manifest); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return &manifest, nil
}

// validate performs validation on the parsed manifest
func validate(manifest *types.KLiteManifest) error {
	if manifest.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}

	if manifest.Kind == "" {
		return fmt.Errorf("kind is required")
	}

	if manifest.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}

	if len(manifest.Spec.Nodes) == 0 {
		return fmt.Errorf("at least one node is required")
	}

	if err := validateServices(manifest); err != nil {
		return err
	}

	if err := ValidateContainerNames(manifest); err != nil {
		return err
	}

	if err := ValidateNodeNames(manifest); err != nil {
		return err
	}

	return nil
}

// validateServices validates the services section of the manifest
func validateServices(manifest *types.KLiteManifest) error {
	serviceNames := make(map[string]bool)

	for _, svc := range manifest.Spec.Services {
		if svc.Name == "" {
			return fmt.Errorf("service name is required")
		}
		if svc.Target == "" {
			return fmt.Errorf("service %s: target is required", svc.Name)
		}
		if svc.Port <= 0 {
			return fmt.Errorf("service %s: port must be positive", svc.Name)
		}

		if serviceNames[svc.Name] {
			return fmt.Errorf("duplicate service name: %s", svc.Name)
		}
		serviceNames[svc.Name] = true

		parts := strings.Split(svc.Target, "/")
		if len(parts) != 2 {
			return fmt.Errorf("service %s: target must be in 'Node/Container' format, got: %s", svc.Name, svc.Target)
		}
		targetNode := parts[0]
		targetContainer := parts[1]

		if targetNode == "" || targetContainer == "" {
			return fmt.Errorf("service %s: target node and container cannot be empty", svc.Name)
		}

		nodeExists := false
		for _, node := range manifest.Spec.Nodes {
			if node.Name == targetNode {
				nodeExists = true
				containerExists := false
				for _, container := range node.Containers {
					if container.Name == targetContainer {
						containerExists = true
						break
					}
				}
				if !containerExists {
					return fmt.Errorf("service %s: target container %s not found in node %s", svc.Name, targetContainer, targetNode)
				}
				break
			}
		}
		if !nodeExists {
			return fmt.Errorf("service %s: target node %s not found", svc.Name, targetNode)
		}
	}

	return nil
}

// ValidateContainerNames checks for duplicate container names on each node
func ValidateContainerNames(manifest *types.KLiteManifest) error {
	for _, node := range manifest.Spec.Nodes {
		containerNames := make(map[string]bool)
		for _, container := range node.Containers {
			if containerNames[container.Name] {
				return fmt.Errorf("duplicate container name: %s on node: %s", container.Name, node.Name)
			}
		}
	}
	return nil
}

// ValidateNodeNames checks for duplicate node names
func ValidateNodeNames(manifest *types.KLiteManifest) error {
	nodeNames := make(map[string]bool)
	for _, node := range manifest.Spec.Nodes {
		if nodeNames[node.Name] {
			return fmt.Errorf("duplicate node name: %s",node.Name)
		}	
	}
	return nil
}

