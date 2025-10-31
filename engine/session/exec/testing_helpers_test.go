package exec

import (
	"context"
)

// mockStateRegistry is a mock implementation of ContainerStateRegistry for testing
type mockStateRegistry struct {
	states map[string]*ContainerState
}

func newMockStateRegistry() *mockStateRegistry {
	return &mockStateRegistry{
		states: make(map[string]*ContainerState),
	}
}

func (m *mockStateRegistry) GetState(containerID string) (*ContainerState, error) {
	state, ok := m.states[containerID]
	if !ok {
		return nil, ErrContainerNotFound
	}
	// Return a copy
	stateCopy := *state
	return &stateCopy, nil
}

func (m *mockStateRegistry) Subscribe(ctx context.Context, containerID string) (<-chan ContainerLifecycleEventData, func()) {
	// Not implemented for control tests
	ch := make(chan ContainerLifecycleEventData)
	close(ch)
	return ch, func() {}
}
