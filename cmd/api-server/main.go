package main

import (
	"bytes"
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
	"github.com/noorimat/distributed-file-storage/internal/node"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	StoragePath      = "./storage"
	ReplicationCount = 3 // Store each chunk on 3 nodes
)

// Global instances
var chunkStore *dedup.ChunkStore
var db *metadata.Database
var nodeRegistry *node.Registry
var consistentHash *node.ConsistentHash

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

	// Initialize chunk store for local deduplication (fallback)
	chunkStore, err = dedup.NewChunkStore(StoragePath)
	if err != nil {
		log.Fatal("Failed to initialize chunk store:", err)
	}

	// Initialize node registry and consistent hashing
	nodeRegistry = node.NewRegistry(30 * time.Second)
	consistentHash = node.NewConsistentHash()
	log.Printf("Initialized node registry and consistent hashing")

	router := mux.NewRouter()

	// Existing routes
	router.HandleFunc("/health", healthHandler).Methods("GET")
	router.HandleFunc("/upload", uploadHandler).Methods("POST")
	router.HandleFunc("/download/{fileID}", downloadHandler).Methods("GET")
	router.HandleFunc("/files", listFilesHandler).Methods("GET")
	router.HandleFunc("/stats", statsHandler).Methods("GET")

	// New routes for node coordination
	router.HandleFunc("/register", registerNodeHandler).Methods("POST")
	router.HandleFunc("/heartbeat", heartbeatHandler).Methods("POST")
	router.HandleFunc("/nodes", listNodesHandler).Methods("GET")

	// Start server
	port := ":8080"
	log.Printf("API Server (Coordinator) starting on http://localhost%s", port)
	log.Printf("Storage path: %s", StoragePath)
	log.Printf("Multi-node distribution + PostgreSQL + encryption ENABLED")
	log.Fatal(http.ListenAndServe(port, router))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	healthyNodes := nodeRegistry.GetHealthyNodes()
	
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "healthy",
		"time":          time.Now().Format(time.RFC3339),
		"database":      "connected",
		"storage_nodes": len(healthyNodes),
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

	// Get healthy nodes
	healthyNodes := nodeRegistry.GetHealthyNodes()
	useDistribution := len(healthyNodes) > 0

	if useDistribution {
		log.Printf("Distributing chunks across %d nodes", len(healthyNodes))
	} else {
		log.Printf("No storage nodes available, storing locally")
	}

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

		var storagePath string
		var isNew bool

		if useDistribution {
			// Distribute to nodes using consistent hashing
			targetNodes, err := consistentHash.GetNodes(chunk.Hash, ReplicationCount)
			if err != nil {
				log.Printf("Failed to get target nodes: %v", err)
				// Fallback to local storage
				storagePath, isNew, err = chunkStore.StoreChunk(chunk.Hash, chunkData)
			} else {
				isNew, err = distributeChunkToNodes(chunk.Hash, chunkData, targetNodes)
				if err != nil {
					log.Printf("Failed to distribute chunk: %v", err)
					// Fallback to local storage
					storagePath, isNew, err = chunkStore.StoreChunk(chunk.Hash, chunkData)
				} else {
					storagePath = fmt.Sprintf("distributed:%s", targetNodes[0])
				}
			}
		} else {
			// Store locally
			storagePath, isNew, err = chunkStore.StoreChunk(chunk.Hash, chunkData)
		}

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
		// Try to get from distributed nodes first
		chunkData, err := retrieveChunkFromNodes(hash)
		if err != nil {
			// Fallback to local storage
			chunkData, err = chunkStore.GetChunk(hash)
			if err != nil {
				log.Printf("Failed to retrieve chunk %d (hash: %s): %v", i, hash[:8], err)
				http.Error(w, "Failed to retrieve chunk", http.StatusInternalServerError)
				return
			}
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

// registerNodeHandler handles storage node registration
func registerNodeHandler(w http.ResponseWriter, r *http.Request) {
	var nodeInfo node.NodeInfo
	if err := json.NewDecoder(r.Body).Decode(&nodeInfo); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if err := nodeRegistry.RegisterNode(nodeInfo.NodeID, nodeInfo.Address); err != nil {
		http.Error(w, "Failed to register node", http.StatusInternalServerError)
		return
	}

	// Add to consistent hash ring
	consistentHash.AddNode(nodeInfo.NodeID)

	log.Printf("Registered storage node: %s at %s", nodeInfo.NodeID, nodeInfo.Address)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "registered",
		"node_id": nodeInfo.NodeID,
	})
}

// heartbeatHandler handles heartbeat messages from storage nodes
func heartbeatHandler(w http.ResponseWriter, r *http.Request) {
	var heartbeat node.HeartbeatMessage
	if err := json.NewDecoder(r.Body).Decode(&heartbeat); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if err := nodeRegistry.UpdateHeartbeat(heartbeat.NodeID, heartbeat.TotalChunks, heartbeat.Used); err != nil {
		http.Error(w, "Failed to update heartbeat", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// listNodesHandler returns all registered nodes
func listNodesHandler(w http.ResponseWriter, r *http.Request) {
	nodes := nodeRegistry.GetAllNodes()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count": len(nodes),
		"nodes": nodes,
	})
}

// distributeChunkToNodes sends a chunk to multiple storage nodes for replication
func distributeChunkToNodes(chunkHash string, chunkData []byte, nodeIDs []string) (bool, error) {
	isNew := false

	for _, nodeID := range nodeIDs {
		nodeInfo, err := nodeRegistry.GetNode(nodeID)
		if err != nil {
			log.Printf("Failed to get node %s: %v", nodeID, err)
			continue
		}

		// Send chunk to node
		url := fmt.Sprintf("http://%s/store", nodeInfo.Address)
		
		storeReq := node.StoreChunkRequest{
			ChunkHash: chunkHash,
			ChunkData: chunkData,
		}

		reqBody, _ := json.Marshal(storeReq)
		resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
		if err != nil {
			log.Printf("Failed to store chunk on node %s: %v", nodeID, err)
			continue
		}
		defer resp.Body.Close()

		var storeResp node.StoreChunkResponse
		if err := json.NewDecoder(resp.Body).Decode(&storeResp); err != nil {
			log.Printf("Failed to decode response from node %s: %v", nodeID, err)
			continue
		}

		if storeResp.Success {
			log.Printf("Stored chunk %s on node %s", chunkHash[:8], nodeID)
			isNew = true
		}
	}

	return isNew, nil
}

// retrieveChunkFromNodes attempts to retrieve a chunk from storage nodes
func retrieveChunkFromNodes(chunkHash string) ([]byte, error) {
	targetNodes, err := consistentHash.GetNodes(chunkHash, ReplicationCount)
	if err != nil {
		return nil, err
	}

	for _, nodeID := range targetNodes {
		nodeInfo, err := nodeRegistry.GetNode(nodeID)
		if err != nil {
			continue
		}

		url := fmt.Sprintf("http://%s/retrieve/%s", nodeInfo.Address, chunkHash)
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Failed to retrieve from node %s: %v", nodeID, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var retrieveResp node.RetrieveChunkResponse
			if err := json.NewDecoder(resp.Body).Decode(&retrieveResp); err != nil {
				continue
			}

			if retrieveResp.Success {
				return retrieveResp.ChunkData, nil
			}
		}
	}

	return nil, fmt.Errorf("chunk not found on any node")
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