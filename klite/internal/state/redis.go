// Package state provides state persistence for KLite using Redis
package state

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/klite/klite/internal/config"
	"github.com/klite/klite/pkg/types"
	"github.com/redis/go-redis/v9"
)

const (
	containerKeyPrefix = "klite:containers:"
	networkKeyPrefix   = "klite:networks:"
	serviceKeyPrefix   = "klite:services:"
	manifestKeyPrefix  = "klite:manifests:"
)

// Redis implements the Store interface using Redis as the backend.
// It persists container and network state across CLI invocations.
type Redis struct {
	client *redis.Client
	ctx    context.Context
}

// NewRedis creates a new Redis connection and verifies connectivity.
// If addr is empty, defaults to localhost:6379.
// Returns an error if the connection cannot be established.
func NewRedis(addr string) (*Redis, error) {
	if addr == "" {
		addr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: "",
		DB:       0,
	})

	ctx := context.Background()

	// Verify connection is working
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis at %s: %w", addr, err)
	}

	return &Redis{
		client: client,
		ctx:    ctx,
	}, nil
}

// Close closes the Redis connection and releases resources.
func (r *Redis) Close() error {
	return r.client.Close()
}

// SaveContainer persists a container's state to Redis.
// Key format: klite:containers:{nodeName}/{containerName}
// The state is JSON-encoded before storage.
func (r *Redis) SaveContainer(nodeName, containerName string, state *types.ContainerState) error {
	key := fmt.Sprintf("%s%s/%s", containerKeyPrefix, nodeName, containerName)

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal container state: %w", err)
	}

	return r.client.Set(r.ctx, key, data, 0).Err()
}

// GetContainer retrieves a container's state from Redis.
// Returns nil, nil if the container is not found (not an error).
// Returns the decoded ContainerState on success.
func (r *Redis) GetContainer(nodeName, containerName string) (*types.ContainerState, error) {
	key := fmt.Sprintf("%s%s/%s", containerKeyPrefix, nodeName, containerName)

	data, err := r.client.Get(r.ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get container state: %w", err)
	}

	var state types.ContainerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal container state: %w", err)
	}

	return &state, nil
}

// ListContainers retrieves all container states from Redis.
// Uses KEYS command to find all containers matching klite:containers:*
// Containers that fail to decode are skipped (best-effort).
func (r *Redis) ListContainers() ([]*types.ContainerState, error) {
	pattern := containerKeyPrefix + "*"

	keys, err := r.client.Keys(r.ctx, pattern).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list container keys: %w", err)
	}

	containers := make([]*types.ContainerState, 0, len(keys))
	for _, key := range keys {
		data, err := r.client.Get(r.ctx, key).Bytes()
		if err != nil {
			continue
		}

		var state types.ContainerState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		containers = append(containers, &state)
	}

	return containers, nil
}

// DeleteContainer removes a container's state from Redis.
// Key format: klite:containers:{nodeName}/{containerName}
func (r *Redis) DeleteContainer(nodeName, containerName string) error {
	key := fmt.Sprintf("%s%s/%s", containerKeyPrefix, nodeName, containerName)
	return r.client.Del(r.ctx, key).Err()
}

// SaveNetwork persists a node's network ID to Redis.
// Key format: klite:networks:{nodeName}
// Value is the Docker network ID string.
func (r *Redis) SaveNetwork(nodeName, networkID string) error {
	key := fmt.Sprintf("%s%s", networkKeyPrefix, nodeName)
	return r.client.Set(r.ctx, key, networkID, 0).Err()
}

// GetNetwork retrieves a node's network ID from Redis.
// Returns empty string and nil if not found (not an error).
func (r *Redis) GetNetwork(nodeName string) (string, error) {
	key := fmt.Sprintf("%s%s", networkKeyPrefix, nodeName)

	networkID, err := r.client.Get(r.ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get network: %w", err)
	}

	return networkID, nil
}

