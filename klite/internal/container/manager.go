// Package container manages Docker container lifecycle
package container

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	networktypes "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/klite/klite/pkg/types"
)

// Manager handles Docker container operations
type Manager struct {
	client *client.Client
}

// NewManager creates a new container manager
func NewManager() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &Manager{
		client: cli,
	}, nil
}

// Close closes the Docker client connection
func (m *Manager) Close() error {
	return m.client.Close()
}

// CreateContainer creates a new container from the spec
// networkName specifies which Docker network to attach the container to
// dnsServers specifies custom DNS servers (e.g., ["172.28.1.254"] for dnsmasq)
// staticIP specifies a static IP address for infrastructure containers (empty for dynamic)
func (m *Manager) CreateContainer(ctx context.Context, nodeName string, spec types.ContainerSpec, networkName string, dnsServers []string, staticIP string) (*types.ContainerState, error) {
	containerName := m.buildContainerName(nodeName, spec.Name)

	if err := m.PullImage(ctx, spec.Image); err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}

	exposedPorts, portBindings := m.buildPortBindings(spec.Ports)

	config := &containertypes.Config{
		Image:        spec.Image,
		Env:          m.buildEnvVars(spec.Env),
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			"klite.managed":        "true",
			"klite.node":           nodeName,
			"klite.container.name": spec.Name,
		},
	}

	if len(spec.Command) > 0 {
		config.Cmd = spec.Command
	}

	hostConfig := &containertypes.HostConfig{
		PortBindings: portBindings,
		Binds:        m.buildVolumeMounts(spec.Volumes),
		DNS:          dnsServers,
		ExtraHosts:   spec.ExtraHosts,
	}

	endpointSettings := &networktypes.EndpointSettings{
		Aliases: m.buildNetworkAliases(nodeName, spec.Name),
	}

	// Set static IP if specified (for infrastructure containers like dnsmasq/nginx)
	if staticIP != "" {
		endpointSettings.IPAMConfig = &networktypes.EndpointIPAMConfig{
			IPv4Address: staticIP,
		}
	}

	networkingConfig := &networktypes.NetworkingConfig{
		EndpointsConfig: map[string]*networktypes.EndpointSettings{
			networkName: endpointSettings,
		},
	}

	resp, err := m.client.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	return &types.ContainerState{
		ID:       resp.ID,
		Name:     containerName,
		NodeName: nodeName,
		Status:   types.StatusPending,
	}, nil
}

// StartContainer starts an existing container
func (m *Manager) StartContainer(ctx context.Context, containerID string) error {
	return m.client.ContainerStart(ctx, containerID, containertypes.StartOptions{})
}

// StopContainer stops a running container
func (m *Manager) StopContainer(ctx context.Context, containerID string) error {
	timeout := 10
	return m.client.ContainerStop(ctx, containerID, containertypes.StopOptions{
		Timeout: &timeout, // # of seconds
	})
}

// RemoveContainer removes a container
func (m *Manager) RemoveContainer(ctx context.Context, containerID string) error {
	return m.client.ContainerRemove(ctx, containerID, containertypes.RemoveOptions{Force: true})
}

// GetContainerByName retrieves a container by its klite name (node/container)
// Returns nil, nil if container doesn't exist
func (m *Manager) GetContainerByName(ctx context.Context, nodeName, containerName string) (*types.ContainerState, error) {
	fullName := m.buildContainerName(nodeName, containerName)

	resp, err := m.client.ContainerInspect(ctx, fullName)
	if err != nil {
		if client.IsErrNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	return m.containerInspectToState(&resp), nil
}

// GetContainerName returns the Docker container name for a given node and container name
func (m *Manager) GetContainerName(nodeName, containerName string) string {
	return m.buildContainerName(nodeName, containerName)
}

// GetContainerState retrieves the current state of a container
func (m *Manager) GetContainerState(ctx context.Context, containerID string) (*types.ContainerState, error) {
	resp, err := m.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get container state: %w", err)
	}

	return m.containerInspectToState(&resp), nil
}

// StreamContainerLogs returns the Docker container logs for a given container
// If follow is true, the logs will be streamed continuously
func (m *Manager) StreamContainerLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	reader, err := m.client.ContainerLogs(ctx, containerID, containertypes.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: false,
	})
	if err != nil {
		return nil, fmt.Errorf("error reading container logs: %w", err)
	}

	return reader, nil
}

