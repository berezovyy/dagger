package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSessionManagerIntegration_ServiceDiscovery tests that clients can discover
// the exec service after session start through gRPC service registration.
func TestSessionManagerIntegration_ServiceDiscovery(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Create a mock gRPC server
	server := grpc.NewServer()
	defer server.Stop()

	// Register the exec service
	exec.Register(server)

	// Verify service is registered by checking the service info
	info := server.GetServiceInfo()
	if _, exists := info["dagger.exec.Exec"]; !exists {
		t.Fatal("ExecServer not registered with gRPC server")
	}

	// Verify the service has the expected methods
	serviceInfo := info["dagger.exec.Exec"]
	expectedMethods := []string{
		"Session",
		"ContainerLifecycle",
		"ContainerStatus",
		"ContainerPause",
		"ContainerResume",
		"ContainerSignal",
	}

	for _, method := range expectedMethods {
		found := false
		for _, m := range serviceInfo.Methods {
			if m.Name == method {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected method %s not found in service", method)
		}
	}
}

// TestSessionManagerIntegration_MultipleContainerStreaming tests that multiple
// containers can stream their output through the same ExecAttachable instance.
func TestSessionManagerIntegration_MultipleContainerStreaming(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Register multiple exec instances for different containers
	containers := []struct {
		containerID string
		execID      string
		output      string
		exitCode    int32
	}{
		{"container-1", "exec-1", "output from container 1\n", 0},
		{"container-2", "exec-2", "output from container 2\n", 0},
		{"container-3", "exec-3", "output from container 3\n", 1},
	}

	// Register all containers
	for _, c := range containers {
		err := exec.RegisterExecution(c.containerID, c.execID, false)
		require.NoError(t, err, "failed to register %s/%s", c.containerID, c.execID)
	}

	// Write output to all containers concurrently
	var wg sync.WaitGroup
	for _, c := range containers {
		wg.Add(1)
		go func(container struct {
			containerID string
			execID      string
			output      string
			exitCode    int32
		}) {
			defer wg.Done()

			// Get stdout writer
			stdout := exec.GetStdoutWriter(container.containerID, container.execID)
			require.NotNil(t, stdout, "stdout writer should not be nil")

			// Write output
			n, err := stdout.Write([]byte(container.output))
			require.NoError(t, err, "failed to write stdout")
			require.Equal(t, len(container.output), n, "written bytes mismatch")

			// Send exit code
			err = exec.SendExitCode(container.containerID, container.execID, container.exitCode)
			require.NoError(t, err, "failed to send exit code")
		}(c)
	}

	// Wait for all writes to complete
	wg.Wait()

	// Verify all instances exist in registry
	for _, c := range containers {
		instance, err := exec.registry.GetInstance(c.containerID, c.execID)
		require.NoError(t, err, "failed to get instance %s/%s", c.containerID, c.execID)
		require.NotNil(t, instance, "instance should not be nil")
		require.Equal(t, ExecStatusExited, instance.GetStatus(), "instance should be exited")
	}

	// Cleanup
	for _, c := range containers {
		err := exec.UnregisterExecution(c.containerID, c.execID)
		require.NoError(t, err, "failed to unregister %s/%s", c.containerID, c.execID)
	}
}

// TestSessionManagerIntegration_RegistrationLifecycle tests the full lifecycle
// of container registration and unregistration.
func TestSessionManagerIntegration_RegistrationLifecycle(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	containerID := "test-container"
	execID := "test-exec"

	// Test 1: Register execution
	err := exec.RegisterExecution(containerID, execID, false)
	require.NoError(t, err, "registration should succeed")

	// Test 2: Verify instance exists
	instance, err := exec.registry.GetInstance(containerID, execID)
	require.NoError(t, err, "instance should exist after registration")
	require.NotNil(t, instance, "instance should not be nil")
	require.Equal(t, ExecStatusPending, instance.GetStatus(), "initial status should be pending")

	// Test 3: Duplicate registration should fail
	err = exec.RegisterExecution(containerID, execID, false)
	require.Error(t, err, "duplicate registration should fail")
	require.Contains(t, err.Error(), "already registered", "error should mention already registered")

	// Test 4: Write some output
	stdout := exec.GetStdoutWriter(containerID, execID)
	require.NotNil(t, stdout, "stdout writer should be available")
	_, err = stdout.Write([]byte("test output\n"))
	require.NoError(t, err, "write should succeed")

	// Test 5: Send exit code
	err = exec.SendExitCode(containerID, execID, 0)
	require.NoError(t, err, "sending exit code should succeed")

	// Verify status changed to exited
	require.Equal(t, ExecStatusExited, instance.GetStatus(), "status should be exited")

	// Test 6: Unregister execution
	err = exec.UnregisterExecution(containerID, execID)
	require.NoError(t, err, "unregistration should succeed")

	// Test 7: Verify instance no longer exists
	_, err = exec.registry.GetInstance(containerID, execID)
	require.Error(t, err, "instance should not exist after unregistration")
	require.Contains(t, err.Error(), "not found", "error should mention not found")

	// Test 8: Unregistering again should fail
	err = exec.UnregisterExecution(containerID, execID)
	require.Error(t, err, "unregistering non-existent instance should fail")
}

// TestSessionManagerIntegration_ConcurrentExecutionMultiplexing tests that
// multiple concurrent executions are properly multiplexed through the registry.
func TestSessionManagerIntegration_ConcurrentExecutionMultiplexing(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	const numContainers = 10
	const numExecsPerContainer = 5

	var wg sync.WaitGroup
	var registrationErrors atomic.Int32
	var writeErrors atomic.Int32
	var exitErrors atomic.Int32

	// Launch concurrent registrations and writes
	for i := 0; i < numContainers; i++ {
		for j := 0; j < numExecsPerContainer; j++ {
			wg.Add(1)
			go func(containerIdx, execIdx int) {
				defer wg.Done()

				containerID := fmt.Sprintf("container-%d", containerIdx)
				execID := fmt.Sprintf("exec-%d", execIdx)

				// Register
				if err := exec.RegisterExecution(containerID, execID, false); err != nil {
					registrationErrors.Add(1)
					return
				}

				// Write stdout
				stdout := exec.GetStdoutWriter(containerID, execID)
				if stdout == nil {
					writeErrors.Add(1)
					return
				}

				data := fmt.Sprintf("output from %s/%s\n", containerID, execID)
				if _, err := stdout.Write([]byte(data)); err != nil {
					writeErrors.Add(1)
					return
				}

				// Write stderr
				stderr := exec.GetStderrWriter(containerID, execID)
				if stderr == nil {
					writeErrors.Add(1)
					return
				}

				errData := fmt.Sprintf("error from %s/%s\n", containerID, execID)
				if _, err := stderr.Write([]byte(errData)); err != nil {
					writeErrors.Add(1)
					return
				}

				// Send exit code
				exitCode := int32(containerIdx + execIdx)
				if err := exec.SendExitCode(containerID, execID, exitCode); err != nil {
					exitErrors.Add(1)
					return
				}
			}(i, j)
		}
	}

	// Wait for all operations to complete
	wg.Wait()

	// Verify no errors occurred
	require.Equal(t, int32(0), registrationErrors.Load(), "no registration errors should occur")
	require.Equal(t, int32(0), writeErrors.Load(), "no write errors should occur")
	require.Equal(t, int32(0), exitErrors.Load(), "no exit code errors should occur")

	// Verify all instances are registered
	for i := 0; i < numContainers; i++ {
		for j := 0; j < numExecsPerContainer; j++ {
			containerID := fmt.Sprintf("container-%d", i)
			execID := fmt.Sprintf("exec-%d", j)

			instance, err := exec.registry.GetInstance(containerID, execID)
			require.NoError(t, err, "instance %s/%s should exist", containerID, execID)
			require.Equal(t, ExecStatusExited, instance.GetStatus(),
				"instance %s/%s should be exited", containerID, execID)
		}
	}

	// Cleanup all instances
	for i := 0; i < numContainers; i++ {
		for j := 0; j < numExecsPerContainer; j++ {
			containerID := fmt.Sprintf("container-%d", i)
			execID := fmt.Sprintf("exec-%d", j)
			err := exec.UnregisterExecution(containerID, execID)
			require.NoError(t, err, "cleanup should succeed for %s/%s", containerID, execID)
		}
	}
}

// TestSessionManagerIntegration_InvalidContainerIDHandling tests error handling
// for invalid container IDs in session operations.
func TestSessionManagerIntegration_InvalidContainerIDHandling(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Test with mock gRPC stream
	stream := &mockSessionStream{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 1),
		sendChan: make(chan *SessionResponse, 10),
	}

	testCases := []struct {
		name        string
		containerID string
		execID      string
		expectCode  codes.Code
	}{
		{
			name:        "empty_container_id",
			containerID: "",
			execID:      "exec-1",
			expectCode:  codes.InvalidArgument,
		},
		{
			name:        "empty_exec_id",
			containerID: "container-1",
			execID:      "",
			expectCode:  codes.InvalidArgument,
		},
		// NOTE: "non_existent_instance" test case removed because Session() now
		// auto-creates instances if they don't exist (new behavior for reconnection support)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Send Start request
			stream.recvChan <- &SessionRequest{
				Msg: &SessionRequest_Start{
					Start: &Start{
						ContainerId: tc.containerID,
						ExecId:      tc.execID,
					},
				},
			}

			// Call Session (should return error)
			err := exec.Session(stream)
			require.Error(t, err, "Session should return error")

			// Verify error code
			st, ok := status.FromError(err)
			require.True(t, ok, "error should be a gRPC status error")
			require.Equal(t, tc.expectCode, st.Code(),
				"error code should be %v", tc.expectCode)
		})
	}
}

