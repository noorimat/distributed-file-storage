package node

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
)

const (
	// Virtual nodes per physical node for better distribution
	VirtualNodesPerNode = 150
)

// ConsistentHash implements consistent hashing for chunk distribution
type ConsistentHash struct {
	circle       map[uint32]string // hash -> nodeID
	sortedHashes []uint32
	nodes        map[string]bool // set of node IDs
	mu           sync.RWMutex
}

// NewConsistentHash creates a new consistent hash ring
func NewConsistentHash() *ConsistentHash {
	return &ConsistentHash{
		circle:       make(map[uint32]string),
		sortedHashes: []uint32{},
		nodes:        make(map[string]bool),
	}
}

// AddNode adds a node to the hash ring
func (ch *ConsistentHash) AddNode(nodeID string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Add virtual nodes to distribute load evenly
	for i := 0; i < VirtualNodesPerNode; i++ {
		virtualNodeID := fmt.Sprintf("%s-vnode-%d", nodeID, i)
		hash := ch.hashKey(virtualNodeID)
		ch.circle[hash] = nodeID
		ch.sortedHashes = append(ch.sortedHashes, hash)
	}

	sort.Slice(ch.sortedHashes, func(i, j int) bool {
		return ch.sortedHashes[i] < ch.sortedHashes[j]
	})

	ch.nodes[nodeID] = true
}

// RemoveNode removes a node from the hash ring
func (ch *ConsistentHash) RemoveNode(nodeID string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Remove all virtual nodes for this physical node
	for i := 0; i < VirtualNodesPerNode; i++ {
		virtualNodeID := fmt.Sprintf("%s-vnode-%d", nodeID, i)
		hash := ch.hashKey(virtualNodeID)
		delete(ch.circle, hash)
	}

	// Rebuild sorted hashes
	ch.sortedHashes = make([]uint32, 0, len(ch.circle))
	for hash := range ch.circle {
		ch.sortedHashes = append(ch.sortedHashes, hash)
	}
	sort.Slice(ch.sortedHashes, func(i, j int) bool {
		return ch.sortedHashes[i] < ch.sortedHashes[j]
	})

	delete(ch.nodes, nodeID)
}

// GetNode returns the node responsible for a given chunk hash
func (ch *ConsistentHash) GetNode(chunkHash string) (string, error) {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.circle) == 0 {
		return "", fmt.Errorf("no nodes available")
	}

	hash := ch.hashKey(chunkHash)

	// Binary search to find the first node >= hash
	idx := sort.Search(len(ch.sortedHashes), func(i int) bool {
		return ch.sortedHashes[i] >= hash
	})

	// Wrap around if necessary
	if idx == len(ch.sortedHashes) {
		idx = 0
	}

	return ch.circle[ch.sortedHashes[idx]], nil
}

// GetNodes returns N nodes for replication (for storing the same chunk on multiple nodes)
func (ch *ConsistentHash) GetNodes(chunkHash string, count int) ([]string, error) {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.nodes) == 0 {
		return nil, fmt.Errorf("no nodes available")
	}

	if count > len(ch.nodes) {
		count = len(ch.nodes)
	}

	hash := ch.hashKey(chunkHash)
	selectedNodes := make(map[string]bool)
	result := []string{}

	// Start from the hash position and walk the ring
	idx := sort.Search(len(ch.sortedHashes), func(i int) bool {
		return ch.sortedHashes[i] >= hash
	})

	// Collect unique nodes
	for len(result) < count {
		if idx >= len(ch.sortedHashes) {
			idx = 0
		}

		nodeID := ch.circle[ch.sortedHashes[idx]]
		if !selectedNodes[nodeID] {
			selectedNodes[nodeID] = true
			result = append(result, nodeID)
		}

		idx++

		// Prevent infinite loop if we've checked all positions
		if idx >= len(ch.sortedHashes)*2 {
			break
		}
	}

	return result, nil
}

// hashKey generates a 32-bit hash from a string
func (ch *ConsistentHash) hashKey(key string) uint32 {
	hash := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint32(hash[:4])
}

// GetNodeCount returns the number of physical nodes
func (ch *ConsistentHash) GetNodeCount() int {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return len(ch.nodes)
}