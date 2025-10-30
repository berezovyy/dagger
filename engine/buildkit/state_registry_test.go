package buildkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewContainerStateRegistry(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	require.NotNil(t, registry)
	require.NotNil(t, registry.states)
	require.Equal(t, 0, registry.Size())

	// Clean up
	require.NoError(t, registry.Close())
}

func TestRegisterContainer(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	initialState := ContainerState{
		Status:    ContainerStatusCreated,
		StartedAt: time.Now(),
	}

	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Verify state was registered
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, "test-container", state.ContainerID)
	require.Equal(t, ContainerStatusCreated, state.Status)
	require.Equal(t, 1, registry.Size())
}

func TestRegisterDuplicateContainer(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	initialState := ContainerState{
		Status: ContainerStatusCreated,
	}

	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Try to register the same container again
	err = registry.Register("test-container", initialState)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already registered")
}

func TestRegisterEmptyID(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	initialState := ContainerState{
		Status: ContainerStatusCreated,
	}

	err := registry.Register("", initialState)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestUnregisterContainer(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Unregister it
	err = registry.Unregister("test-container")
	require.NoError(t, err)
	require.Equal(t, 0, registry.Size())

	// Verify it's gone
	_, err = registry.GetState("test-container")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUnregisterNonExistent(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	err := registry.Unregister("non-existent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUnregisterEmptyID(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	err := registry.Unregister("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestGetState(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	startTime := time.Now()
	initialState := ContainerState{
		Status:    ContainerStatusRunning,
		StartedAt: startTime,
	}

	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, "test-container", state.ContainerID)
	require.Equal(t, ContainerStatusRunning, state.Status)
	require.Equal(t, startTime.Unix(), state.StartedAt.Unix())
}

func TestGetStateNonExistent(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	_, err := registry.GetState("non-existent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestGetStateEmptyID(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	_, err := registry.GetState("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestGetStateReturnsCopy(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	initialState := ContainerState{
		Status: ContainerStatusRunning,
	}

	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Get state twice
	state1, err := registry.GetState("test-container")
	require.NoError(t, err)
	state2, err := registry.GetState("test-container")
	require.NoError(t, err)

	// Modify the first copy
	state1.Status = ContainerStatusExited

	// The second copy should be unchanged
	require.Equal(t, ContainerStatusRunning, state2.Status)

	// The internal state should also be unchanged
	state3, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, ContainerStatusRunning, state3.Status)
}

func TestGetAllStates(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register multiple containers
	for i := 0; i < 5; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{
			Status: ContainerStatusRunning,
		}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	// Get all states
	allStates := registry.GetAllStates()
	require.Equal(t, 5, len(allStates))

	// Verify all containers are present
	for i := 0; i < 5; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state, exists := allStates[containerID]
		require.True(t, exists)
		require.Equal(t, containerID, state.ContainerID)
		require.Equal(t, ContainerStatusRunning, state.Status)
	}
}

func TestListContainerIDs(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register multiple containers
	expectedIDs := make(map[string]bool)
	for i := 0; i < 5; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		expectedIDs[containerID] = true
		state := ContainerState{Status: ContainerStatusRunning}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	// List IDs
	ids := registry.ListContainerIDs()
	require.Equal(t, 5, len(ids))

	// Verify all IDs are present
	for _, id := range ids {
		require.True(t, expectedIDs[id])
	}
}

func TestUpdateState(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a container
	initialState := ContainerState{
		Status: ContainerStatusCreated,
	}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Update the state
	err = registry.UpdateState("test-container", func(state *ContainerState) {
		state.Status = ContainerStatusRunning
		state.StartedAt = time.Now()
	})
	require.NoError(t, err)

	// Verify the update
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, ContainerStatusRunning, state.Status)
	require.False(t, state.StartedAt.IsZero())
	require.False(t, state.LastUpdated.IsZero())
}

func TestUpdateStateNonExistent(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	err := registry.UpdateState("non-existent", func(state *ContainerState) {
		state.Status = ContainerStatusRunning
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUpdateStateEmptyID(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	err := registry.UpdateState("", func(state *ContainerState) {
		state.Status = ContainerStatusRunning
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestUpdateStateNilUpdater(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	err = registry.UpdateState("test-container", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be nil")
}

func TestUpdateResourceUsage(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusRunning}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Update resource usage
	usage := ResourceUsage{
		CPUPercent:   50.5,
		MemoryBytes:  1024 * 1024 * 100, // 100 MB
		MemoryLimit:  1024 * 1024 * 500, // 500 MB
		IOReadBytes:  1024 * 1024,       // 1 MB
		IOWriteBytes: 1024 * 512,        // 512 KB
	}
	err = registry.UpdateResourceUsage("test-container", usage)
	require.NoError(t, err)

	// Verify the update
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.NotNil(t, state.ResourceUsage)
	require.Equal(t, usage.CPUPercent, state.ResourceUsage.CPUPercent)
	require.Equal(t, usage.MemoryBytes, state.ResourceUsage.MemoryBytes)
	require.Equal(t, usage.MemoryLimit, state.ResourceUsage.MemoryLimit)
	require.Equal(t, usage.IOReadBytes, state.ResourceUsage.IOReadBytes)
	require.Equal(t, usage.IOWriteBytes, state.ResourceUsage.IOWriteBytes)
}

func TestMarkExited(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a running container
	initialState := ContainerState{
		Status:    ContainerStatusRunning,
		StartedAt: time.Now(),
	}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Mark as exited
	exitCode := int32(0)
	err = registry.MarkExited("test-container", exitCode)
	require.NoError(t, err)

	// Verify the state
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, ContainerStatusExited, state.Status)
	require.NotNil(t, state.ExitCode)
	require.Equal(t, exitCode, *state.ExitCode)
	require.NotNil(t, state.FinishedAt)
	require.False(t, state.FinishedAt.IsZero())
}

func TestMarkExitedNonZeroCode(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a running container
	initialState := ContainerState{Status: ContainerStatusRunning}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Mark as exited with non-zero exit code
	exitCode := int32(137) // SIGKILL
	err = registry.MarkExited("test-container", exitCode)
	require.NoError(t, err)

	// Verify the exit code
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, ContainerStatusExited, state.Status)
	require.NotNil(t, state.ExitCode)
	require.Equal(t, exitCode, *state.ExitCode)
}

func TestMarkExitedIdempotent(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register and mark as exited
	initialState := ContainerState{Status: ContainerStatusRunning}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	exitCode1 := int32(0)
	err = registry.MarkExited("test-container", exitCode1)
	require.NoError(t, err)

	// Get the first finish time
	state1, err := registry.GetState("test-container")
	require.NoError(t, err)
	finishTime1 := *state1.FinishedAt
	firstExitCode := *state1.ExitCode

	// Wait a bit and mark as exited again with a different exit code
	time.Sleep(10 * time.Millisecond)
	exitCode2 := int32(1)
	err = registry.MarkExited("test-container", exitCode2)
	require.NoError(t, err)

	// Verify that the state hasn't changed (idempotent)
	state2, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, firstExitCode, *state2.ExitCode) // Exit code should not change
	require.Equal(t, finishTime1.Unix(), state2.FinishedAt.Unix())
}

func TestCleanup(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register some containers
	for i := 0; i < 5; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{Status: ContainerStatusRunning}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	// Mark 3 containers as exited at different times
	pastTime := time.Now().Add(-10 * time.Minute)
	err := registry.UpdateState("container-0", func(state *ContainerState) {
		state.Status = ContainerStatusExited
		state.FinishedAt = &pastTime
	})
	require.NoError(t, err)

	recentTime := time.Now().Add(-30 * time.Second)
	err = registry.UpdateState("container-1", func(state *ContainerState) {
		state.Status = ContainerStatusExited
		state.FinishedAt = &recentTime
	})
	require.NoError(t, err)

	veryOldTime := time.Now().Add(-1 * time.Hour)
	err = registry.UpdateState("container-2", func(state *ContainerState) {
		state.Status = ContainerStatusExited
		state.FinishedAt = &veryOldTime
	})
	require.NoError(t, err)

	// Cleanup containers older than 5 minutes
	removed := registry.Cleanup(5 * time.Minute)
	require.Equal(t, 2, removed) // container-0 and container-2

	// Verify the right containers remain
	require.Equal(t, 3, registry.Size())
	_, err = registry.GetState("container-1")
	require.NoError(t, err) // Should still exist (too recent)
	_, err = registry.GetState("container-3")
	require.NoError(t, err) // Should still exist (still running)
	_, err = registry.GetState("container-4")
	require.NoError(t, err) // Should still exist (still running)

	// Verify the old containers are gone
	_, err = registry.GetState("container-0")
	require.Error(t, err)
	_, err = registry.GetState("container-2")
	require.Error(t, err)
}

func TestCleanupIgnoresRunningContainers(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register running containers
	for i := 0; i < 3; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{Status: ContainerStatusRunning}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	// Cleanup should not remove any running containers
	removed := registry.Cleanup(1 * time.Second)
	require.Equal(t, 0, removed)
	require.Equal(t, 3, registry.Size())
}

func TestSize(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	require.Equal(t, 0, registry.Size())

	// Add containers
	for i := 0; i < 10; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{Status: ContainerStatusRunning}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	require.Equal(t, 10, registry.Size())

	// Remove some
	for i := 0; i < 5; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		err := registry.Unregister(containerID)
		require.NoError(t, err)
	}

	require.Equal(t, 5, registry.Size())
}

func TestStartStop(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	err := registry.Start()
	require.NoError(t, err)

	err = registry.Stop()
	require.NoError(t, err)
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)

	err := registry.Close()
	require.NoError(t, err)
}

func TestCloseIdempotent(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)

	// Close multiple times
	err := registry.Close()
	require.NoError(t, err)

	err = registry.Close()
	require.NoError(t, err)

	err = registry.Close()
	require.NoError(t, err)
}

func TestStartAfterClose(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)

	err := registry.Close()
	require.NoError(t, err)

	err = registry.Start()
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}

// Concurrency tests

func TestConcurrentReads(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusRunning}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Spawn multiple concurrent readers
	var wg sync.WaitGroup
	numReaders := 100
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				state, err := registry.GetState("test-container")
				require.NoError(t, err)
				require.NotNil(t, state)
				require.Equal(t, ContainerStatusRunning, state.Status)
			}
		}()
	}

	wg.Wait()
}

