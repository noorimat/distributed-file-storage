package dedup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ChunkStore manages deduplicated chunk storage
// It keeps track of which chunks exist and their reference counts
type ChunkStore struct {
	basePath  string
	index     map[string]*ChunkMetadata // hash -> metadata
	indexLock sync.RWMutex
	indexPath string
}

// ChunkMetadata tracks information about a stored chunk
type ChunkMetadata struct {
	Hash      string `json:"hash"`       // SHA-256 hash of chunk
	Size      int    `json:"size"`       // Size in bytes
	RefCount  int    `json:"ref_count"`  // Number of files referencing this chunk
	StorePath string `json:"store_path"` // Path where chunk is stored
}

// NewChunkStore creates a new deduplicated chunk store
func NewChunkStore(basePath string) (*ChunkStore, error) {
	// Create chunks directory
	chunksPath := filepath.Join(basePath, "chunks")
	if err := os.MkdirAll(chunksPath, 0755); err != nil {
		return nil, err
	}

	indexPath := filepath.Join(basePath, "chunk_index.json")

	store := &ChunkStore{
		basePath:  chunksPath,
		index:     make(map[string]*ChunkMetadata),
		indexPath: indexPath,
	}

	// Load existing index
	if err := store.loadIndex(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return store, nil
}

// StoreChunk stores a chunk if it doesn't exist, or increments ref count if it does
// Returns: (chunkPath, isNewChunk, error)
func (cs *ChunkStore) StoreChunk(hash string, data []byte) (string, bool, error) {
	cs.indexLock.Lock()
	defer cs.indexLock.Unlock()

	// Check if chunk already exists (deduplication!)
	if metadata, exists := cs.index[hash]; exists {
		// Chunk already exists - just increment reference count
		metadata.RefCount++
		cs.saveIndex()
		return metadata.StorePath, false, nil
	}

	// New chunk - store it
	// Use first 2 chars of hash for directory sharding (prevents too many files in one dir)
	shardDir := filepath.Join(cs.basePath, hash[:2])
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return "", false, err
	}

	chunkPath := filepath.Join(shardDir, hash)
	
	// Write chunk to disk
	if err := os.WriteFile(chunkPath, data, 0644); err != nil {
		return "", false, err
	}

	// Add to index
	cs.index[hash] = &ChunkMetadata{
		Hash:      hash,
		Size:      len(data),
		RefCount:  1,
		StorePath: chunkPath,
	}

	cs.saveIndex()
	return chunkPath, true, nil
}

// GetChunk retrieves a chunk by its hash
func (cs *ChunkStore) GetChunk(hash string) ([]byte, error) {
	cs.indexLock.RLock()
	metadata, exists := cs.index[hash]
	cs.indexLock.RUnlock()

	if !exists {
		return nil, fmt.Errorf("chunk not found: %s", hash)
	}

	data, err := os.ReadFile(metadata.StorePath)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// ReleaseChunk decrements the reference count for a chunk
// If ref count reaches 0, the chunk is deleted (garbage collection)
func (cs *ChunkStore) ReleaseChunk(hash string) error {
	cs.indexLock.Lock()
	defer cs.indexLock.Unlock()

	metadata, exists := cs.index[hash]
	if !exists {
		return fmt.Errorf("chunk not found: %s", hash)
	}

	metadata.RefCount--

	// If no more references, delete the chunk
	if metadata.RefCount <= 0 {
		if err := os.Remove(metadata.StorePath); err != nil {
			return err
		}
		delete(cs.index, hash)
	}

	cs.saveIndex()
	return nil
}

// GetStats returns deduplication statistics
func (cs *ChunkStore) GetStats() map[string]interface{} {
	cs.indexLock.RLock()
	defer cs.indexLock.RUnlock()

	totalChunks := len(cs.index)
	totalSize := int64(0)
	totalRefs := 0

	for _, metadata := range cs.index {
		totalSize += int64(metadata.Size)
		totalRefs += metadata.RefCount
	}

	// Calculate space savings
	var savedSpace int64
	if totalRefs > 0 {
		savedSpace = totalSize * int64(totalRefs-totalChunks)
	}

	return map[string]interface{}{
		"unique_chunks":    totalChunks,
		"total_references": totalRefs,
		"storage_used":     totalSize,
		"space_saved":      savedSpace,
		"dedup_ratio":      float64(totalRefs) / float64(max(totalChunks, 1)),
	}
}

// loadIndex loads the chunk index from disk
func (cs *ChunkStore) loadIndex() error {
	data, err := os.ReadFile(cs.indexPath)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, &cs.index)
}

// saveIndex saves the chunk index to disk
func (cs *ChunkStore) saveIndex() error {
	data, err := json.MarshalIndent(cs.index, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cs.indexPath, data, 0644)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}