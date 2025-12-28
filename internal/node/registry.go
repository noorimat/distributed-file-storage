package node

import (
	"fmt"
	"sync"
	"time"
)

// Registry manages the cluster of storage nodes
type Registry struct {
	nodes     map[string]*NodeInfo // nodeID -> NodeInfo
	nodeLock  sync.RWMutex
	heartbeatTimeout time.Duration
}

// NewRegistry creates a new node registry
func NewRegistry(heartbeatTimeout time.Duration) *Registry {
	return &Registry{
		nodes:            make(map[string]*NodeInfo),
		heartbeatTimeout: heartbeatTimeout,
	}
}

// RegisterNode adds a new node to the registry
func (r *Registry) RegisterNode(nodeID, address string) error {
	r.nodeLock.Lock()
	defer r.nodeLock.Unlock()

	r.nodes[nodeID] = &NodeInfo{
		NodeID:   nodeID,
		Address:  address,
		Status:   "healthy",
		LastSeen: time.Now(),
	}

	return nil
}

// UpdateHeartbeat updates the last seen time for a node
func (r *Registry) UpdateHeartbeat(nodeID string, totalChunks int, used int64) error {
	r.nodeLock.Lock()
	defer r.nodeLock.Unlock()

	node, exists := r.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	node.LastSeen = time.Now()
	node.TotalChunks = totalChunks
	node.Used = used
	node.Status = "healthy"

	return nil
}

// GetHealthyNodes returns all nodes that are currently healthy
func (r *Registry) GetHealthyNodes() []*NodeInfo {
	r.nodeLock.RLock()
	defer r.nodeLock.RUnlock()

	var healthyNodes []*NodeInfo
	now := time.Now()

	for _, node := range r.nodes {
		// Check if node has sent a heartbeat recently
		if now.Sub(node.LastSeen) < r.heartbeatTimeout {
			node.Status = "healthy"
			healthyNodes = append(healthyNodes, node)
		} else {
			node.Status = "offline"
		}
	}

	return healthyNodes
}

// GetNode returns information about a specific node
func (r *Registry) GetNode(nodeID string) (*NodeInfo, error) {
	r.nodeLock.RLock()
	defer r.nodeLock.RUnlock()

	node, exists := r.nodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	return node, nil
}

// GetAllNodes returns all registered nodes (healthy and unhealthy)
func (r *Registry) GetAllNodes() []*NodeInfo {
	r.nodeLock.RLock()
	defer r.nodeLock.RUnlock()

	nodes := make([]*NodeInfo, 0, len(r.nodes))
	for _, node := range r.nodes {
		nodes = append(nodes, node)
	}

	return nodes
}

// RemoveNode removes a node from the registry
func (r *Registry) RemoveNode(nodeID string) {
	r.nodeLock.Lock()
	defer r.nodeLock.Unlock()

	delete(r.nodes, nodeID)
}

// GetNodeCount returns the number of registered nodes
func (r *Registry) GetNodeCount() int {
	r.nodeLock.RLock()
	defer r.nodeLock.RUnlock()

	return len(r.nodes)
}