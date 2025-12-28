package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/noorimat/distributed-file-storage/internal/node"
	"github.com/google/uuid"
)

func main() {
	// Command line flags
	nodeID := flag.String("id", uuid.New().String(), "Node ID (auto-generated if not specified)")
	port := flag.Int("port", 9001, "Port to listen on")
	storagePath := flag.String("storage", "./node-storage", "Storage directory path")
	coordinatorAddr := flag.String("coordinator", "localhost:8080", "Coordinator address")
	flag.Parse()

	// Create storage node
	address := fmt.Sprintf("localhost:%d", *port)
	storageNode := node.NewStorageNode(*nodeID, address, *storagePath, *coordinatorAddr)

	log.Printf("Starting storage node...")
	log.Printf("Node ID: %s", *nodeID)
	log.Printf("Address: %s", address)
	log.Printf("Storage: %s", *storagePath)
	log.Printf("Coordinator: %s", *coordinatorAddr)

	// Start the node (blocks)
	if err := storageNode.Start(); err != nil {
		log.Fatalf("Failed to start storage node: %v", err)
		os.Exit(1)
	}
}