// ListNetworks retrieves all node network mappings from Redis.
// Returns a map of nodeName -> networkID.
// Networks that fail to retrieve are skipped (best-effort).
func (r *Redis) ListNetworks() (map[string]string, error) {
	pattern := networkKeyPrefix + "*"

	keys, err := r.client.Keys(r.ctx, pattern).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list network keys: %w", err)
	}

	networks := make(map[string]string)
	for _, key := range keys {
		networkID, err := r.client.Get(r.ctx, key).Result()
		if err != nil {
			continue
		}
		nodeName := key[len(networkKeyPrefix):]
		networks[nodeName] = networkID
	}

	return networks, nil
}

// DeleteNetwork removes a node's network ID from Redis.
// Key format: klite:networks:{nodeName}
func (r *Redis) DeleteNetwork(nodeName string) error {
	key := fmt.Sprintf("%s%s", networkKeyPrefix, nodeName)
	return r.client.Del(r.ctx, key).Err()
}

// SaveService stores a service's state in Redis.
// Key format: klite:services:{serviceName}
func (r *Redis) SaveService(name string, state *types.ServiceState) error {
	key := fmt.Sprintf("%s%s", serviceKeyPrefix, name)

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal service state: %w", err)
	}

	return r.client.Set(r.ctx, key, data, 0).Err()
}

// GetService retrieves a service's state from Redis.
// Returns nil if the service doesn't exist.
func (r *Redis) GetService(name string) (*types.ServiceState, error) {
	key := fmt.Sprintf("%s%s", serviceKeyPrefix, name)

	data, err := r.client.Get(r.ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get service: %w", err)
	}

	var state types.ServiceState
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal service state: %w", err)
	}

	return &state, nil
}

// ListServices retrieves all service states from Redis.
// Services that fail to unmarshal are skipped (best-effort).
func (r *Redis) ListServices() ([]*types.ServiceState, error) {
	pattern := serviceKeyPrefix + "*"

	keys, err := r.client.Keys(r.ctx, pattern).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list service keys: %w", err)
	}

	var services []*types.ServiceState
	for _, key := range keys {
		data, err := r.client.Get(r.ctx, key).Result()
		if err != nil {
			continue
		}

		var state types.ServiceState
		if err := json.Unmarshal([]byte(data), &state); err != nil {
			continue
		}

		services = append(services, &state)
	}

	return services, nil
}

// DeleteService removes a service's state from Redis.
// Key format: klite:services:{serviceName}
func (r *Redis) DeleteService(name string) error {
	key := fmt.Sprintf("%s%s", serviceKeyPrefix, name)
	return r.client.Del(r.ctx, key).Err()
}

// SaveManifestFromFile stores a manifest in Redis from a filepath.
// Key format: klite:manifests:{name}
func (r *Redis) SaveManifestFromFile(name string, manifestPath string) error {
	manifest, err := config.ParseFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}
	return r.SaveManifest(name, manifest)
}

// SaveManifest stores a manifest in Redis.
// Key format: klite:manifests:{name}
func (r *Redis) SaveManifest(name string, manifest *types.KLiteManifest) error {
	key := fmt.Sprintf("%s%s", manifestKeyPrefix, name)

	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	return r.client.Set(r.ctx, key, data, 0).Err()
}

// GetManifest retrieves a manifest from Redis.
// Returns nil if the manifest doesn't exist.
func (r *Redis) GetManifest(name string) (*types.KLiteManifest, error) {
	key := fmt.Sprintf("%s%s", manifestKeyPrefix, name)

	data, err := r.client.Get(r.ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest: %w", err)
	}

	var manifest types.KLiteManifest
	if err := json.Unmarshal([]byte(data), &manifest); err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	return &manifest, nil
}

// DeleteManifest removes a manifest from Redis.
// Key format: klite:manifests:{name}
func (r *Redis) DeleteManifest(name string) error {
	key := fmt.Sprintf("%s%s", manifestKeyPrefix, name)
	return r.client.Del(r.ctx, key).Err()
}