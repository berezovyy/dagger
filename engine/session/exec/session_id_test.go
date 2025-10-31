package exec

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateSessionID(t *testing.T) {
	containerID := "container-123"
	execID := "exec-456"

	sessionID, err := generateSessionID(containerID, execID)
	if err != nil {
		t.Fatalf("generateSessionID failed: %v", err)
	}

	// Verify format
	if !strings.HasPrefix(sessionID, containerID+":"+execID+":") {
		t.Errorf("Session ID does not have expected prefix: %s", sessionID)
	}

	// Verify contains timestamp and random part
	parts := strings.Split(sessionID, ":")
	if len(parts) != 3 {
		t.Errorf("Session ID should have 3 colon-separated parts, got %d", len(parts))
	}

	if !strings.Contains(parts[2], "-") {
		t.Errorf("Timestamp-random part should contain hyphen: %s", parts[2])
	}

	t.Logf("Generated session ID: %s", sessionID)
}

func TestGenerateSessionIDUniqueness(t *testing.T) {
	containerID := "container-123"
	execID := "exec-456"

	// Generate many session IDs
	const iterations = 10000
	seen := make(map[string]bool, iterations)

	for i := 0; i < iterations; i++ {
		sessionID, err := generateSessionID(containerID, execID)
		if err != nil {
			t.Fatalf("generateSessionID failed at iteration %d: %v", i, err)
		}

		if seen[sessionID] {
			t.Errorf("Duplicate session ID detected: %s", sessionID)
		}
		seen[sessionID] = true
	}

	t.Logf("Generated %d unique session IDs", len(seen))
}

func TestSessionIndexLookup(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Register with generated session ID
	instance1, err := registry.Register("container-1", "exec-1", "", false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	sessionID1 := instance1.GetSessionID()
	if sessionID1 == "" {
		t.Fatal("Session ID should not be empty")
	}

	// Lookup by session ID
	found, err := registry.GetInstanceBySessionID(sessionID1)
	if err != nil {
		t.Fatalf("GetInstanceBySessionID failed: %v", err)
	}

	if found != instance1 {
		t.Errorf("Retrieved different instance")
	}

	// Register another instance
	instance2, err := registry.Register("container-2", "exec-2", "", false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	sessionID2 := instance2.GetSessionID()
	if sessionID2 == sessionID1 {
		t.Errorf("Session IDs should be unique")
	}

	// Lookup by container:exec
	found2, err := registry.GetInstance("container-2", "exec-2")
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}

	if found2 != instance2 {
		t.Errorf("Retrieved different instance via GetInstance")
	}

	// Verify session count
	count := registry.GetSessionCount()
	if count != 2 {
		t.Errorf("Expected session count 2, got %d", count)
	}

	// Cleanup
	if err := registry.Unregister("container-1", "exec-1"); err != nil {
		t.Errorf("Unregister failed: %v", err)
	}

	// Verify removed from session index
	_, err = registry.GetInstanceBySessionID(sessionID1)
	if err == nil {
		t.Errorf("Session should not be found after unregister")
	}

	// Verify session count decreased
	count = registry.GetSessionCount()
	if count != 1 {
		t.Errorf("Expected session count 1 after unregister, got %d", count)
	}
}

func TestSessionIDCollisionDetection(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Register with explicit session ID
	explicitSessionID := "container:exec:12345-abcdef"
	_, err := registry.Register("container-1", "exec-1", explicitSessionID, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Try to register another instance with same session ID
	_, err = registry.Register("container-2", "exec-2", explicitSessionID, false)
	if err == nil {
		t.Errorf("Expected error for session ID collision")
	}

	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("Error should mention collision: %v", err)
	}
}