// TestSessionManagerIntegration_ClientDisconnectIsolation tests that one client
// disconnecting doesn't affect other clients connected to the same exec instance.
func TestSessionManagerIntegration_ClientDisconnectIsolation(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	containerID := "test-container"
	execID := "test-exec"

	// Register execution
	err := exec.RegisterExecution(containerID, execID, false)
	require.NoError(t, err, "registration should succeed")

	// Get the instance
	instance, err := exec.registry.GetInstance(containerID, execID)
	require.NoError(t, err, "instance should exist")

	// Add multiple clients
	const numClients = 5
	clientIDs := make([]string, numClients)
	for i := 0; i < numClients; i++ {
		clientID := fmt.Sprintf("client-%d", i)
		clientIDs[i] = clientID
		err := instance.AddClient(clientID, false, 0)
		require.NoError(t, err, "AddClient should not return error")
		require.Equal(t, i+1, instance.GetClientCount(), "client count should increment")
	}

	// Verify all clients are connected
	require.Equal(t, numClients, instance.GetClientCount(), "all clients should be connected")

	// Disconnect one client
	disconnectedClient := clientIDs[2]
	remaining := instance.RemoveClient(disconnectedClient)
	require.Equal(t, numClients-1, remaining, "remaining client count should be correct")

	// Verify other clients are still connected
	clients := instance.GetClients()
	for _, clientID := range clientIDs {
		if clientID == disconnectedClient {
			_, exists := clients[clientID]
			require.False(t, exists, "disconnected client should not exist")
		} else {
			_, exists := clients[clientID]
			require.True(t, exists, "other clients should still be connected")
		}
	}

	// Write output - should still work for remaining clients
	stdout := exec.GetStdoutWriter(containerID, execID)
	require.NotNil(t, stdout, "stdout writer should still be available")
	_, err = stdout.Write([]byte("test output after disconnect\n"))
	require.NoError(t, err, "write should succeed after client disconnect")

	// Disconnect all remaining clients
	for _, clientID := range clientIDs {
		if clientID != disconnectedClient {
			instance.RemoveClient(clientID)
		}
	}

	// Verify no clients remain
	require.Equal(t, 0, instance.GetClientCount(), "no clients should remain")

	// Cleanup
	err = exec.UnregisterExecution(containerID, execID)
	require.NoError(t, err, "cleanup should succeed")
}

