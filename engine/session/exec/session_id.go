package exec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// generateSessionID creates a unique, secure session identifier
// Format: {containerID}:{execID}:{unixNano}-{16hexdigits}
func generateSessionID(containerID, execID string) (string, error) {
	// Get current timestamp
	timestamp := time.Now().UnixNano()

	// Generate 8 random bytes (16 hex characters)
	randomBytes := make([]byte, 8)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random session ID: %w", err)
	}

	randomHex := hex.EncodeToString(randomBytes)

	// Format: containerID:execID:timestamp-randomhex
	sessionID := fmt.Sprintf("%s:%s:%d-%s", containerID, execID, timestamp, randomHex)

	return sessionID, nil
}

// parseSessionID parses a session ID into its components
// Returns: containerID, execID, timestamp, randomPart, error
func parseSessionID(sessionID string) (string, string, int64, string, error) {
	// This is a helper function for future use (e.g., validation, logging)
	// Format expected: containerID:execID:timestamp-randomhex

	// For now, we'll implement a simple parser that can be enhanced later
	// The registry will primarily use the session ID as an opaque string

	return "", "", 0, "", fmt.Errorf("session ID parsing not yet implemented")
}
