package exec

import (
	"context"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mock stream for testing
type mockExecSessionServer struct {
	Exec_SessionServer
	ctx      context.Context
	recvChan chan *SessionRequest
	sendChan chan *SessionResponse
}

func (m *mockExecSessionServer) Context() context.Context {
	return m.ctx
}

func (m *mockExecSessionServer) Recv() (*SessionRequest, error) {
	select {
	case req := <-m.recvChan:
		return req, nil
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}
}

func (m *mockExecSessionServer) Send(resp *SessionResponse) error {
	select {
	case m.sendChan <- resp:
		return nil
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

func TestSessionNewFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Create mock stream with cancellable context
	streamCtx, streamCancel := context.WithCancel(ctx)
	stream := &mockExecSessionServer{
		ctx:      streamCtx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 10),
	}

	// Send Start message (new session)
	stream.recvChan <- &SessionRequest{
		Msg: &SessionRequest_Start{
			Start: &Start{
				ContainerId: "container1",
				ExecId:      "exec1",
				Tty:         true,
			},
		},
	}

	// Start session in goroutine
	sessionDone := make(chan error, 1)
	go func() {
		sessionDone <- attachable.Session(stream)
	}()

	// Should receive Ready message
	select {
	case resp := <-stream.sendChan:
		ready := resp.GetReady()
		if ready == nil {
			t.Fatal("Expected Ready message")
		}
		if ready.SessionId == "" {
			t.Error("Session ID should not be empty")
		}
		t.Logf("Session ID: %s", ready.SessionId)
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for Ready message")
	}

	// Cancel the stream context to end the session
	streamCancel()
	close(stream.recvChan)

	// Wait for session to end
	select {
	case err := <-sessionDone:
		if err != nil && err != io.EOF && err != context.Canceled {
			t.Fatalf("Session failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for session to end")
	}
}

func TestSessionReconnectFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Pre-register an instance
	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	sessionID := instance.GetSessionID()

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Create mock stream for reconnection
	streamCtx, streamCancel := context.WithCancel(ctx)
	stream := &mockExecSessionServer{
		ctx:      streamCtx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 10),
	}

	// Send Start message with session ID (reconnect)
	stream.recvChan <- &SessionRequest{
		Msg: &SessionRequest_Start{
			Start: &Start{
				ContainerId:        "container1",
				ExecId:             "exec1",
				SessionId:          sessionID,
				ReplayFromSequence: 0,
			},
		},
	}

	// Start session in goroutine
	sessionDone := make(chan error, 1)
	go func() {
		sessionDone <- attachable.Session(stream)
	}()

	// Should receive Ready with same session ID
	select {
	case resp := <-stream.sendChan:
		ready := resp.GetReady()
		if ready == nil {
			t.Fatal("Expected first Ready message")
		}
		if ready.SessionId != sessionID {
			t.Errorf("Expected session ID %s, got %s", sessionID, ready.SessionId)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for first Ready message")
	}

	// Should also receive replay complete message
	select {
	case resp := <-stream.sendChan:
		ready := resp.GetReady()
		if ready == nil {
			t.Fatal("Expected replay complete Ready message")
		}
		if !ready.ReplayComplete {
			t.Error("Expected replay_complete to be true")
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for replay complete message")
	}

	// Cancel the stream context to end the session
	streamCancel()
	close(stream.recvChan)

	// Wait for session to end
	select {
	case err := <-sessionDone:
		if err != nil && err != io.EOF && err != context.Canceled {
			t.Fatalf("Session failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for session to end")
	}
}

func TestSessionInvalidSessionID(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	stream := &mockExecSessionServer{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 10),
	}

	// Send Start with invalid session ID
	stream.recvChan <- &SessionRequest{
		Msg: &SessionRequest_Start{
			Start: &Start{
				ContainerId: "container1",
				ExecId:      "exec1",
				SessionId:   "invalid-session-id",
			},
		},
	}

	// Run session
	err := attachable.Session(stream)

	// Should fail with NotFound
	if err == nil {
		t.Fatal("Expected error for invalid session ID")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}

	if st.Code() != codes.NotFound {
		t.Errorf("Expected NotFound code, got: %v", st.Code())
	}
}

func TestSessionClientTracking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	sessionID := instance.GetSessionID()

	// Initial client count
	if instance.GetClientCount() != 0 {
		t.Errorf("Expected 0 clients, got %d", instance.GetClientCount())
	}

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Connect first client
	streamCtx, streamCancel := context.WithCancel(ctx)
	stream1 := &mockExecSessionServer{
		ctx:      streamCtx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 10),
	}

	stream1.recvChan <- &SessionRequest{
		Msg: &SessionRequest_Start{
			Start: &Start{
				ContainerId: "container1",
				ExecId:      "exec1",
				SessionId:   sessionID,
			},
		},
	}

	go attachable.Session(stream1)

	// Wait for Ready message (confirms client connected)
	select {
	case <-stream1.sendChan:
		// Got Ready message
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for Ready message")
	}

	// Wait for replay complete message
	select {
	case <-stream1.sendChan:
		// Got replay complete
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for replay complete message")
	}

	// Should have 1 client
	if instance.GetClientCount() != 1 {
		t.Errorf("Expected 1 client, got %d", instance.GetClientCount())
	}

	// Disconnect client
	streamCancel()
	close(stream1.recvChan)

	// Wait for disconnect
	time.Sleep(100 * time.Millisecond)

	// Client should be removed
	if instance.GetClientCount() != 0 {
		t.Errorf("Expected 0 clients after disconnect, got %d", instance.GetClientCount())
	}
}