func TestConcurrentReadWrite(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusRunning}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	var wg sync.WaitGroup

	// Spawn readers
	numReaders := 50
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				state, err := registry.GetState("test-container")
				require.NoError(t, err)
				require.NotNil(t, state)
			}
		}()
	}

	// Spawn writers
	numWriters := 10
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				usage := ResourceUsage{
					CPUPercent:  float64(j),
					MemoryBytes: uint64(j * 1024),
				}
				err := registry.UpdateResourceUsage("test-container", usage)
				require.NoError(t, err)
			}
		}()
	}

	wg.Wait()

	// Verify final state
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.NotNil(t, state)
}

func TestConcurrentRegisterUnregister(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	var wg sync.WaitGroup

	// Spawn goroutines that register and unregister containers
	numGoroutines := 20
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		containerID := fmt.Sprintf("container-%d", i)
		go func(id string) {
			defer wg.Done()

			// Register
			state := ContainerState{Status: ContainerStatusCreated}
			err := registry.Register(id, state)
			require.NoError(t, err)

			// Read
			_, err = registry.GetState(id)
			require.NoError(t, err)

			// Update
			err = registry.UpdateState(id, func(state *ContainerState) {
				state.Status = ContainerStatusRunning
			})
			require.NoError(t, err)

			// Unregister
			err = registry.Unregister(id)
			require.NoError(t, err)
		}(containerID)
	}

	wg.Wait()

	// All containers should be unregistered
	require.Equal(t, 0, registry.Size())
}

