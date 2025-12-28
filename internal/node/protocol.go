package node

import (
	"encoding/json"
	"time"
)

// NodeInfo represents metadata about a storage node
type NodeInfo struct {
	NodeID      string    `json:"node_id"`      // Unique identifier for this node
	Address     string    `json:"address"`      // HTTP address (e.g., "localhost:9001")
	Status      string    `json:"status"`       // "healthy", "degraded", "offline"
	TotalChunks int       `json:"total_chunks"` // Number of chunks stored on this node
	LastSeen    time.Time `json:"last_seen"`    // Last heartbeat timestamp
	Capacity    int64     `json:"capacity"`     // Total storage capacity in bytes
	Used        int64     `json:"used"`         // Used storage in bytes
}

// ChunkLocation represents where a chunk is stored
type ChunkLocation struct {
	ChunkHash string   `json:"chunk_hash"`
	NodeIDs   []string `json:"node_ids"` // List of nodes that have this chunk (for replication)
}

// StoreChunkRequest is sent to a storage node to store a chunk
type StoreChunkRequest struct {
	ChunkHash string `json:"chunk_hash"`
	ChunkData []byte `json:"chunk_data"`
}

// StoreChunkResponse is returned after storing a chunk
type StoreChunkResponse struct {
	Success   bool   `json:"success"`
	NodeID    string `json:"node_id"`
	ChunkHash string `json:"chunk_hash"`
	Error     string `json:"error,omitempty"`
}

// RetrieveChunkRequest asks a node for a specific chunk
type RetrieveChunkRequest struct {
	ChunkHash string `json:"chunk_hash"`
}

// RetrieveChunkResponse returns the chunk data
type RetrieveChunkResponse struct {
	Success   bool   `json:"success"`
	ChunkHash string `json:"chunk_hash"`
	ChunkData []byte `json:"chunk_data"`
	Error     string `json:"error,omitempty"`
}

// HeartbeatMessage is sent periodically by nodes to indicate they're alive
type HeartbeatMessage struct {
	NodeID      string    `json:"node_id"`
	Address     string    `json:"address"`
	TotalChunks int       `json:"total_chunks"`
	Used        int64     `json:"used"`
	Timestamp   time.Time `json:"timestamp"`
}

// Helper function to serialize messages
func (n *NodeInfo) ToJSON() ([]byte, error) {
	return json.Marshal(n)
}

// Helper function to deserialize messages
func NodeInfoFromJSON(data []byte) (*NodeInfo, error) {
	var node NodeInfo
	err := json.Unmarshal(data, &node)
	return &node, err
}