package exec

import (
	"context"
	"syscall"
	"testing"
	"time"

	runc "github.com/containerd/go-runc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// mockRuncClient is a mock implementation of runc.Runc for testing
type mockRuncClient struct {
	pauseCalled  bool
	resumeCalled bool
	killCalled   bool
	killSignal   int
	shouldFail   bool
}

func (m *mockRuncClient) Pause(ctx context.Context, id string) error {
	m.pauseCalled = true
	if m.shouldFail {
		return status.Errorf(codes.Internal, "mock pause error")
	}
	return nil
}

func (m *mockRuncClient) Resume(ctx context.Context, id string) error {
	m.resumeCalled = true
	if m.shouldFail {
		return status.Errorf(codes.Internal, "mock resume error")
	}
	return nil
}

func (m *mockRuncClient) Kill(ctx context.Context, id string, sig int, opts *runc.KillOpts) error {
	m.killCalled = true
	m.killSignal = sig
	if m.shouldFail {
		return status.Errorf(codes.Internal, "mock kill error")
	}
	return nil
}

// TestContainerPause tests the ContainerPause RPC method
func TestContainerPause(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		// Call ContainerPause
		resp, err := exec.ContainerPause(ctx, &ContainerControlRequest{
			ContainerId: "test-container",
		})

		// Verify response
		if err != nil {
			t.Fatalf("ContainerPause failed: %v", err)
		}
		if !resp.Success {
			t.Fatalf("Expected success=true, got success=false: %s", resp.Message)
		}
		if !mockRunc.pauseCalled {
			t.Fatal("Expected runc.Pause to be called")
		}
	})

	t.Run("missing_container_id", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		resp, err := exec.ContainerPause(ctx, &ContainerControlRequest{
			ContainerId: "",
		})

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resp.Success {
			t.Fatal("Expected success=false for missing container_id")
		}
	})

	t.Run("runc_client_unavailable", func(t *testing.T) {
		exec := NewExecAttachable(ctx)
		defer exec.Close()

		_, err := exec.ContainerPause(ctx, &ContainerControlRequest{
			ContainerId: "test-container",
		})

		if err == nil {
			t.Fatal("Expected error when runc client is unavailable")
		}
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("Expected Unavailable error, got: %v", err)
		}
	})

	t.Run("runc_pause_fails", func(t *testing.T) {
		mockRunc := &mockRuncClient{shouldFail: true}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		resp, err := exec.ContainerPause(ctx, &ContainerControlRequest{
			ContainerId: "test-container",
		})

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resp.Success {
			t.Fatal("Expected success=false when runc fails")
		}
		if !mockRunc.pauseCalled {
			t.Fatal("Expected runc.Pause to be called")
		}
	})
}

// TestContainerResume tests the ContainerResume RPC method
func TestContainerResume(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		// Call ContainerResume
		resp, err := exec.ContainerResume(ctx, &ContainerControlRequest{
			ContainerId: "test-container",
		})

		// Verify response
		if err != nil {
			t.Fatalf("ContainerResume failed: %v", err)
		}
		if !resp.Success {
			t.Fatalf("Expected success=true, got success=false: %s", resp.Message)
		}
		if !mockRunc.resumeCalled {
			t.Fatal("Expected runc.Resume to be called")
		}
	})

	t.Run("missing_container_id", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		resp, err := exec.ContainerResume(ctx, &ContainerControlRequest{
			ContainerId: "",
		})

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resp.Success {
			t.Fatal("Expected success=false for missing container_id")
		}
	})

	t.Run("runc_client_unavailable", func(t *testing.T) {
		exec := NewExecAttachable(ctx)
		defer exec.Close()

		_, err := exec.ContainerResume(ctx, &ContainerControlRequest{
			ContainerId: "test-container",
		})

		if err == nil {
			t.Fatal("Expected error when runc client is unavailable")
		}
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("Expected Unavailable error, got: %v", err)
		}
	})
}

// TestContainerSignal tests the ContainerSignal RPC method
func TestContainerSignal(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name           string
		signal         string
		expectedSignal syscall.Signal
	}{
		{"SIGTERM", "SIGTERM", syscall.SIGTERM},
		{"SIGKILL", "SIGKILL", syscall.SIGKILL},
		{"SIGINT", "SIGINT", syscall.SIGINT},
		{"SIGHUP", "SIGHUP", syscall.SIGHUP},
		{"SIGUSR1", "SIGUSR1", syscall.SIGUSR1},
		{"TERM_without_prefix", "TERM", syscall.SIGTERM},
		{"lowercase", "sigterm", syscall.SIGTERM},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRunc := &mockRuncClient{}
			exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
			defer exec.Close()

			resp, err := exec.ContainerSignal(ctx, &ContainerSignalRequest{
				ContainerId: "test-container",
				Signal:      tc.signal,
			})

			if err != nil {
				t.Fatalf("ContainerSignal failed: %v", err)
			}
			if !resp.Success {
				t.Fatalf("Expected success=true, got success=false: %s", resp.Message)
			}
			if !mockRunc.killCalled {
				t.Fatal("Expected runc.Kill to be called")
			}
			if mockRunc.killSignal != int(tc.expectedSignal) {
				t.Fatalf("Expected signal %d, got %d", tc.expectedSignal, mockRunc.killSignal)
			}
		})
	}

	t.Run("missing_container_id", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		resp, err := exec.ContainerSignal(ctx, &ContainerSignalRequest{
			ContainerId: "",
			Signal:      "SIGTERM",
		})

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resp.Success {
			t.Fatal("Expected success=false for missing container_id")
		}
	})

	t.Run("missing_signal", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		resp, err := exec.ContainerSignal(ctx, &ContainerSignalRequest{
			ContainerId: "test-container",
			Signal:      "",
		})

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resp.Success {
			t.Fatal("Expected success=false for missing signal")
		}
	})

	t.Run("invalid_signal", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		resp, err := exec.ContainerSignal(ctx, &ContainerSignalRequest{
			ContainerId: "test-container",
			Signal:      "SIGINVALID",
		})

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resp.Success {
			t.Fatal("Expected success=false for invalid signal")
		}
		if mockRunc.killCalled {
			t.Fatal("Expected runc.Kill not to be called for invalid signal")
		}
	})

	t.Run("runc_client_unavailable", func(t *testing.T) {
		exec := NewExecAttachable(ctx)
		defer exec.Close()

		_, err := exec.ContainerSignal(ctx, &ContainerSignalRequest{
			ContainerId: "test-container",
			Signal:      "SIGTERM",
		})

		if err == nil {
			t.Fatal("Expected error when runc client is unavailable")
		}
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("Expected Unavailable error, got: %v", err)
		}
	})
}