// TestSessionManagerIntegration_RegistryCleanupOnSessionEnd tests that the
// registry properly cleans up all instances when the session ends.
func TestSessionManagerIntegration_RegistryCleanupOnSessionEnd(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	// Register multiple executions
	containers := []struct {
		containerID string
		execID      string
	}{
		{"container-1", "exec-1"},
		{"container-2", "exec-2"},
		{"container-3", "exec-3"},
	}

	for _, c := range containers {
		err := exec.RegisterExecution(c.containerID, c.execID, false)
		require.NoError(t, err, "registration should succeed")

		// Write some data and mark as exited
		stdout := exec.GetStdoutWriter(c.containerID, c.execID)
		_, err = stdout.Write([]byte("test output\n"))
		require.NoError(t, err, "write should succeed")

		err = exec.SendExitCode(c.containerID, c.execID, 0)
		require.NoError(t, err, "send exit code should succeed")
	}

	// Verify all instances exist
	for _, c := range containers {
		instance, err := exec.registry.GetInstance(c.containerID, c.execID)
		require.NoError(t, err, "instance should exist before cleanup")
		require.NotNil(t, instance, "instance should not be nil")
	}

	// Close the exec attachable (simulates session end)
	err := exec.Close()
	require.NoError(t, err, "close should succeed")

	// Verify all instances are cleaned up
	for _, c := range containers {
		_, err := exec.registry.GetInstance(c.containerID, c.execID)
		require.Error(t, err, "instance should not exist after cleanup")
	}
}

