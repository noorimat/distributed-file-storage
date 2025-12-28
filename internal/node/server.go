package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// StorageNode represents a single storage node in the cluster
type StorageNode struct {
	NodeID           string
	Address          string
	StoragePath      string
	CoordinatorAddr  string
	chunks           map[string]bool // Track which chunks this node has
	chunksLock       sync.RWMutex
	server           *http.Server
}

// NewStorageNode creates a new storage node
func NewStorageNode(nodeID, address, storagePath, coordinatorAddr string) *StorageNode {
	return &StorageNode{
		NodeID:          nodeID,
		Address:         address,
		StoragePath:     storagePath,
		CoordinatorAddr: coordinatorAddr,
		chunks:          make(map[string]bool),
	}
}

// Start starts the storage node HTTP server
func (sn *StorageNode) Start() error {
	// Create storage directory
	if err := os.MkdirAll(sn.StoragePath, 0755); err != nil {
		return fmt.Errorf("failed to create storage directory: %w", err)
	}

	// Load existing chunks
	if err := sn.loadExistingChunks(); err != nil {
		return fmt.Errorf("failed to load existing chunks: %w", err)
	}

	// Set up HTTP routes
	router := mux.NewRouter()
	router.HandleFunc("/health", sn.healthHandler).Methods("GET")
	router.HandleFunc("/store", sn.storeChunkHandler).Methods("POST")
	router.HandleFunc("/retrieve/{hash}", sn.retrieveChunkHandler).Methods("GET")
	router.HandleFunc("/chunks", sn.listChunksHandler).Methods("GET")

	sn.server = &http.Server{
		Addr:    sn.Address,
		Handler: router,
	}

	// Register with coordinator
	go sn.registerWithCoordinator()

	// Start heartbeat
	go sn.startHeartbeat()

	log.Printf("Storage Node %s starting on %s", sn.NodeID, sn.Address)
	return sn.server.ListenAndServe()
}

// healthHandler returns the health status of this node
func (sn *StorageNode) healthHandler(w http.ResponseWriter, r *http.Request) {
	sn.chunksLock.RLock()
	chunkCount := len(sn.chunks)
	sn.chunksLock.RUnlock()

	response := map[string]interface{}{
		"status":       "healthy",
		"node_id":      sn.NodeID,
		"address":      sn.Address,
		"total_chunks": chunkCount,
		"timestamp":    time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// storeChunkHandler handles storing a chunk on this node
func (sn *StorageNode) storeChunkHandler(w http.ResponseWriter, r *http.Request) {
	var req StoreChunkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Store chunk to disk
	chunkPath := filepath.Join(sn.StoragePath, req.ChunkHash[:2], req.ChunkHash)
	
	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(chunkPath), 0755); err != nil {
		log.Printf("Failed to create chunk directory: %v", err)
		http.Error(w, "Failed to store chunk", http.StatusInternalServerError)
		return
	}

	// Write chunk data
	if err := os.WriteFile(chunkPath, req.ChunkData, 0644); err != nil {
		log.Printf("Failed to write chunk: %v", err)
		http.Error(w, "Failed to store chunk", http.StatusInternalServerError)
		return
	}

	// Track chunk
	sn.chunksLock.Lock()
	sn.chunks[req.ChunkHash] = true
	sn.chunksLock.Unlock()

	log.Printf("Stored chunk %s on node %s", req.ChunkHash[:8], sn.NodeID)

	response := StoreChunkResponse{
		Success:   true,
		NodeID:    sn.NodeID,
		ChunkHash: req.ChunkHash,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// retrieveChunkHandler handles retrieving a chunk from this node
func (sn *StorageNode) retrieveChunkHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	chunkHash := vars["hash"]

	// Check if chunk exists
	sn.chunksLock.RLock()
	exists := sn.chunks[chunkHash]
	sn.chunksLock.RUnlock()

	if !exists {
		http.Error(w, "Chunk not found", http.StatusNotFound)
		return
	}

	// Read chunk from disk
	chunkPath := filepath.Join(sn.StoragePath, chunkHash[:2], chunkHash)
	chunkData, err := os.ReadFile(chunkPath)
	if err != nil {
		log.Printf("Failed to read chunk: %v", err)
		http.Error(w, "Failed to retrieve chunk", http.StatusInternalServerError)
		return
	}

	response := RetrieveChunkResponse{
		Success:   true,
		ChunkHash: chunkHash,
		ChunkData: chunkData,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// listChunksHandler returns all chunks stored on this node
func (sn *StorageNode) listChunksHandler(w http.ResponseWriter, r *http.Request) {
	sn.chunksLock.RLock()
	chunks := make([]string, 0, len(sn.chunks))
	for hash := range sn.chunks {
		chunks = append(chunks, hash)
	}
	sn.chunksLock.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"node_id": sn.NodeID,
		"count":   len(chunks),
		"chunks":  chunks,
	})
}

// registerWithCoordinator registers this node with the coordinator
func (sn *StorageNode) registerWithCoordinator() {
	if sn.CoordinatorAddr == "" {
		return
	}

	url := fmt.Sprintf("http://%s/register", sn.CoordinatorAddr)
	
	nodeInfo := NodeInfo{
		NodeID:  sn.NodeID,
		Address: sn.Address,
		Status:  "healthy",
	}

	data, _ := json.Marshal(nodeInfo)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("Failed to register with coordinator: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("Successfully registered with coordinator")
	}
}

// startHeartbeat sends periodic heartbeats to the coordinator
func (sn *StorageNode) startHeartbeat() {
	if sn.CoordinatorAddr == "" {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		sn.chunksLock.RLock()
		chunkCount := len(sn.chunks)
		sn.chunksLock.RUnlock()

		url := fmt.Sprintf("http://%s/heartbeat", sn.CoordinatorAddr)
		
		heartbeat := HeartbeatMessage{
			NodeID:      sn.NodeID,
			Address:     sn.Address,
			TotalChunks: chunkCount,
			Timestamp:   time.Now(),
		}

		data, _ := json.Marshal(heartbeat)
		_, err := http.Post(url, "application/json", bytes.NewReader(data))
		if err != nil {
			log.Printf("Failed to send heartbeat: %v", err)
		}
	}
}

// loadExistingChunks scans the storage directory and loads chunk hashes
func (sn *StorageNode) loadExistingChunks() error {
	return filepath.Walk(sn.StoragePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && len(info.Name()) == 64 {
			// Looks like a chunk hash (SHA-256 is 64 hex chars)
			sn.chunksLock.Lock()
			sn.chunks[info.Name()] = true
			sn.chunksLock.Unlock()
		}

		return nil
	})
}