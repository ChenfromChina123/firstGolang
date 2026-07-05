package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Storage defines the interface for file storage backends
type Storage interface {
	// SaveChunk stores a single chunk
	SaveChunk(sessionID string, chunkIndex int, data io.Reader) (int64, error)
	// ReadChunk reads a single chunk
	ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error)
	// AssembleFile merges all chunks into the final file, returns the path
	AssembleFile(sessionID string, filename string, totalChunks int) (string, error)
	// DeleteTemp cleans up temporary chunk files
	DeleteTemp(sessionID string) error
	// ReadFile reads from the completed file at an optional offset
	ReadFile(path string, offset int64) (io.ReadCloser, error)
	// FileSize returns the size of a completed file
	FileSize(path string) (int64, error)
	// BasePath returns the storage base directory
	BasePath() string
	// HashFile returns the SHA256 hex hash of a file
	HashFile(path string) (string, error)
	// DeleteFile removes a completed file from storage
	DeleteFile(path string) error
}

// AsyncStorager is an optional interface for backends that support async writes.
type AsyncStorager interface {
	Storage
	// SaveChunkAsync enqueues a chunk for async background write. Returns immediately.
	SaveChunkAsync(sessionID string, chunkIndex int, data []byte)
	// WaitAsync blocks until all pending async writes complete.
	WaitAsync()
}

// HashAssembler is an optional interface for backends that can compute hash during assembly.
// Avoids reading the assembled file a second time for hash computation.
type HashAssembler interface {
	// AssembleFileWithHash merges all chunks and computes SHA256 simultaneously.
	// Returns (filePath, hash, error).
	AssembleFileWithHash(sessionID string, filename string, totalChunks int) (string, string, error)
}

// tempDir returns the temporary directory for chunk storage
func tempDir(basePath, sessionID string) string {
	return filepath.Join(basePath, "_chunks", sessionID)
}

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat file: %w", err)
	}
	return fi.Size(), nil
}

func readFile(path string, offset int64) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, fmt.Errorf("seek: %w", err)
		}
	}
	return f, nil
}
