package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Configuration constants for the storage system
const (
	StoragePath = "./storage/chunks"     // Local directory where chunks are stored
	ChunkSize   = 4 * 1024 * 1024        // 4MB chunks - balance between memory usage and I/O efficiency
)

// UploadResponse represents the JSON response sent after a successful file upload
type UploadResponse struct {
	FileID   string   `json:"file_id"`    // Unique identifier for the uploaded file
	FileName string   `json:"file_name"`  // Original name of the uploaded file
	Size     int64    `json:"size"`       // Total size of the file in bytes
	ChunkIDs []string `json:"chunk_ids"`  // List of chunk identifiers for this file
}

// DownloadResponse represents metadata about a file being downloaded
type DownloadResponse struct {
	FileName string `json:"file_name"`   // Name of the file being downloaded
	Size     int64  `json:"size"`        // Size of the file in bytes
}

func main() {
	// Initialize storage directory - create it if it doesn't exist
	// 0755 permissions: owner can read/write/execute, others can read/execute
	if err := os.MkdirAll(StoragePath, 0755); err != nil {
		log.Fatal("Failed to create storage directory:", err)
	}

	// Initialize HTTP router using gorilla/mux for flexible routing
	router := mux.NewRouter()

	// Define API endpoints
	router.HandleFunc("/health", healthHandler).Methods("GET")              // Health check endpoint
	router.HandleFunc("/upload", uploadHandler).Methods("POST")             // File upload endpoint
	router.HandleFunc("/download/{fileID}", downloadHandler).Methods("GET") // File download by ID
	router.HandleFunc("/files", listFilesHandler).Methods("GET")            // List all uploaded files

	// Start HTTP server on port 8080
	port := ":8080"
	log.Printf("🚀 API Server starting on http://localhost%s", port)
	log.Printf("📁 Storage path: %s", StoragePath)
	log.Fatal(http.ListenAndServe(port, router))
}

// healthHandler responds with server status - useful for monitoring and load balancers
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// uploadHandler processes file uploads by chunking them into fixed-size pieces
// This approach allows for efficient storage, deduplication (future), and distributed storage (future)
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	// Parse the multipart form data with a 32MB memory limit
	// Files larger than this will be temporarily stored on disk
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Extract the uploaded file from the form data
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file from form", http.StatusBadRequest)
		return
	}
	defer file.Close() // Ensure file is closed when function exits

	// Generate a unique UUID for this file - prevents naming conflicts
	fileID := uuid.New().String()
	fileName := header.Filename

	log.Printf("Uploading: %s (ID: %s, Size: %d bytes)", fileName, fileID, header.Size)

	// Create a dedicated directory for this file's chunks
	// Directory structure: ./storage/chunks/{fileID}/
	fileDir := filepath.Join(StoragePath, fileID)
	if err := os.MkdirAll(fileDir, 0755); err != nil {
		http.Error(w, "Failed to create file directory", http.StatusInternalServerError)
		return
	}

	// Chunk the file into fixed-size pieces
	// This enables: deduplication, parallel transfers, and distributed storage
	chunkIDs := []string{}
	chunkIndex := 0
	buffer := make([]byte, ChunkSize) // Reusable buffer for reading chunks

	for {
		// Read up to ChunkSize bytes from the file
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			http.Error(w, "Failed to read file", http.StatusInternalServerError)
			return
		}
		if n == 0 {
			break // End of file reached
		}

		// Generate a unique chunk identifier: {fileID}-chunk-{index}
		chunkID := fmt.Sprintf("%s-chunk-%d", fileID, chunkIndex)
		chunkPath := filepath.Join(fileDir, chunkID)

		// Write the chunk to disk
		// buffer[:n] ensures we only write the actual bytes read, not the entire buffer
		if err := os.WriteFile(chunkPath, buffer[:n], 0644); err != nil {
			http.Error(w, "Failed to write chunk", http.StatusInternalServerError)
			return
		}

		chunkIDs = append(chunkIDs, chunkID)
		chunkIndex++
	}

	// Store metadata about the file for later retrieval
	// This includes: original filename, size, chunk list, and upload timestamp
	metadata := map[string]interface{}{
		"file_id":   fileID,
		"file_name": fileName,
		"size":      header.Size,
		"chunks":    chunkIDs,
		"uploaded":  time.Now().Format(time.RFC3339),
	}

	// Write metadata to a JSON file in the same directory as the chunks
	metadataPath := filepath.Join(fileDir, "metadata.json")
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		http.Error(w, "Failed to save metadata", http.StatusInternalServerError)
		return
	}

	log.Printf("Upload complete: %d chunks created", len(chunkIDs))

	// Send success response back to client with file details
	response := UploadResponse{
		FileID:   fileID,
		FileName: fileName,
		Size:     header.Size,
		ChunkIDs: chunkIDs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// downloadHandler reconstructs a file from its chunks and streams it to the client
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	// Extract fileID from URL path parameter
	vars := mux.Vars(r)
	fileID := vars["fileID"]

	// Locate the file's metadata
	fileDir := filepath.Join(StoragePath, fileID)
	metadataPath := filepath.Join(fileDir, "metadata.json")

	// Read and parse metadata
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		http.Error(w, "Invalid metadata", http.StatusInternalServerError)
		return
	}

	fileName := metadata["file_name"].(string)
	chunks := metadata["chunks"].([]interface{})

	log.Printf("Downloading: %s (ID: %s, %d chunks)", fileName, fileID, len(chunks))

	// Set HTTP headers to trigger browser download
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
	w.Header().Set("Content-Type", "application/octet-stream")

	// Stream chunks back to client in order
	// This approach allows downloading large files without loading entire file into memory
	for _, chunkIDInterface := range chunks {
		chunkID := chunkIDInterface.(string)
		chunkPath := filepath.Join(fileDir, chunkID)

		// Read chunk from disk
		chunkData, err := os.ReadFile(chunkPath)
		if err != nil {
			log.Printf("Failed to read chunk: %s", chunkID)
			return
		}

		// Write chunk to HTTP response stream
		if _, err := w.Write(chunkData); err != nil {
			log.Printf("Failed to write chunk to response: %s", chunkID)
			return
		}
	}

	log.Printf("Download complete: %s", fileName)
}

// listFilesHandler returns a JSON list of all uploaded files and their metadata
func listFilesHandler(w http.ResponseWriter, r *http.Request) {
	files := []map[string]interface{}{}

	// Read all directories in the storage path
	// Each directory represents one uploaded file
	entries, err := os.ReadDir(StoragePath)
	if err != nil {
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}

	// Iterate through each file directory and extract metadata
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // Skip any non-directory files
		}

		// Read the metadata.json file for this uploaded file
		metadataPath := filepath.Join(StoragePath, entry.Name(), "metadata.json")
		metadataBytes, err := os.ReadFile(metadataPath)
		if err != nil {
			continue // Skip if metadata is missing or unreadable
		}

		var metadata map[string]interface{}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue // Skip if metadata is malformed
		}

		files = append(files, metadata)
	}

	// Return list of files as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count": len(files),
		"files": files,
	})
}