// containerInspectToState converts Docker inspect response to ContainerState
func (m *Manager) containerInspectToState(resp *dockertypes.ContainerJSON) *types.ContainerState {
	ipAddress := resp.NetworkSettings.IPAddress
	for _, network := range resp.NetworkSettings.Networks {
		if network.IPAddress != "" {
			ipAddress = network.IPAddress
			break
		}
	}

	ports := extractPorts(resp.NetworkSettings.Ports)
	if len(ports) == 0 {
		ports = extractPorts(resp.HostConfig.PortBindings)
	}
	accessAddress := buildAccessAddress(ports)

	return &types.ContainerState{
		ID:            resp.ID,
		Name:          strings.TrimPrefix(resp.Name, "/"),
		NodeName:      resp.Config.Labels["klite.node"],
		IPAddress:     ipAddress,
		AccessAddress: accessAddress,
		Status:        types.ConvertDockerStatus(resp.State.Status),
		Ports:         ports,
	}
}

// buildAccessAddress creates the localhost accessible address from port mappings
func buildAccessAddress(ports []types.PortMapping) string {
	for _, p := range ports {
		if p.HostPort > 0 {
			return fmt.Sprintf("localhost:%d", p.HostPort)
		}
	}
	return ""
}

// buildContainerName creates a unique container name
func (m *Manager) buildContainerName(nodeName, containerName string) string {
	return fmt.Sprintf("klite-%s-%s", nodeName, containerName)
}

// buildEnvVars converts env map to Docker env format
func (m *Manager) buildEnvVars(env map[string]string) []string {
	vars := make([]string, 0, len(env))
	for k, v := range env {
		vars = append(vars, fmt.Sprintf("%s=%s", k, v))
	}
	return vars
}

// buildNetworkAliases creates DNS aliases for container name resolution
// This allows containers to reach each other by name
func (m *Manager) buildNetworkAliases(nodeName, containerName string) []string {
	return []string{
		containerName,
		fmt.Sprintf("%s.%s", containerName, nodeName),       // Node qualified: "A.Mom"
		fmt.Sprintf("%s.%s.klite", containerName, nodeName), // Fully qualified: "A.Mom.klite"
	}
}

// buildPortBindings converts PortMapping slice to Docker's nat.PortMap
func (m *Manager) buildPortBindings(ports []types.PortMapping) (nat.PortSet, nat.PortMap) {
	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}

	for _, p := range ports {
		protocol := p.Protocol
		if protocol == "" {
			protocol = "tcp"
		}

		containerPort, _ := nat.NewPort(protocol, fmt.Sprintf("%d", p.ContainerPort))

		exposedPorts[containerPort] = struct{}{}

		// Add to port bindings (host config) - only if HostPort is specified
		if p.HostPort > 0 {
			portBindings[containerPort] = []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", p.HostPort),
				},
			}
		}
	}

	return exposedPorts, portBindings
}

// extractPorts converts Docker's nat.PortMap back to []types.PortMapping
func extractPorts(portMap nat.PortMap) []types.PortMapping {
	var ports []types.PortMapping

	for containerPort, bindings := range portMap {
		port := types.PortMapping{
			ContainerPort: containerPort.Int(),
			Protocol:      containerPort.Proto(),
		}

		if len(bindings) > 0 && bindings[0].HostPort != "" {
			hostPort, _ := strconv.Atoi(bindings[0].HostPort)
			port.HostPort = hostPort
		}

		ports = append(ports, port)
	}

	return ports
}

// buildVolumeMounts converts VolumeMount slice to a string slice
func (m *Manager) buildVolumeMounts(volumes []types.VolumeMount) []string {
	binds := make([]string, len(volumes))
	for i, v := range volumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		binds[i] = fmt.Sprintf("%s:%s:%s", v.HostPath, v.ContainerPath, mode)
	}
	return binds
}

// PullImage pulls a Docker image
func (m *Manager) PullImage(ctx context.Context, imageName string) error {
	reader, err := m.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}

	defer reader.Close()
	io.Copy(io.Discard, reader)
	return nil
}