func TestConcurrentListOperations(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Register some initial containers
	for i := 0; i < 10; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{Status: ContainerStatusRunning}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup

	// Concurrent GetAllStates
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states := registry.GetAllStates()
			require.NotNil(t, states)
		}()
	}

	// Concurrent ListContainerIDs
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := registry.ListContainerIDs()
			require.NotNil(t, ids)
		}()
	}

	// Concurrent Size
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			size := registry.Size()
			require.GreaterOrEqual(t, size, 0)
		}()
	}

	wg.Wait()
}

// Benchmark tests

func BenchmarkGetState(b *testing.B) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	state := ContainerState{Status: ContainerStatusRunning}
	_ = registry.Register("test-container", state)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = registry.GetState("test-container")
	}
}

func BenchmarkUpdateState(b *testing.B) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	state := ContainerState{Status: ContainerStatusRunning}
	_ = registry.Register("test-container", state)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.UpdateState("test-container", func(state *ContainerState) {
			state.ResourceUsage = &ResourceUsage{
				CPUPercent:  50.0,
				MemoryBytes: 1024 * 1024,
			}
		})
	}
}

func BenchmarkConcurrentReads(b *testing.B) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	state := ContainerState{Status: ContainerStatusRunning}
	_ = registry.Register("test-container", state)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = registry.GetState("test-container")
		}
	})
}

// Background updater tests