// TestSessionManagerIntegration_AutomaticCleanupOfExitedInstances tests that
// the registry automatically cleans up exited instances after the retention period.
func TestSessionManagerIntegration_AutomaticCleanupOfExitedInstances(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping automatic cleanup test in short mode")
	}

	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	containerID := "test-container"
	execID := "test-exec"

	// Register and mark as exited
	err := exec.RegisterExecution(containerID, execID, false)
	require.NoError(t, err, "registration should succeed")

	instance, err := exec.registry.GetInstance(containerID, execID)
	require.NoError(t, err, "instance should exist")

	// Mark as exited with backdated exit time to simulate old exit
	instance.mu.Lock()
	instance.status = ExecStatusExited
	exitCode := int32(0)
	instance.exitCode = &exitCode
	oldTime := time.Now().Add(-10 * time.Minute) // Older than 5-minute retention
	instance.exitTime = &oldTime
	instance.mu.Unlock()

	// Manually trigger cleanup
	exec.registry.cleanupExitedInstances()

	// Verify instance was cleaned up
	_, err = exec.registry.GetInstance(containerID, execID)
	require.Error(t, err, "instance should be cleaned up after retention period")
	require.Contains(t, err.Error(), "not found", "error should indicate not found")
}

// TestSessionManagerIntegration_ExitedInstanceRetention tests that exited
// instances are retained for the configured period to allow late clients to connect.
func TestSessionManagerIntegration_ExitedInstanceRetention(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	containerID := "test-container"
	execID := "test-exec"

	// Register and mark as exited recently
	err := exec.RegisterExecution(containerID, execID, false)
	require.NoError(t, err, "registration should succeed")

	instance, err := exec.registry.GetInstance(containerID, execID)
	require.NoError(t, err, "instance should exist")

	// Mark as exited with recent time
	err = exec.SendExitCode(containerID, execID, 0)
	require.NoError(t, err, "send exit code should succeed")

	// Trigger cleanup - should NOT clean up recent exit
	exec.registry.cleanupExitedInstances()

	// Verify instance still exists
	instance, err = exec.registry.GetInstance(containerID, execID)
	require.NoError(t, err, "recently exited instance should still exist")
	require.Equal(t, ExecStatusExited, instance.GetStatus(), "status should be exited")

	// Cleanup manually
	err = exec.UnregisterExecution(containerID, execID)
	require.NoError(t, err, "manual cleanup should succeed")
}

// TestSessionManagerIntegration_StateRegistryIntegration tests integration
// with the container state registry for status queries.
func TestSessionManagerIntegration_StateRegistryIntegration(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Create and set mock state registry
	mockRegistry := &mockStateRegistry{
		states: map[string]*ContainerState{
			"running-container": {
				ContainerID: "running-container",
				Status:      "running",
				ExitCode:    nil,
				StartedAt:   time.Now().Add(-1 * time.Minute),
				FinishedAt:  nil,
				ResourceUsage: &ContainerResourceUsage{
					CPUPercent:   50.0,
					MemoryBytes:  1024 * 1024,
					MemoryLimit:  2048 * 1024,
					IOReadBytes:  512,
					IOWriteBytes: 1024,
				},
			},
		},
	}
	exec.SetStateRegistry(mockRegistry)

	// Test 1: Query status without registry (should fail)
	exec.SetStateRegistry(nil)
	_, err := exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId: "running-container",
	})
	require.Error(t, err, "query without registry should fail")
	require.Equal(t, codes.Unavailable, status.Code(err), "should return Unavailable")

	// Test 2: Query status with registry (should succeed)
	exec.SetStateRegistry(mockRegistry)
	resp, err := exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId:          "running-container",
		IncludeResourceUsage: true,
	})
	require.NoError(t, err, "query with registry should succeed")
	require.Equal(t, "running-container", resp.ContainerId, "container ID should match")
	require.Equal(t, "running", resp.Status, "status should be running")
	require.Nil(t, resp.ExitCode, "exit code should be nil for running container")
	require.NotNil(t, resp.ResourceUsage, "resource usage should be included")
	require.Equal(t, 50.0, resp.ResourceUsage.CpuPercent, "CPU percent should match")

	// Test 3: Query non-existent container (should fail)
	_, err = exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId: "non-existent",
	})
	require.Error(t, err, "query for non-existent container should fail")
	require.Equal(t, codes.NotFound, status.Code(err), "should return NotFound")
}

