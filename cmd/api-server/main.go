package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/noorimat/distributed-file-storage/internal/chunking"
	"github.com/noorimat/distributed-file-storage/internal/crypto"
	"github.com/noorimat/distributed-file-storage/internal/dedup"
	"github.com/noorimat/distributed-file-storage/internal/metadata"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	StoragePath = "./storage"
)

// Global instances
var chunkStore *dedup.ChunkStore
var db *metadata.Database

type UploadResponse struct {
	FileID       string   `json:"file_id"`
	FileName     string   `json:"file_name"`
	Size         int64    `json:"size"`
	ChunkHashes  []string `json:"chunk_hashes"`
	ChunksStored int      `json:"chunks_stored"`
	DedupRatio   float64  `json:"dedup_ratio"`
	Encrypted    bool     `json:"encrypted"`
}

func main() {
	// Create storage directory
	if err := os.MkdirAll(StoragePath, 0755); err != nil {
		log.Fatal("Failed to create storage directory:", err)
	}

	// Initialize database connection
	dbURL := getEnv("DATABASE_URL", "postgres://filestore:dev_password@localhost:5432/filestore?sslmode=disable")
	var err error
	db, err = metadata.NewDatabase(dbURL)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	defer db.Close()
	log.Printf("Connected to PostgreSQL database")

	// Initialize chunk store for deduplication
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
	log.Printf("Content-defined chunking + deduplication + encryption + PostgreSQL ENABLED")
	log.Fatal(http.ListenAndServe(port, router))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "healthy",
		"time":     time.Now().Format(time.RFC3339),
		"database": "connected",
	})
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	// Parse multipart form
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

	// Check for encryption
	password := r.FormValue("password")
	var encryptionKey *crypto.EncryptionKey
	var encryptionSalt string

	if password != "" {
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

	// Generate file ID
	fileID := uuid.New().String()
	fileName := header.Filename

	log.Printf("Uploading: %s (ID: %s, Size: %d bytes, Encrypted: %v)",
		fileName, fileID, header.Size, password != "")

	// Chunk the file
	chunks, err := chunking.ChunkFile(file)
	if err != nil {
		http.Error(w, "Failed to chunk file", http.StatusInternalServerError)
		log.Printf("Chunking error: %v", err)
		return
	}

	log.Printf("Created %d content-defined chunks", len(chunks))

	// Store chunks with deduplication and encryption
	chunkHashes := []string{}
	newChunksStored := 0

	for i, chunk := range chunks {
		chunkData := chunk.Data

		// Encrypt if password provided
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

		// Store chunk physically
		storagePath, isNew, err := chunkStore.StoreChunk(chunk.Hash, chunkData)
		if err != nil {
			http.Error(w, "Failed to store chunk", http.StatusInternalServerError)
			log.Printf("Failed to store chunk %d: %v", i, err)
			return
		}

		// Store chunk metadata in database
		dbIsNew, err := db.CreateChunk(chunk.Hash, len(chunkData), storagePath)
		if err != nil {
			http.Error(w, "Failed to save chunk metadata", http.StatusInternalServerError)
			log.Printf("Database error on chunk %d: %v", i, err)
			return
		}

		chunkHashes = append(chunkHashes, chunk.Hash)

		if isNew && dbIsNew {
			newChunksStored++
			log.Printf("  Chunk %d: NEW (hash: %s..., size: %d bytes, encrypted: %v)",
				i, chunk.Hash[:8], len(chunkData), encryptionKey != nil)
		} else {
			log.Printf("  Chunk %d: DEDUPLICATED (hash: %s...)", i, chunk.Hash[:8])
		}
	}

	// Save file metadata to database
	if err := db.CreateFile(fileID, fileName, header.Size, password != "", encryptionSalt); err != nil {
		http.Error(w, "Failed to save file metadata", http.StatusInternalServerError)
		log.Printf("Database error saving file: %v", err)
		return
	}

	// Link file to chunks in database
	for i, chunkHash := range chunkHashes {
		if err := db.LinkFileChunk(fileID, chunkHash, i); err != nil {
			http.Error(w, "Failed to link file chunks", http.StatusInternalServerError)
			log.Printf("Database error linking chunks: %v", err)
			return
		}
	}

	dedupRatio := float64(len(chunks)) / float64(max(newChunksStored, 1))

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

	// Get file metadata from database
	fileRecord, err := db.GetFile(fileID)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Check encryption
	password := r.URL.Query().Get("password")
	var decryptionKey *crypto.EncryptionKey

	if fileRecord.Encrypted {
		if password == "" {
			http.Error(w, "Password required for encrypted file", http.StatusUnauthorized)
			return
		}

		salt, err := hex.DecodeString(fileRecord.Salt)
		if err != nil {
			http.Error(w, "Invalid encryption metadata", http.StatusInternalServerError)
			return
		}

		key, err := crypto.DeriveKey(password, salt)
		if err != nil {
			http.Error(w, "Failed to derive decryption key", http.StatusInternalServerError)
			return
		}
		decryptionKey = key
	}

	// Get chunk hashes from database
	chunkHashes, err := db.GetFileChunks(fileID)
	if err != nil {
		http.Error(w, "Failed to retrieve file chunks", http.StatusInternalServerError)
		return
	}

	log.Printf("Downloading: %s (ID: %s, %d chunks, Encrypted: %v)",
		fileRecord.FileName, fileID, len(chunkHashes), fileRecord.Encrypted)

	// Set download headers
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileRecord.FileName))
	w.Header().Set("Content-Type", "application/octet-stream")

	// Stream chunks
	for i, hash := range chunkHashes {
		chunkData, err := chunkStore.GetChunk(hash)
		if err != nil {
			log.Printf("Failed to retrieve chunk %d (hash: %s): %v", i, hash[:8], err)
			http.Error(w, "Failed to retrieve chunk", http.StatusInternalServerError)
			return
		}

		// Decrypt if needed
		if fileRecord.Encrypted {
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

	log.Printf("Download complete: %s", fileRecord.FileName)
}

func listFilesHandler(w http.ResponseWriter, r *http.Request) {
	files, err := db.ListFiles()
	if err != nil {
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		log.Printf("Database error listing files: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count": len(files),
		"files": files,
	})
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	stats, err := db.GetStats()
	if err != nil {
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		log.Printf("Database error getting stats: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}