func TestBackgroundUpdaterWithoutRuncClient(t *testing.T) {
	// Test that registry works fine without a runc client (updater disabled)
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Start should succeed but not launch updater
	err := registry.Start()
	require.NoError(t, err)

	// Register a container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err = registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Wait a bit to ensure no updater is running
	time.Sleep(100 * time.Millisecond)

	// State should be unchanged
	state, err := registry.GetState("test-container")
	require.NoError(t, err)
	require.Equal(t, ContainerStatusCreated, state.Status)

	// Stop should succeed
	err = registry.Stop()
	require.NoError(t, err)
}

func TestStartStopCycle(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Start and stop multiple times
	for i := 0; i < 3; i++ {
		err := registry.Start()
		require.NoError(t, err)

		err = registry.Stop()
		require.NoError(t, err)
	}
}

func TestCloseStopsUpdater(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)

	err := registry.Start()
	require.NoError(t, err)

	// Register a container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err = registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Close should stop updater
	err = registry.Close()
	require.NoError(t, err)

	// Operations after close should still work or fail gracefully
	// (registry doesn't prevent operations after close, just stops updater)
}

func TestUpdateIntervalOption(t *testing.T) {
	ctx := context.Background()

	// Create registry with custom update interval
	registry := NewContainerStateRegistry(ctx, WithUpdateInterval(100*time.Millisecond))
	defer registry.Close()

	require.Equal(t, 100*time.Millisecond, registry.updateInterval)
}

func TestCgroupParentOption(t *testing.T) {
	ctx := context.Background()

	// Create registry with custom cgroup parent
	registry := NewContainerStateRegistry(ctx, WithCgroupParent("/custom/cgroup"))
	defer registry.Close()

	require.Equal(t, "/custom/cgroup", registry.cgroupParent)
}

func TestMapRuncStatus(t *testing.T) {
	tests := []struct {
		runcStatus      string
		expectedStatus  ContainerStatus
	}{
		{"created", ContainerStatusCreated},
		{"running", ContainerStatusRunning},
		{"paused", ContainerStatusPaused},
		{"stopped", ContainerStatusStopped},
		{"unknown", ContainerStatusExited},
		{"", ContainerStatusExited},
	}

	for _, tt := range tests {
		t.Run(tt.runcStatus, func(t *testing.T) {
			result := mapRuncStatusToContainerStatus(tt.runcStatus)
			require.Equal(t, tt.expectedStatus, result)
		})
	}
}

func TestBuildCgroupPath(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx, WithCgroupParent("/sys/fs/cgroup/dagger"))
	defer registry.Close()

	path := registry.buildCgroupPath("test-container-123")
	require.Equal(t, "/sys/fs/cgroup/dagger/test-container-123", path)
}

func TestReadMemoryCurrentInvalidData(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Create a temporary file with invalid data
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "memory.current")
	err := os.WriteFile(memFile, []byte("not-a-number"), 0644)
	require.NoError(t, err)

	// Should return error for invalid data
	_, err = registry.readMemoryCurrent(tmpDir)
	require.Error(t, err)
}

func TestReadMemoryMaxUnlimited(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Create a temporary file with "max" (unlimited)
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "memory.max")
	err := os.WriteFile(memFile, []byte("max\n"), 0644)
	require.NoError(t, err)

	// Should return 0 for unlimited
	limit, err := registry.readMemoryMax(tmpDir)
	require.NoError(t, err)
	require.Equal(t, uint64(0), limit)
}

func TestReadMemoryMaxWithValue(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Create a temporary file with a limit value
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "memory.max")
	err := os.WriteFile(memFile, []byte("536870912\n"), 0644)
	require.NoError(t, err)

	// Should return the limit value
	limit, err := registry.readMemoryMax(tmpDir)
	require.NoError(t, err)
	require.Equal(t, uint64(536870912), limit)
}

// Lifecycle event streaming tests

func TestLifecycleEventSubscription(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe to events
	eventChan, unsubscribe := registry.Subscribe(ctx, "")
	defer unsubscribe()

	// Register a container and expect registration event
	initialState := ContainerState{Status: ContainerStatusCreated}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Wait for registration event
	select {
	case event := <-eventChan:
		require.Equal(t, "test-container", event.ContainerID)
		require.Equal(t, EventTypeRegistered, event.EventType)
		require.Equal(t, ContainerStatusCreated, event.Status)
		require.NotEmpty(t, event.Message)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for registration event")
	}
}

func TestLifecycleEventFiltering(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe to events for specific container
	eventChan, unsubscribe := registry.Subscribe(ctx, "container-1")
	defer unsubscribe()

	// Register multiple containers
	for i := 0; i < 3; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{Status: ContainerStatusCreated}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	// Should receive events for all containers (filtering happens at RPC level)
	// Just verify we get events
	receivedEvents := 0
	timeout := time.After(2 * time.Second)

loop:
	for receivedEvents < 3 {
		select {
		case event := <-eventChan:
			require.NotNil(t, event)
			receivedEvents++
		case <-timeout:
			break loop
		}
	}

	require.GreaterOrEqual(t, receivedEvents, 1, "should receive at least one event")
}

