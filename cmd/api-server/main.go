package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/noorimat/distributed-file-storage/internal/chunking"
	"github.com/noorimat/distributed-file-storage/internal/dedup"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	StoragePath = "./storage"
)

// Global chunk store for deduplication
var chunkStore *dedup.ChunkStore

type UploadResponse struct {
	FileID       string   `json:"file_id"`
	FileName     string   `json:"file_name"`
	Size         int64    `json:"size"`
	ChunkHashes  []string `json:"chunk_hashes"`  // Now using hashes instead of IDs
	ChunksStored int      `json:"chunks_stored"` // How many were actually stored (vs deduplicated)
	DedupRatio   float64  `json:"dedup_ratio"`   // Deduplication ratio for this file
}

type FileMetadata struct {
	FileID      string   `json:"file_id"`
	FileName    string   `json:"file_name"`
	Size        int64    `json:"size"`
	ChunkHashes []string `json:"chunk_hashes"`
	Uploaded    string   `json:"uploaded"`
}

func main() {
	// Create storage directory
	if err := os.MkdirAll(StoragePath, 0755); err != nil {
		log.Fatal("Failed to create storage directory:", err)
	}

	// Initialize chunk store for deduplication
	var err error
	chunkStore, err = dedup.NewChunkStore(StoragePath)
	if err != nil {
		log.Fatal("Failed to initialize chunk store:", err)
	}

	router := mux.NewRouter()

	// Routes
	router.HandleFunc("/health", healthHandler).Methods("GET")
	router.HandleFunc("/upload", uploadHandler).Methods("POST")
	router.HandleFunc("/download/{fileID}", downloadHandler).Methods("GET")
	router.HandleFunc("/files", listFilesHandler).Methods("GET")
	router.HandleFunc("/stats", statsHandler).Methods("GET") // New: deduplication stats

	// Start server
	port := ":8080"
	log.Printf("API Server starting on http://localhost%s", port)
	log.Printf("Storage path: %s", StoragePath)
	log.Printf("Content-defined chunking + deduplication ENABLED")
	log.Fatal(http.ListenAndServe(port, router))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	// Parse multipart form (32MB max in memory)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file from form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Generate unique file ID
	fileID := uuid.New().String()
	fileName := header.Filename

	log.Printf("Uploading: %s (ID: %s, Size: %d bytes)", fileName, fileID, header.Size)

	// Use content-defined chunking (Rabin fingerprinting)
	chunks, err := chunking.ChunkFile(file)
	if err != nil {
		http.Error(w, "Failed to chunk file", http.StatusInternalServerError)
		log.Printf("Chunking error: %v", err)
		return
	}

	log.Printf("Created %d content-defined chunks", len(chunks))

	// Store chunks with deduplication
	chunkHashes := []string{}
	newChunksStored := 0

	for i, chunk := range chunks {
		// Store chunk - dedup.StoreChunk will handle deduplication
		_, isNew, err := chunkStore.StoreChunk(chunk.Hash, chunk.Data)
		if err != nil {
			http.Error(w, "Failed to store chunk", http.StatusInternalServerError)
			log.Printf("Failed to store chunk %d: %v", i, err)
			return
		}

		chunkHashes = append(chunkHashes, chunk.Hash)

		if isNew {
			newChunksStored++
			log.Printf("  Chunk %d: NEW (hash: %s..., size: %d bytes)", i, chunk.Hash[:8], chunk.Size)
		} else {
			log.Printf("  Chunk %d: DEDUPLICATED (hash: %s...)", i, chunk.Hash[:8])
		}
	}

	// Calculate deduplication ratio for this file
	dedupRatio := float64(len(chunks)) / float64(max(newChunksStored, 1))

	// Save file metadata
	metadata := FileMetadata{
		FileID:      fileID,
		FileName:    fileName,
		Size:        header.Size,
		ChunkHashes: chunkHashes,
		Uploaded:    time.Now().Format(time.RFC3339),
	}

	metadataDir := filepath.Join(StoragePath, "metadata")
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		http.Error(w, "Failed to create metadata directory", http.StatusInternalServerError)
		return
	}

	metadataPath := filepath.Join(metadataDir, fileID+".json")
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		http.Error(w, "Failed to save metadata", http.StatusInternalServerError)
		return
	}

	log.Printf("Upload complete: %d total chunks, %d stored, %d deduplicated (%.2fx dedup ratio)",
		len(chunks), newChunksStored, len(chunks)-newChunksStored, dedupRatio)

	// Send response
	response := UploadResponse{
		FileID:       fileID,
		FileName:     fileName,
		Size:         header.Size,
		ChunkHashes:  chunkHashes,
		ChunksStored: newChunksStored,
		DedupRatio:   dedupRatio,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fileID := vars["fileID"]

	// Read file metadata
	metadataPath := filepath.Join(StoragePath, "metadata", fileID+".json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	var metadata FileMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		http.Error(w, "Invalid metadata", http.StatusInternalServerError)
		return
	}

	log.Printf("Downloading: %s (ID: %s, %d chunks)", metadata.FileName, fileID, len(metadata.ChunkHashes))

	// Set headers for download
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", metadata.FileName))
	w.Header().Set("Content-Type", "application/octet-stream")

	// Stream chunks back to client by retrieving them via their hash
	for i, hash := range metadata.ChunkHashes {
		chunkData, err := chunkStore.GetChunk(hash)
		if err != nil {
			log.Printf("Failed to retrieve chunk %d (hash: %s): %v", i, hash[:8], err)
			http.Error(w, "Failed to retrieve chunk", http.StatusInternalServerError)
			return
		}

		if _, err := w.Write(chunkData); err != nil {
			log.Printf("Failed to write chunk %d to response", i)
			return
		}
	}

	log.Printf("Download complete: %s", metadata.FileName)
}

func listFilesHandler(w http.ResponseWriter, r *http.Request) {
	files := []FileMetadata{}

	metadataDir := filepath.Join(StoragePath, "metadata")
	entries, err := os.ReadDir(metadataDir)
	if err != nil {
		// If directory doesn't exist yet, return empty list
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"count": 0,
				"files": files,
			})
			return
		}
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(metadataDir, entry.Name())
		metadataBytes, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}

		var metadata FileMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue
		}

		files = append(files, metadata)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count": len(files),
		"files": files,
	})
}

// statsHandler returns deduplication statistics
func statsHandler(w http.ResponseWriter, r *http.Request) {
	stats := chunkStore.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}