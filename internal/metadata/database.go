package metadata

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// Database handles all database operations
type Database struct {
	db *sql.DB
}

// FileRecord represents a file in the database
type FileRecord struct {
	FileID     string    `json:"file_id"`
	FileName   string    `json:"file_name"`
	FileSize   int64     `json:"file_size"`
	Encrypted  bool      `json:"encrypted"`
	Salt       string    `json:"salt,omitempty"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// ChunkRecord represents a chunk in the database
type ChunkRecord struct {
	ChunkHash   string `json:"chunk_hash"`
	ChunkSize   int    `json:"chunk_size"`
	RefCount    int    `json:"ref_count"`
	StoragePath string `json:"storage_path"`
}

// NewDatabase creates a new database connection
func NewDatabase(connectionString string) (*Database, error) {
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &Database{db: db}, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) CreateFile(fileID, fileName string, fileSize int64, encrypted bool, salt string) error {
	query := `
		INSERT INTO files (file_id, file_name, file_size, encrypted, salt)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := d.db.Exec(query, fileID, fileName, fileSize, encrypted, sql.NullString{String: salt, Valid: salt != ""})
	return err
}

func (d *Database) GetFile(fileID string) (*FileRecord, error) {
	query := `
		SELECT file_id, file_name, file_size, encrypted, COALESCE(salt, ''), uploaded_at
		FROM files
		WHERE file_id = $1
	`
	
	var file FileRecord
	err := d.db.QueryRow(query, fileID).Scan(
		&file.FileID,
		&file.FileName,
		&file.FileSize,
		&file.Encrypted,
		&file.Salt,
		&file.UploadedAt,
	)
	
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("file not found")
	}
	if err != nil {
		return nil, err
	}
	
	return &file, nil
}

func (d *Database) ListFiles() ([]FileRecord, error) {
	query := `
		SELECT file_id, file_name, file_size, encrypted, COALESCE(salt, ''), uploaded_at
		FROM files
		ORDER BY uploaded_at DESC
	`
	
	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var files []FileRecord
	for rows.Next() {
		var file FileRecord
		err := rows.Scan(
			&file.FileID,
			&file.FileName,
			&file.FileSize,
			&file.Encrypted,
			&file.Salt,
			&file.UploadedAt,
		)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	
	return files, nil
}

func (d *Database) CreateChunk(chunkHash string, chunkSize int, storagePath string) (bool, error) {
	var exists bool
	checkQuery := `SELECT EXISTS(SELECT 1 FROM chunks WHERE chunk_hash = $1)`
	err := d.db.QueryRow(checkQuery, chunkHash).Scan(&exists)
	if err != nil {
		return false, err
	}
	
	if exists {
		updateQuery := `UPDATE chunks SET ref_count = ref_count + 1 WHERE chunk_hash = $1`
		_, err := d.db.Exec(updateQuery, chunkHash)
		return false, err
	}
	
	insertQuery := `
		INSERT INTO chunks (chunk_hash, chunk_size, storage_path, ref_count)
		VALUES ($1, $2, $3, 1)
	`
	_, err = d.db.Exec(insertQuery, chunkHash, chunkSize, storagePath)
	return true, err
}

func (d *Database) LinkFileChunk(fileID, chunkHash string, chunkOrder int) error {
	query := `
		INSERT INTO file_chunks (file_id, chunk_hash, chunk_order)
		VALUES ($1, $2, $3)
	`
	_, err := d.db.Exec(query, fileID, chunkHash, chunkOrder)
	return err
}

func (d *Database) GetFileChunks(fileID string) ([]string, error) {
	query := `
		SELECT chunk_hash
		FROM file_chunks
		WHERE file_id = $1
		ORDER BY chunk_order ASC
	`
	
	rows, err := d.db.Query(query, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var chunkHashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		chunkHashes = append(chunkHashes, hash)
	}
	
	return chunkHashes, nil
}

func (d *Database) GetChunk(chunkHash string) (*ChunkRecord, error) {
	query := `
		SELECT chunk_hash, chunk_size, ref_count, storage_path
		FROM chunks
		WHERE chunk_hash = $1
	`
	
	var chunk ChunkRecord
	err := d.db.QueryRow(query, chunkHash).Scan(
		&chunk.ChunkHash,
		&chunk.ChunkSize,
		&chunk.RefCount,
		&chunk.StoragePath,
	)
	
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chunk not found")
	}
	if err != nil {
		return nil, err
	}
	
	return &chunk, nil
}

func (d *Database) GetStats() (map[string]interface{}, error) {
	query := `
		SELECT 
			COUNT(*) as unique_chunks,
			COALESCE(SUM(ref_count), 0) as total_references,
			COALESCE(SUM(chunk_size), 0) as storage_used
		FROM chunks
	`
	
	var uniqueChunks, totalRefs int
	var storageUsed int64
	
	err := d.db.QueryRow(query).Scan(&uniqueChunks, &totalRefs, &storageUsed)
	if err != nil {
		return nil, err
	}
	
	spaceSaved := int64(0)
	if totalRefs > uniqueChunks {
		spaceSaved = storageUsed * int64(totalRefs-uniqueChunks) / int64(max(uniqueChunks, 1))
	}
	
	dedupRatio := float64(totalRefs) / float64(max(uniqueChunks, 1))
	
	return map[string]interface{}{
		"unique_chunks":    uniqueChunks,
		"total_references": totalRefs,
		"storage_used":     storageUsed,
		"space_saved":      spaceSaved,
		"dedup_ratio":      dedupRatio,
	}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}