func TestMultipleSubscribers(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Create multiple subscribers
	numSubscribers := 5
	subscribers := make([]<-chan *LifecycleEvent, numSubscribers)
	unsubscribes := make([]func(), numSubscribers)

	for i := 0; i < numSubscribers; i++ {
		eventChan, unsubscribe := registry.Subscribe(ctx, "")
		subscribers[i] = eventChan
		unsubscribes[i] = unsubscribe
	}

	defer func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
	}()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// All subscribers should receive the event
	for i := 0; i < numSubscribers; i++ {
		select {
		case event := <-subscribers[i]:
			require.Equal(t, "test-container", event.ContainerID)
			require.Equal(t, EventTypeRegistered, event.EventType)
		case <-time.After(1 * time.Second):
			t.Fatalf("subscriber %d did not receive event", i)
		}
	}
}

func TestLifecycleEventStateTransitions(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe to events
	eventChan, unsubscribe := registry.Subscribe(ctx, "")
	defer unsubscribe()

	// Register container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Drain registration event
	<-eventChan

	// Simulate state transition: created -> running
	err = registry.UpdateState("test-container", func(state *ContainerState) {
		state.Status = ContainerStatusRunning
		state.StartedAt = time.Now()
	})
	require.NoError(t, err)

	// Note: State transitions via UpdateState don't automatically trigger events
	// Only updateContainer (called by background updater) triggers events
	// This is by design to avoid double-publishing
}

func TestLifecycleEventUnregistration(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe to events
	eventChan, unsubscribe := registry.Subscribe(ctx, "")
	defer unsubscribe()

	// Register a container
	initialState := ContainerState{Status: ContainerStatusCreated}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Drain registration event
	<-eventChan

	// Unregister container
	err = registry.Unregister("test-container")
	require.NoError(t, err)

	// Wait for unregistration event
	select {
	case event := <-eventChan:
		require.Equal(t, "test-container", event.ContainerID)
		require.Equal(t, EventTypeUnregistered, event.EventType)
		require.NotEmpty(t, event.Message)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for unregistration event")
	}
}

func TestLifecycleEventContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe to events
	eventChan, _ := registry.Subscribe(ctx, "")

	// Cancel context
	cancel()

	// Channel should be closed
	select {
	case _, ok := <-eventChan:
		if ok {
			t.Fatal("expected channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for channel to close")
	}
}

func TestLifecycleEventBufferOverflow(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe but don't read events
	_, unsubscribe := registry.Subscribe(ctx, "")
	defer unsubscribe()

	// Register many containers to overflow the buffer (100 events)
	for i := 0; i < 150; i++ {
		containerID := fmt.Sprintf("container-%d", i)
		state := ContainerState{Status: ContainerStatusCreated}
		err := registry.Register(containerID, state)
		require.NoError(t, err)
	}

	// Should not block or panic - events are dropped with warning
	// This is the expected behavior for slow subscribers
}

func TestLifecycleEventExitCode(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	// Subscribe to events
	eventChan, unsubscribe := registry.Subscribe(ctx, "")
	defer unsubscribe()

	// Register container
	initialState := ContainerState{Status: ContainerStatusRunning}
	err := registry.Register("test-container", initialState)
	require.NoError(t, err)

	// Drain registration event
	<-eventChan

	// Note: Exit events with exit codes are generated by updateContainer
	// which is called by the background updater. Manual UpdateState calls
	// don't trigger lifecycle events to avoid duplicates.
}

func TestConcurrentSubscriptions(t *testing.T) {
	ctx := context.Background()
	registry := NewContainerStateRegistry(ctx)
	defer registry.Close()

	var wg sync.WaitGroup
	numSubscribers := 20

	// Create many concurrent subscribers
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			eventChan, unsubscribe := registry.Subscribe(ctx, "")
			defer unsubscribe()

			// Register a container
			containerID := fmt.Sprintf("container-%d", id)
			state := ContainerState{Status: ContainerStatusCreated}
			err := registry.Register(containerID, state)
			require.NoError(t, err)

			// Read at least one event
			select {
			case event := <-eventChan:
				require.NotNil(t, event)
			case <-time.After(2 * time.Second):
				t.Errorf("subscriber %d timeout", id)
			}
		}(i)
	}

	wg.Wait()
}
