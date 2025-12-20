// Package engine provides reconciliation loop for continuous state management
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/klite/klite/pkg/types"
)

// Reconciler continuously ensures actual state matches desired state
type Reconciler struct {
	engine       *Engine
	interval     time.Duration
	desiredState *types.KLiteManifest
	mu           sync.RWMutex
}

// NewReconciler creates a new reconciler instance
func NewReconciler(eng *Engine, interval time.Duration) *Reconciler {
	return &Reconciler{
		engine:   eng,
		interval: interval,
	}
}

// Run starts the reconciliation loop
func (r *Reconciler) Run(ctx context.Context) error {
	if manifest, err := r.engine.GetPersistedManifest(); err == nil && manifest != nil {
		r.desiredState = manifest
		fmt.Println("✓ Loaded persisted manifest:", manifest.Metadata.Name)
	} else {
		fmt.Println("→ No persisted manifest found, waiting for apply...")
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	fmt.Printf("KLite daemon started. Reconciling every %v\n", r.interval)

	if r.desiredState != nil {
		if err := r.reconcile(ctx); err != nil {
			fmt.Printf("Initial reconciliation error: %v\n", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down reconciler...")
			return nil

		case <-ticker.C:
			if err := r.reconcile(ctx); err != nil {
				fmt.Printf("✗ Reconciliation error: %v\n", err)
			}
		}
	}
}

// reconcile ensures actual state matches desired state
func (r *Reconciler) reconcile(ctx context.Context) error {
	r.mu.RLock()
	desired := r.desiredState
	defer r.mu.RUnlock()

	if desired == nil {
		return nil
	}
	for _, nodeSpec := range desired.Spec.Nodes {
		if _, err := r.reconcileNode(ctx, nodeSpec); err != nil {
			fmt.Printf("  ✗ Node %s reconciliation failed: %v\n", nodeSpec.Name, err)
			continue
		}
	}

	return nil
}

// reconcileNode ensures a single node matches desired state
func (r *Reconciler) reconcileNode(ctx context.Context, desired types.NodeSpec) (bool, error) {
	// Build expected container list (including infrastructure)
	expectedContainers := r.buildExpectedContainers(desired)
	var errs []error
	needReconcile := false
	for _, containerSpec := range expectedContainers {
		existing, err := r.engine.containerManager.GetContainerByName(ctx, desired.Name, containerSpec.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("error getting container: %s",containerSpec.Name))
		}
		if existing == nil {
			fmt.Printf("  + Creating missing container: %s/%s\n", desired.Name, containerSpec.Name)
			needReconcile = true
			continue
		}

		if existing.Status != types.StatusRunning {
			fmt.Printf("  ⟳ Restarting stopped container: %s/%s (status: %s)\n", desired.Name, containerSpec.Name, existing.Status)

			// Actually restart the container
			if err := r.engine.containerManager.StartContainer(ctx, existing.ID); err != nil {
				fmt.Printf("    ✗ Failed to start container: %v\n", err)
				errs = append(errs, fmt.Errorf("failed to start %s: %w", containerSpec.Name, err))
			} else {
				fmt.Printf("    ✓ Container restarted successfully\n")
			}
		}
	}
	if len(errs) > 0 {
		return needReconcile, fmt.Errorf("Reconciled node: %s with %d errors: %v", desired.Name, len(errs), errs)
	}
	return needReconcile, nil
}

// buildExpectedContainers returns all containers that should exist (including infrastructure)
func (r *Reconciler) buildExpectedContainers(node types.NodeSpec) []types.ContainerSpec {
	expected := make([]types.ContainerSpec, 0, len(node.Containers)+2)

	expected = append(expected, types.ContainerSpec{Name: "dnsmasq"})
	expected = append(expected, types.ContainerSpec{Name: "nginx-proxy"})
	expected = append(expected, node.Containers...)

	return expected
}


// SetDesiredState updates the desired state (called by API)
func (r *Reconciler) SetDesiredState(manifest *types.KLiteManifest) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.desiredState = manifest
	if manifest != nil {
		fmt.Printf("✓ Desired state updated: %s\n", manifest.Metadata.Name)
	} else {
		fmt.Println("✓ Desired state cleared")
	}
}

// GetDesiredState returns the current desired state
func (r *Reconciler) GetDesiredState() *types.KLiteManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.desiredState
}

