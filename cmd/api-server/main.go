package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/noorimat/distributed-file-storage/internal/chunking"
	"github.com/noorimat/distributed-file-storage/internal/crypto"
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
	ChunkHashes  []string `json:"chunk_hashes"`
	ChunksStored int      `json:"chunks_stored"`
	DedupRatio   float64  `json:"dedup_ratio"`
	Encrypted    bool     `json:"encrypted"`
}

type FileMetadata struct {
	FileID      string   `json:"file_id"`
	FileName    string   `json:"file_name"`
	Size        int64    `json:"size"`
	ChunkHashes []string `json:"chunk_hashes"`
	Uploaded    string   `json:"uploaded"`
	Encrypted   bool     `json:"encrypted"`
	Salt        string   `json:"salt,omitempty"`
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
	router.HandleFunc("/stats", statsHandler).Methods("GET")

	// Start server
	port := ":8080"
	log.Printf("API Server starting on http://localhost%s", port)
	log.Printf("Storage path: %s", StoragePath)
	log.Printf("Content-defined chunking + deduplication + encryption ENABLED")
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

	// Check if encryption is requested
	password := r.FormValue("password")
	var encryptionKey *crypto.EncryptionKey
	var encryptionSalt string

	if password != "" {
		// Derive encryption key from password
		key, err := crypto.DeriveKey(password, nil)
		if err != nil {
			http.Error(w, "Failed to derive encryption key", http.StatusInternalServerError)
			log.Printf("Encryption key derivation error: %v", err)
			return
		}
		encryptionKey = key
		encryptionSalt = fmt.Sprintf("%x", key.Salt)
		log.Printf("Encryption enabled for upload")
	}

	// Generate unique file ID
	fileID := uuid.New().String()
	fileName := header.Filename

	log.Printf("Uploading: %s (ID: %s, Size: %d bytes, Encrypted: %v)",
		fileName, fileID, header.Size, password != "")

	// Use content-defined chunking (Rabin fingerprinting)
	chunks, err := chunking.ChunkFile(file)
	if err != nil {
		http.Error(w, "Failed to chunk file", http.StatusInternalServerError)
		log.Printf("Chunking error: %v", err)
		return
	}

	log.Printf("Created %d content-defined chunks", len(chunks))

	// Store chunks with deduplication and optional encryption
	chunkHashes := []string{}
	newChunksStored := 0

	for i, chunk := range chunks {
		chunkData := chunk.Data

		// Encrypt chunk if password provided
		if encryptionKey != nil {
			encrypted, err := crypto.EncryptChunk(chunkData, encryptionKey)
			if err != nil {
				http.Error(w, "Failed to encrypt chunk", http.StatusInternalServerError)
				log.Printf("Encryption error on chunk %d: %v", i, err)
				return
			}
			chunkData = encrypted

			// Recalculate hash for encrypted data
			hash := sha256.Sum256(chunkData)
			chunk.Hash = hex.EncodeToString(hash[:])
		}

		// Store chunk - dedup.StoreChunk will handle deduplication
		_, isNew, err := chunkStore.StoreChunk(chunk.Hash, chunkData)
		if err != nil {
			http.Error(w, "Failed to store chunk", http.StatusInternalServerError)
			log.Printf("Failed to store chunk %d: %v", i, err)
			return
		}

		chunkHashes = append(chunkHashes, chunk.Hash)

		if isNew {
			newChunksStored++
			log.Printf("  Chunk %d: NEW (hash: %s..., size: %d bytes, encrypted: %v)",
				i, chunk.Hash[:8], len(chunkData), encryptionKey != nil)
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
		Encrypted:   password != "",
		Salt:        encryptionSalt,
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
		Encrypted:    password != "",
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

	// Check if file is encrypted and password is required
	password := r.URL.Query().Get("password")
	var decryptionKey *crypto.EncryptionKey

	if metadata.Encrypted {
		if password == "" {
			http.Error(w, "Password required for encrypted file", http.StatusUnauthorized)
			return
		}

		// Decode salt from hex
		salt, err := hex.DecodeString(metadata.Salt)
		if err != nil {
			http.Error(w, "Invalid encryption metadata", http.StatusInternalServerError)
			return
		}

		// Derive decryption key from password
		key, err := crypto.DeriveKey(password, salt)
		if err != nil {
			http.Error(w, "Failed to derive decryption key", http.StatusInternalServerError)
			return
		}
		decryptionKey = key
	}

	log.Printf("Downloading: %s (ID: %s, %d chunks, Encrypted: %v)",
		metadata.FileName, fileID, len(metadata.ChunkHashes), metadata.Encrypted)

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

		// Decrypt chunk if file is encrypted
		if metadata.Encrypted {
			decrypted, err := crypto.DecryptChunk(chunkData, decryptionKey)
			if err != nil {
				log.Printf("Failed to decrypt chunk %d: %v", i, err)
				http.Error(w, "Decryption failed - incorrect password?", http.StatusUnauthorized)
				return
			}
			chunkData = decrypted
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