// ExecInContainer executes a command in a running container
func (m *Manager) ExecInContainer(ctx context.Context, containerID string, cmd []string) (string, error) {
	execConfig := containertypes.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}

	execID, err := m.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec instance: %w", err)
	}

	attachResp, err := m.client.ContainerExecAttach(ctx, execID.ID, containertypes.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to attach to exec instance: %w", err)
	}
	defer attachResp.Close()

	output, err := io.ReadAll(attachResp.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to read exec output: %w", err)
	}

	inspectResp, err := m.client.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return string(output), fmt.Errorf("failed to inspect exec instance: %w", err)
	}

	if inspectResp.ExitCode != 0 {
		return string(output), fmt.Errorf("command exited with code %d", inspectResp.ExitCode)
	}

	return string(output), nil
}

// SpecMatches compares a ContainerSpec with actual running container state
// Returns true if the container matches the spec, false if it needs to be recreated
func (m *Manager) SpecMatches(ctx context.Context, spec types.ContainerSpec, state *types.ContainerState) (bool, error) {
	resp, err := m.client.ContainerInspect(ctx, state.ID)
	if err != nil {
		return false, fmt.Errorf("failed to inspect container: %w", err)
	}

	if !imageMatches(spec.Image, resp.Config.Image) {
		return false, nil
	}

	if !portsMatch(spec.Ports, resp.HostConfig.PortBindings) {
		return false, nil
	}

	if !envMatches(spec.Env, resp.Config.Env) {
		return false, nil
	}

	if !volumesMatch(spec.Volumes, resp.HostConfig.Binds) {
		return false, nil
	}

	if len(spec.Command) > 0 && !commandMatches(spec.Command, resp.Config.Cmd) {
		return false, nil
	}

	return true, nil
}

// imageMatches checks if spec image matches running container image
func imageMatches(specImage, containerImage string) bool {
	return specImage == containerImage ||
		   strings.HasPrefix(containerImage, specImage+"@sha256:")
}

// portsMatch checks if port bindings match
func portsMatch(specPorts []types.PortMapping, containerPorts nat.PortMap) bool {
	// Build map of expected port bindings (only ports with HostPort > 0)
	expectedBindings := make(map[string]int)
	for _, p := range specPorts {
		if p.HostPort > 0 {
			protocol := p.Protocol
			if protocol == "" {
				protocol = "tcp"
			}
			key := fmt.Sprintf("%d/%s", p.ContainerPort, protocol)
			expectedBindings[key] = p.HostPort
		}
	}

	// If no expected bindings and no actual bindings, match
	if len(expectedBindings) == 0 && len(containerPorts) == 0 {
		return true
	}

	// Check all actual bindings match expected
	for portProto, bindings := range containerPorts {
		expectedHostPort, exists := expectedBindings[string(portProto)]
		if !exists {
			// Container has a binding that's not in spec
			return false
		}

		if len(bindings) == 0 {
			return false
		}
		actualHostPort, _ := strconv.Atoi(bindings[0].HostPort)
		if actualHostPort != expectedHostPort {
			return false
		}
		// Delete the matching binding
		delete(expectedBindings, string(portProto))
	}

	// Check all expected bindings exist in actual
	return len(expectedBindings) == 0
}

// envMatches checks if environment variables match
func envMatches(specEnv map[string]string, containerEnv []string) bool {
	// Convert container env array to map
	actualEnv := make(map[string]string)
	for _, e := range containerEnv {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			actualEnv[parts[0]] = parts[1]
		}
	}

	// Check all spec env vars exist and match
	for key, expectedVal := range specEnv {
		actualVal, exists := actualEnv[key]
		if !exists || actualVal != expectedVal {
			return false
		}
	}

	return true
}

// volumesMatch checks if volume mounts match
func volumesMatch(specVolumes []types.VolumeMount, containerBinds []string) bool {
	if len(specVolumes) == 0 && len(containerBinds) == 0 {
		return true
	}

	// Build expected binds from spec
	expectedBinds := make(map[string]bool)
	for _, v := range specVolumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		bind := fmt.Sprintf("%s:%s:%s", v.HostPath, v.ContainerPath, mode)
		expectedBinds[bind] = true
	}

	// Compare with actual binds
	if len(expectedBinds) != len(containerBinds) {
		return false
	}

	for _, bind := range containerBinds {
		if !expectedBinds[bind] {
			return false
		}
	}

	return true
}

// commandMatches checks if command matches
func commandMatches(specCmd, containerCmd []string) bool {
	if len(specCmd) != len(containerCmd) {
		return false
	}
	for i, cmd := range specCmd {
		if cmd != containerCmd[i] {
			return false
		}
	}
	return true
}
