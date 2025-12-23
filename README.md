# Distributed File Storage System

A scalable file storage system with content-based deduplication and intelligent chunking algorithms. Built to explore distributed systems concepts and storage optimization techniques.

## Overview

This project implements a file storage service that automatically detects and eliminates duplicate content across uploaded files. Using Rabin fingerprinting for content-defined chunking and SHA-256 hashing for deduplication, the system achieves significant storage savings while maintaining data integrity.

**Key Achievement**: Demonstrated 2-4x storage reduction through global deduplication on test workloads.

## Core Features

### Intelligent Chunking
- Content-defined chunking using Rabin fingerprinting algorithm
- Variable chunk sizes (2-8MB) based on content patterns
- Ensures identical data produces identical chunks regardless of file position

### Global Deduplication
- SHA-256 content-addressed storage
- Automatic duplicate detection across all files
- Reference counting system for efficient chunk lifecycle management
- Real-time deduplication statistics

### RESTful API
- File upload with automatic chunking and deduplication
- File download with chunk reconstruction
- Metadata querying and statistics endpoints
- Health monitoring

## Technical Implementation

### Architecture

The system employs a three-layer architecture:
```
API Layer (HTTP/REST)
       ↓
Processing Layer (Chunking + Deduplication)
       ↓
Storage Layer (Content-Addressed Chunks)
```

**Upload Flow**:
1. File arrives via multipart form upload
2. Rabin fingerprinting identifies optimal chunk boundaries
3. SHA-256 hash computed for each chunk
4. Deduplication check: store new chunks, reference existing ones
5. Metadata persisted with chunk references

**Download Flow**:
1. Retrieve file metadata by ID
2. Fetch chunks by hash from content-addressed storage
3. Stream chunks in order to reconstruct original file

### Deduplication Strategy

The system uses content-addressable storage where each chunk is identified by its SHA-256 hash. This enables:
- O(1) duplicate detection via hash lookup
- Automatic space savings without additional processing
- Data integrity verification through hash validation

**Reference Counting**: Tracks chunk usage across files. When a chunk's reference count reaches zero, it becomes eligible for garbage collection.

## Technology Stack

- **Language**: Go 1.25+ (chosen for performance and concurrency support)
- **Chunking Algorithm**: Rabin fingerprinting with rolling hash
- **Hashing**: SHA-256 cryptographic hash function
- **Storage**: Filesystem-based content-addressed storage
- **API**: HTTP REST with JSON responses

## Installation

### Prerequisites
- Go 1.25 or higher
- Git

### Setup
```bash
git clone https://github.com/noorimat/distributed-file-storage.git
cd distributed-file-storage
go mod download
go run cmd/api-server/main.go
```

Server starts on `http://localhost:8080`

## Usage Examples

### Upload File
```bash
curl -X POST -F "file=@document.pdf" http://localhost:8080/upload
```

**Response**:
```json
{
  "file_id": "74c93898-ef07-4d32-9cb0-51036cc50b42",
  "file_name": "document.pdf",
  "size": 5242880,
  "chunk_hashes": ["373ff5ec...", "a7d4b2e9..."],
  "chunks_stored": 2,
  "dedup_ratio": 1.0
}
```

### Upload Duplicate File
```bash
curl -X POST -F "file=@document.pdf" http://localhost:8080/upload
```

**Response**:
```json
{
  "file_id": "34b347ce-76df-4219-a285-8a3fab493d9c",
  "file_name": "document.pdf",
  "size": 5242880,
  "chunk_hashes": ["373ff5ec...", "a7d4b2e9..."],
  "chunks_stored": 0,
  "dedup_ratio": 1.0
}
```

*Note: `chunks_stored: 0` indicates complete deduplication*

### View Statistics
```bash
curl http://localhost:8080/stats
```

**Response**:
```json
{
  "unique_chunks": 2,
  "total_references": 4,
  "storage_used": 5242880,
  "space_saved": 10485760,
  "dedup_ratio": 2.0
}
```

### Download File
```bash
curl http://localhost:8080/download/{file_id} -o downloaded.pdf
```

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/upload` | POST | Upload file with automatic deduplication |
| `/download/{fileID}` | GET | Download file by ID |
| `/files` | GET | List all stored files |
| `/stats` | GET | View deduplication statistics |
| `/health` | GET | Server health check |

## Project Structure
```
distributed-file-storage/
├── cmd/
│   └── api-server/          # HTTP server and request handlers
├── internal/
│   ├── chunking/            # Rabin fingerprinting implementation
│   └── dedup/               # Deduplication engine
├── storage/                 # Data directory (excluded from version control)
│   ├── chunks/              # Content-addressed chunk storage
│   ├── metadata/            # File metadata
│   └── chunk_index.json     # Deduplication index
└── README.md
```

## Performance Characteristics

- **Chunking**: Variable size (2-8MB avg 4MB) based on content
- **Deduplication**: O(1) lookup via hash table
- **Storage Savings**: 2-4x reduction on typical workloads
- **Hash Algorithm**: SHA-256 for security and collision resistance

## Testing & Validation

Tested scenarios:
- Duplicate file uploads achieving 100% deduplication
- Multiple files with overlapping content showing partial deduplication
- File integrity verification through upload-download cycles
- Deduplication statistics accuracy across various file types

## Future Enhancements

**Phase 3: Security**
- AES-256 encryption for chunks at rest
- Client-side encryption with key derivation
- JWT-based authentication

**Phase 4: Distribution**
- Multi-node storage cluster
- Consistent hashing for chunk placement
- Node discovery and health monitoring
- Data replication for fault tolerance

**Phase 5: Production Features**
- Erasure coding (Reed-Solomon) for redundancy
- PostgreSQL for scalable metadata storage
- Compression (zstd) before encryption
- Web-based management UI
- Automated testing suite
- Monitoring and metrics (Prometheus)

## Technical Challenges

**Rabin Fingerprinting Implementation**: Implementing a rolling hash algorithm that balances chunk size distribution with boundary detection accuracy required careful tuning of the polynomial mask.

**Concurrent Access**: Ensuring thread-safe deduplication checks and chunk storage required proper mutex usage to prevent race conditions.

**File Reconstruction**: Designing an efficient streaming approach for reassembling files from chunks without loading entire files into memory.

## Learning Outcomes

This project provided hands-on experience with:
- Advanced chunking algorithms and their tradeoffs
- Content-addressed storage systems
- Building production-quality REST APIs in Go
- Concurrent programming and race condition prevention
- Systems programming and file I/O optimization

## Author

**Noor Imat**  
Computer Science, University of Virginia  
GitHub: [@noorimat](https://github.com/noorimat)

## License

MIT License - see LICENSE file for details

---

**Note**: This is an educational project demonstrating distributed systems concepts. While functional, it would require additional hardening for production deployment (authentication, encryption, distributed architecture, comprehensive testing).
