package trafficcapture

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
)

// BodySpool stores captured body bytes in a private temporary file so capture
// memory use stays bounded for large prompts and streamed responses.
type BodySpool struct {
	mu   sync.Mutex
	file *os.File
	// name holds the temp path when the eager unlink was refused (Windows
	// refuses to unlink an open file), so the file is removed at Close instead.
	name   string
	err    error
	closed bool
}

// Write appends bytes without affecting the HTTP stream if local storage fails.
func (spool *BodySpool) Write(body []byte) {
	if len(body) == 0 {
		return
	}

	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.closed || spool.err != nil {
		return
	}
	if spool.file == nil {
		bodyFile, err := os.CreateTemp("", "router-http-capture-body-*")
		if err != nil {
			spool.err = fmt.Errorf("create temporary HTTP body capture: %w", err)
			return
		}
		if err := os.Remove(bodyFile.Name()); err != nil {
			// Windows refuses to unlink an open file; keep the path and remove
			// it at Close. POSIX succeeds here, so the file is never visible
			// under a name there.
			spool.name = bodyFile.Name()
		}
		spool.file = bodyFile
	}
	written, err := spool.file.Write(body)
	if err == nil && written != len(body) {
		err = io.ErrShortWrite
	}
	if err != nil {
		spool.err = fmt.Errorf("write temporary HTTP body capture: %w", err)
	}
}

// Reader returns the captured prefix without changing the spool's write offset.
func (spool *BodySpool) Reader() (io.Reader, error) {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.file == nil {
		return bytes.NewReader(nil), nil
	}
	fileInfo, err := spool.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat temporary HTTP body capture: %w", err)
	}
	return io.NewSectionReader(spool.file, 0, fileInfo.Size()), nil
}

// Err reports a local-storage failure without changing the observed HTTP body.
func (spool *BodySpool) Err() error {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	return spool.err
}

// Close releases the unlinked temporary body file after its exchange is recorded.
func (spool *BodySpool) Close() error {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.closed {
		return nil
	}
	spool.closed = true
	if spool.file == nil {
		return nil
	}
	closeErr := spool.file.Close()
	if spool.name != "" {
		if err := os.Remove(spool.name); err != nil {
			if closeErr == nil {
				closeErr = err
			}
			return fmt.Errorf("remove temporary HTTP body capture: %w", closeErr)
		}
		spool.name = ""
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary HTTP body capture: %w", closeErr)
	}
	return nil
}