// TestSessionManagerIntegration_StreamingWithMultipleClients tests that
// multiple clients can connect via Session RPC and track the same execution.
func TestSessionManagerIntegration_StreamingWithMultipleClients(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	containerID := "test-container"
	execID := "test-exec"

	// Register execution
	err := exec.RegisterExecution(containerID, execID, false)
	require.NoError(t, err, "registration should succeed")

	// Verify multiple clients can be tracked
	instance, err := exec.registry.GetInstance(containerID, execID)
	require.NoError(t, err, "instance should exist")

	// Add multiple clients to the instance
	const numClients = 3
	for i := 0; i < numClients; i++ {
		clientID := fmt.Sprintf("client-%d", i)
		err := instance.AddClient(clientID, false, 0)
		require.NoError(t, err, "AddClient should not return error")
	}

	// Verify all clients are tracked
	require.Equal(t, numClients, instance.GetClientCount(), "should track all clients")

	// Write some output to verify writers work
	stdout := exec.GetStdoutWriter(containerID, execID)
	stderr := exec.GetStderrWriter(containerID, execID)

	testOutput := []byte("test stdout\n")
	testError := []byte("test stderr\n")

	_, err = stdout.Write(testOutput)
	require.NoError(t, err, "stdout write should succeed")

	_, err = stderr.Write(testError)
	require.NoError(t, err, "stderr write should succeed")

	// Verify data was written to channels (non-blocking check)
	select {
	case data := <-instance.stdoutChan:
		require.Equal(t, testOutput, data, "stdout data should match")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for stdout data")
	}

	select {
	case data := <-instance.stderrChan:
		require.Equal(t, testError, data, "stderr data should match")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for stderr data")
	}

	// Send exit code
	err = exec.SendExitCode(containerID, execID, 42)
	require.NoError(t, err, "send exit code should succeed")

	// Verify exit code was sent to channel
	select {
	case exitCode := <-instance.exitChan:
		require.Equal(t, int32(42), exitCode, "exit code should match")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for exit code")
	}

	// Verify instance is marked as exited
	require.Equal(t, ExecStatusExited, instance.GetStatus(), "instance should be exited")

	// Remove clients
	var finalCount int
	for i := 0; i < numClients; i++ {
		clientID := fmt.Sprintf("client-%d", i)
		finalCount = instance.RemoveClient(clientID)
	}

	// Verify all clients removed
	require.Equal(t, 0, finalCount, "all clients should be removed")

	// Cleanup
	err = exec.UnregisterExecution(containerID, execID)
	require.NoError(t, err, "cleanup should succeed")
}

// mockSessionStream implements Exec_SessionServer for testing
type mockSessionStream struct {
	grpc.ServerStream
	ctx      context.Context
	recvChan chan *SessionRequest
	sendChan chan *SessionResponse
	closed   bool
	mu       sync.Mutex
}

func (m *mockSessionStream) Context() context.Context {
	return m.ctx
}

func (m *mockSessionStream) Recv() (*SessionRequest, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, io.EOF
	}
	m.mu.Unlock()

	select {
	case req, ok := <-m.recvChan:
		if !ok {
			return nil, io.EOF
		}
		return req, nil
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}
}

func (m *mockSessionStream) Send(resp *SessionResponse) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("stream closed")
	}
	m.mu.Unlock()

	select {
	case m.sendChan <- resp:
		return nil
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

func (m *mockSessionStream) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.recvChan)
		close(m.sendChan)
	}
}