// TestParseSignal tests the parseSignal helper function
func TestParseSignal(t *testing.T) {
	testCases := []struct {
		input    string
		expected syscall.Signal
		wantErr  bool
	}{
		// With SIG prefix
		{"SIGTERM", syscall.SIGTERM, false},
		{"SIGKILL", syscall.SIGKILL, false},
		{"SIGINT", syscall.SIGINT, false},
		{"SIGHUP", syscall.SIGHUP, false},
		{"SIGQUIT", syscall.SIGQUIT, false},
		{"SIGUSR1", syscall.SIGUSR1, false},
		{"SIGUSR2", syscall.SIGUSR2, false},

		// Without SIG prefix
		{"TERM", syscall.SIGTERM, false},
		{"KILL", syscall.SIGKILL, false},
		{"INT", syscall.SIGINT, false},
		{"HUP", syscall.SIGHUP, false},

		// Case insensitive
		{"sigterm", syscall.SIGTERM, false},
		{"SigTerm", syscall.SIGTERM, false},
		{"term", syscall.SIGTERM, false},
		{"TERM", syscall.SIGTERM, false},

		// With whitespace
		{" SIGTERM ", syscall.SIGTERM, false},
		{" TERM ", syscall.SIGTERM, false},

		// Invalid signals
		{"SIGINVALID", 0, true},
		{"INVALID", 0, true},
		{"", 0, true},
		{"SIG", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result, err := parseSignal(tc.input)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Expected error for input %q, got nil", tc.input)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error for input %q: %v", tc.input, err)
			}

			if result != tc.expected {
				t.Fatalf("For input %q, expected %v, got %v", tc.input, tc.expected, result)
			}
		})
	}
}

// TestExecAttachableOptions tests the functional options pattern
func TestExecAttachableOptions(t *testing.T) {
	ctx := context.Background()

	t.Run("with_runc_client", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
		defer exec.Close()

		if exec.runcClient == nil {
			t.Fatal("Expected runcClient to be set")
		}
	})

	t.Run("with_state_registry", func(t *testing.T) {
		registry := newMockStateRegistry()
		exec := NewExecAttachableWithOptions(ctx, WithStateRegistry(registry))
		defer exec.Close()

		if exec.stateRegistry == nil {
			t.Fatal("Expected stateRegistry to be set")
		}
	})

	t.Run("with_both_options", func(t *testing.T) {
		mockRunc := &mockRuncClient{}
		registry := newMockStateRegistry()

		exec := NewExecAttachableWithOptions(
			ctx,
			WithRuncClient(mockRunc),
			WithStateRegistry(registry),
		)
		defer exec.Close()

		if exec.runcClient == nil {
			t.Fatal("Expected runcClient to be set")
		}
		if exec.stateRegistry == nil {
			t.Fatal("Expected stateRegistry to be set")
		}
	})

	t.Run("without_options", func(t *testing.T) {
		exec := NewExecAttachableWithOptions(ctx)
		defer exec.Close()

		if exec.runcClient != nil {
			t.Fatal("Expected runcClient to be nil")
		}
		if exec.stateRegistry != nil {
			t.Fatal("Expected stateRegistry to be nil")
		}
	})
}

// TestContainerControlConcurrency tests thread-safety of container control operations
func TestContainerControlConcurrency(t *testing.T) {
	ctx := context.Background()
	mockRunc := &mockRuncClient{}

	exec := NewExecAttachableWithOptions(ctx, WithRuncClient(mockRunc))
	defer exec.Close()

	// Run multiple operations concurrently
	const numOps = 10
	done := make(chan bool, numOps)

	for i := 0; i < numOps; i++ {
		go func() {
			// Pause
			_, err := exec.ContainerPause(ctx, &ContainerControlRequest{
				ContainerId: "test-container",
			})
			if err != nil && status.Code(err) != codes.Unavailable {
				t.Errorf("ContainerPause failed: %v", err)
			}

			// Small delay
			time.Sleep(1 * time.Millisecond)

			// Resume
			_, err = exec.ContainerResume(ctx, &ContainerControlRequest{
				ContainerId: "test-container",
			})
			if err != nil && status.Code(err) != codes.Unavailable {
				t.Errorf("ContainerResume failed: %v", err)
			}

			done <- true
		}()
	}

	// Wait for all operations to complete
	for i := 0; i < numOps; i++ {
		select {
		case <-done:
			// Operation completed
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent operations")
		}
	}
}
