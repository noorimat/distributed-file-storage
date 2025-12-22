package chunking

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// Rabin chunking parameters
const (
	MinChunkSize    = 2 * 1024 * 1024  // 2MB minimum
	AvgChunkSize    = 4 * 1024 * 1024  // 4MB average (target)
	MaxChunkSize    = 8 * 1024 * 1024  // 8MB maximum
	WindowSize      = 48                // Rolling hash window
	RabinPolynomial = 0x3DA3358B4DC173  // Rabin fingerprint polynomial
)

// Chunk represents a single chunk of data with its hash
type Chunk struct {
	Data     []byte // The actual chunk data
	Hash     string // SHA-256 hash of the chunk (used for deduplication)
	Size     int    // Size in bytes
	Offset   int64  // Offset in original file
}

// ChunkReader performs content-defined chunking using Rabin fingerprinting
type ChunkReader struct {
	reader      io.Reader
	buffer      []byte
	windowSize  int
	polynomial  uint64
	offset      int64
}

// NewChunkReader creates a new ChunkReader with Rabin fingerprinting
func NewChunkReader(r io.Reader) *ChunkReader {
	return &ChunkReader{
		reader:     r,
		buffer:     make([]byte, MaxChunkSize),
		windowSize: WindowSize,
		polynomial: RabinPolynomial,
		offset:     0,
	}
}

// NextChunk reads the next content-defined chunk
// Uses Rabin fingerprinting to find chunk boundaries based on content patterns
func (cr *ChunkReader) NextChunk() (*Chunk, error) {
	n, err := io.ReadFull(cr.reader, cr.buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	
	if n == 0 {
		return nil, io.EOF
	}

	// Find chunk boundary using simplified Rabin fingerprinting
	chunkSize := cr.findBoundary(cr.buffer[:n])
	
	// Ensure chunk stays within size limits
	if chunkSize < MinChunkSize && n == MaxChunkSize {
		chunkSize = MinChunkSize
	}
	if chunkSize > n {
		chunkSize = n
	}

	// Extract the chunk data
	chunkData := make([]byte, chunkSize)
	copy(chunkData, cr.buffer[:chunkSize])

	// Calculate SHA-256 hash for deduplication
	hash := sha256.Sum256(chunkData)
	hashString := hex.EncodeToString(hash[:])

	chunk := &Chunk{
		Data:   chunkData,
		Hash:   hashString,
		Size:   chunkSize,
		Offset: cr.offset,
	}

	cr.offset += int64(chunkSize)

	// If we have leftover data, we need to handle it
	// For now, we'll read more data in the next call
	if chunkSize < n {
		// Create a new reader that includes the leftover data
		leftover := cr.buffer[chunkSize:n]
		cr.reader = io.MultiReader(
			&bufferReader{data: leftover},
			cr.reader,
		)
	}

	return chunk, nil
}

// findBoundary uses a simplified Rabin fingerprint to find chunk boundaries
// Returns the position where we should cut the chunk
func (cr *ChunkReader) findBoundary(data []byte) int {
	if len(data) < MinChunkSize {
		return len(data)
	}

	// Start looking for boundary after minimum chunk size
	hash := uint64(0)

	// We want roughly 4MB chunks, so adjust the mask
	// Smaller mask = more frequent boundaries = smaller chunks
	// Larger mask = less frequent boundaries = larger chunks
	targetMask := uint64((1 << 20) - 1) // Targets ~4MB average

	for i := MinChunkSize; i < len(data) && i < MaxChunkSize; i++ {
		// Simple rolling hash
		hash = (hash << 1) + uint64(data[i])

		// Check if we hit a boundary (when lower bits match pattern)
		if (hash & targetMask) == 0 {
			return i
		}
	}

	// If no boundary found, return max size or end of data
	if len(data) > MaxChunkSize {
		return MaxChunkSize
	}
	return len(data)
}

// bufferReader is a helper to read from a byte slice
type bufferReader struct {
	data []byte
	pos  int
}

func (br *bufferReader) Read(p []byte) (n int, err error) {
	if br.pos >= len(br.data) {
		return 0, io.EOF
	}
	n = copy(p, br.data[br.pos:])
	br.pos += n
	return n, nil
}

// ChunkFile is a helper function that chunks an entire file
func ChunkFile(r io.Reader) ([]*Chunk, error) {
	cr := NewChunkReader(r)
	chunks := []*Chunk{}

	for {
		chunk, err := cr.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}