package exec

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCleanupOrphanTimeout(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create instance
	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Add and remove a client to set lastClientDisconnect
	instance.AddClient("client1", false, 0)
	instance.RemoveClient("client1")

	// Backdate the disconnect time
	instance.lastClientDisconnect = time.Now().Add(-SessionOrphanTimeout - time.Minute)

	// Run cleanup
	registry.cleanupExitedInstances()

	// Instance should be cleaned up
	_, err = registry.GetInstance("container1", "exec1")
	if err == nil {
		t.Error("Instance should have been cleaned up due to orphan timeout")
	}
}

func TestCleanupMaxIdleTimeout(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Backdate last activity
	instance.lastActivity = time.Now().Add(-SessionMaxIdleTimeout - time.Minute)

	// Run cleanup
	registry.cleanupExitedInstances()

	// Instance should be cleaned up
	_, err = registry.GetInstance("container1", "exec1")
	if err == nil {
		t.Error("Instance should have been cleaned up due to max idle timeout")
	}
}

func TestResourceLimitPerContainer(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create max sessions for a container
	for i := 0; i < MaxSessionsPerContainer; i++ {
		_, err := registry.Register("container1", fmt.Sprintf("exec%d", i), "", false)
		if err != nil {
			t.Fatalf("Register %d failed: %v", i, err)
		}
	}

	// Next registration should fail
	_, err := registry.Register("container1", "exec-overflow", "", false)
	if err == nil {
		t.Error("Expected error for exceeding per-container limit")
	}
	if !strings.Contains(err.Error(), "maximum number of sessions for container") {
		t.Errorf("Wrong error message: %v", err)
	}

	// Different container should still work
	_, err = registry.Register("container2", "exec1", "", false)
	if err != nil {
		t.Errorf("Different container should not be affected: %v", err)
	}
}

func TestResourceLimitGlobal(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create sessions up to global limit
	// Use multiple containers to avoid per-container limit
	containersNeeded := (MaxSessionsGlobal / MaxSessionsPerContainer) + 1

	sessionsCreated := 0
	for c := 0; c < containersNeeded && sessionsCreated < MaxSessionsGlobal; c++ {
		for e := 0; e < MaxSessionsPerContainer && sessionsCreated < MaxSessionsGlobal; e++ {
			_, err := registry.Register(
				fmt.Sprintf("container%d", c),
				fmt.Sprintf("exec%d", e),
				"", false)
			if err != nil {
				t.Fatalf("Register failed at %d: %v", sessionsCreated, err)
			}
			sessionsCreated++
		}
	}

	// Verify we hit the limit
	if registry.GetSessionCount() != MaxSessionsGlobal {
		t.Errorf("Expected %d sessions, got %d", MaxSessionsGlobal, registry.GetSessionCount())
	}

	// Next registration should fail
	_, err := registry.Register("overflow-container", "overflow-exec", "", false)
	if err == nil {
		t.Error("Expected error for exceeding global limit")
	}
	if !strings.Contains(err.Error(), "maximum number of sessions reached") {
		t.Errorf("Wrong error message: %v", err)
	}
}

func TestCleanupExitedWithRetention(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create and exit an instance
	instance, err := registry.Register("container1", "exec1", "", false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Add and remove client
	instance.AddClient("client1", false, 0)
	instance.RemoveClient("client1")

	// Mark as exited
	instance.MarkExited(0)

	// Should not be cleaned up immediately
	registry.cleanupExitedInstances()
	_, err = registry.GetInstance("container1", "exec1")
	if err != nil {
		t.Error("Instance should not be cleaned up immediately after exit")
	}

	// Backdate the exit time
	now := time.Now().Add(-SessionOutputRetentionTimeout - time.Minute)
	instance.exitTime = &now

	// Now should be cleaned up
	registry.cleanupExitedInstances()
	_, err = registry.GetInstance("container1", "exec1")
	if err == nil {
		t.Error("Instance should have been cleaned up after retention timeout")
	}
}

func TestCleanupNotAppliedWithActiveClients(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create instance with client
	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Add client
	instance.AddClient("client1", false, 0)

	// Backdate last activity (would normally trigger idle timeout)
	instance.lastActivity = time.Now().Add(-SessionMaxIdleTimeout - time.Minute)

	// Run cleanup
	registry.cleanupExitedInstances()

	// Instance should NOT be cleaned up (has active client)
	_, err = registry.GetInstance("container1", "exec1")
	if err != nil {
		t.Error("Instance should not be cleaned up while it has active clients")
	}
}

func TestGetContainerSessionCount(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create sessions for multiple containers
	for i := 0; i < 3; i++ {
		_, err := registry.Register("container1", fmt.Sprintf("exec%d", i), "", false)
		if err != nil {
			t.Fatalf("Register failed: %v", err)
		}
	}

	for i := 0; i < 5; i++ {
		_, err := registry.Register("container2", fmt.Sprintf("exec%d", i), "", false)
		if err != nil {
			t.Fatalf("Register failed: %v", err)
		}
	}

	// Check counts
	count1 := registry.GetContainerSessionCount("container1")
	if count1 != 3 {
		t.Errorf("Expected 3 sessions for container1, got %d", count1)
	}

	count2 := registry.GetContainerSessionCount("container2")
	if count2 != 5 {
		t.Errorf("Expected 5 sessions for container2, got %d", count2)
	}

	// Check non-existent container
	count3 := registry.GetContainerSessionCount("container3")
	if count3 != 0 {
		t.Errorf("Expected 0 sessions for container3, got %d", count3)
	}

	// Check total
	total := registry.GetSessionCount()
	if total != 8 {
		t.Errorf("Expected 8 total sessions, got %d", total)
	}
}

func TestCleanupPolicyPriority(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create an exited instance
	instance, err := registry.Register("container1", "exec1", "", false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Add and remove client
	instance.AddClient("client1", false, 0)
	instance.RemoveClient("client1")

	// Mark as exited and backdate
	instance.MarkExited(0)
	exitTime := time.Now().Add(-SessionOutputRetentionTimeout - time.Minute)
	instance.exitTime = &exitTime

	// Also backdate activity (would trigger idle timeout)
	instance.lastActivity = time.Now().Add(-SessionMaxIdleTimeout - time.Minute)

	// Run cleanup - should clean up due to output retention timeout
	registry.cleanupExitedInstances()

	// Instance should be cleaned up
	_, err = registry.GetInstance("container1", "exec1")
	if err == nil {
		t.Error("Instance should have been cleaned up")
	}
}
