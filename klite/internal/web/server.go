// Package web provides HTTP API and web UI for KLite
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/klite/klite/internal/config"
	"github.com/klite/klite/internal/engine"
)

//go:embed static/*
var staticFiles embed.FS

// Server provides HTTP API and web interface
type Server struct {
	engine      *engine.Engine
	addr        string
	reconciler  *engine.Reconciler
}

// NewServer creates a new web server
func NewServer(eng *engine.Engine, addr string) *Server {
	return &Server{
		engine: eng,
		addr:   addr,
	}
}

// SetReconciler sets the reconciler for daemon mode
func (s *Server) SetReconciler(r *engine.Reconciler) {
	s.reconciler = r
}

// Start starts the HTTP server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/apply", s.handleApply)
	mux.HandleFunc("/api/delete", s.handleDelete)
	mux.HandleFunc("/api/nodes", s.handleNodes)

	// Static files (web UI)
	mux.Handle("/", http.FileServer(http.FS(staticFiles)))

	fmt.Printf("Web UI available at http://%s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

// handleStatus returns current cluster status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	status, err := s.engine.Status(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleNodes returns detailed node information
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	status, err := s.engine.Status(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Group containers by node
	nodeMap := make(map[string][]interface{})
	for _, container := range status.Containers {
		nodeMap[container.NodeName] = append(nodeMap[container.NodeName], container)
	}

	// Get services from state store
	var services []interface{}
	if s.engine != nil {
		ctx := context.Background()
		if serviceList, err := s.engine.GetServices(ctx); err == nil {
			for _, svc := range serviceList {
				services = append(services, svc)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes":           nodeMap,
		"networks":        status.NodeNetworks,
		"nodeCount":       status.ActiveNodes,
		"totalContainers": status.TotalContainers,
		"services":        services,
	})
}

// handleApply applies a manifest
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read uploaded YAML file
	file, header, err := r.FormFile("manifest")
	if err != nil {
		http.Error(w, "Failed to read manifest file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save to temp file
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, header.Filename)

	out, err := os.Create(tmpFile)
	if err != nil {
		http.Error(w, "Failed to save manifest", http.StatusInternalServerError)
		return
	}
	defer out.Close()
	defer os.Remove(tmpFile)

	_, err = io.Copy(out, file)
	if err != nil {
		http.Error(w, "Failed to write manifest", http.StatusInternalServerError)
		return
	}

	// Apply manifest
	
	err = s.engine.SaveManifestFromFile(tmpFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save manifest: %v", err), http.StatusInternalServerError)
		return	
	}
	manifest, err := config.ParseFile(tmpFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error parsing manifest: %v", err), http.StatusInternalServerError)
	}
	ctx := context.Background()
	err = s.engine.Apply(ctx, manifest)
	if err != nil {
		http.Error(w, fmt.Sprintf("Apply failed: %v", err), http.StatusInternalServerError)
		return
	}

	if s.reconciler != nil {
		manifest, err := s.engine.GetPersistedManifest()
		if err == nil && manifest != nil {
			s.reconciler.SetDesiredState(manifest)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
		"message": "Manifest applied successfully",
	})
}

// handleDelete deletes resources from a manifest
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read uploaded YAML file
	file, header, err := r.FormFile("manifest")
	if err != nil {
		http.Error(w, "Failed to read manifest file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save to temp file
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, header.Filename)

	out, err := os.Create(tmpFile)
	if err != nil {
		http.Error(w, "Failed to save manifest", http.StatusInternalServerError)
		return
	}
	defer out.Close()
	defer os.Remove(tmpFile)

	_, err = io.Copy(out, file)
	if err != nil {
		http.Error(w, "Failed to write manifest", http.StatusInternalServerError)
		return
	}

	// Delete resources
	manifest, err := config.ParseFile(tmpFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error parsing manifest: %v", err), http.StatusInternalServerError)
	}
	ctx := context.Background()
	// If running in daemon mode, clear reconciler's desired state
	if s.reconciler != nil {
		s.reconciler.SetDesiredState(nil)
	}
	err = s.engine.Delete(ctx, manifest)
	if err != nil {
		http.Error(w, fmt.Sprintf("Delete failed: %v", err), http.StatusInternalServerError)
		return
	}

	

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
		"message": "Resources deleted successfully",
	})
}
