# Distributed File Storage

A distributed file storage system I built in Go. You upload a file, it gets split into chunks, duplicate chunks are only stored once, and each chunk is copied to 3 storage nodes so the file can still be downloaded if a node goes down.

## How it works

```
                 Client
                   |
             Coordinator  ----  PostgreSQL (file + chunk metadata)
            /      |      \
       Node 1   Node 2   Node 3
```

- **Coordinator:** the API server. Handles uploads and downloads, keeps track of which nodes are alive, decides where each chunk goes, and stores metadata in PostgreSQL.
- **Storage nodes:** small HTTP servers that store chunks on disk. They register with the coordinator when they start and send heartbeats so it knows they're still up.

### Upload path

1. The file is split into chunks using content-defined chunking (Rabin fingerprinting), with chunk sizes between 2 and 8 MB.
2. Each chunk is hashed with SHA-256. If that hash already exists, the chunk isn't stored again and its reference count goes up.
3. If a password is given, the file is encrypted with AES-256-GCM (key derived with PBKDF2, unique salt per file) before it's stored.
4. Consistent hashing (150 virtual nodes per physical node) picks a primary node and 2 replicas, and the chunk is written to all 3.

### Download path

The coordinator looks up the file's chunks in PostgreSQL and fetches each one from any healthy replica. If a node misses heartbeats for 30 seconds, it's marked down and reads go to the other replicas.

## Why I made these choices

- **Content-defined chunking instead of fixed-size:** with fixed-size chunks, inserting a few bytes at the start of a file shifts every chunk after it, so nothing dedupes. Rabin fingerprinting picks boundaries based on the content itself, so an edit only changes the chunks around it.
- **Consistent hashing instead of `hash % N`:** with `hash % N`, adding or removing a node remaps almost every chunk. With a hash ring, only the chunks near that node move.
- **3x replication instead of erasure coding:** much simpler to build and any single replica can serve a read. Erasure coding would use less space but needs reconstruction logic I didn't want to take on yet.
- **PostgreSQL for metadata:** I started with JSON files, but needed transactions and indexed lookups once multiple uploads were running at the same time.

## Running it locally

Requires Go 1.25+ and Docker.

```bash
git clone https://github.com/noorimat/distributed-file-storage.git
cd distributed-file-storage

# start PostgreSQL
docker compose up -d

# start the coordinator
go run cmd/api-server/main.go

# start 3 storage nodes (separate terminals)
go run cmd/storage-node/main.go -id node1 -port 9001 -storage ./node1-storage
go run cmd/storage-node/main.go -id node2 -port 9002 -storage ./node2-storage
go run cmd/storage-node/main.go -id node3 -port 9003 -storage ./node3-storage
```

## Usage

```bash
# upload
curl -X POST -F "file=@document.pdf" http://localhost:8080/upload

# upload with encryption
curl -X POST -F "file=@document.pdf" -F "password=mysecret" http://localhost:8080/upload

# download (add ?password=... for encrypted files)
curl http://localhost:8080/download/<file_id> -o document.pdf

# list files, dedup stats, and node status
curl http://localhost:8080/files
curl http://localhost:8080/stats
curl http://localhost:8080/nodes
```

Uploading the same file twice returns `"chunks_stored": 0`, since every chunk already exists.

### Endpoints

| Coordinator | | Storage node | |
|---|---|---|---|
| `POST /upload` | upload a file | `POST /store` | store a chunk (internal) |
| `GET /download/{id}` | download a file | `GET /retrieve/{hash}` | fetch a chunk (internal) |
| `GET /files` | list files | `GET /chunks` | list chunks on this node |
| `GET /stats` | dedup stats | `GET /health` | node health |
| `GET /nodes` | node status | | |
| `POST /register`, `POST /heartbeat` | used by nodes | | |

## Project structure

```
cmd/
  api-server/        coordinator
  storage-node/      storage node
internal/
  chunking/          Rabin fingerprinting
  crypto/            AES-GCM + PBKDF2
  dedup/             hashing and reference counting
  metadata/          PostgreSQL layer
  node/              registry, heartbeats, consistent hash ring, node server
scripts/init.sql     database schema
```

## How I tested it

I ran everything locally with 3 nodes and tested:

- uploads from 100 bytes to 100 MB
- uploading duplicate files and checking that no new chunks were stored
- encrypted uploads, downloading with the right and wrong password
- stopping a node mid-download and confirming the file still came back from a replica
- restarting the coordinator and checking that metadata persisted

In my testing, dedup cut storage by about 2-4x on files that shared content.

## What was hard

<!-- Write 2-3 sentences in your own words: a bug that took you a while, something that surprised you, or what you'd do differently. -->

## Limitations

- The coordinator is a single point of failure.
- If a node dies, its chunks aren't re-replicated, so those chunks are down to 2 copies until the node comes back.
- Files are loaded fully into memory while being chunked, so very large files (over ~1 GB) are a problem.
- No authentication on the API.
- Encryption happens on the coordinator, so the password is sent to the server. It's not end-to-end.
- Only tested with all nodes on one machine.

## What I'd add next

- Re-replicating chunks when a node goes down
- Streaming uploads so large files don't have to fit in memory
- Erasure coding to cut the 3x storage overhead

## License

MIT
