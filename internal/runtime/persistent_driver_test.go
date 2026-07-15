package runtime

import "testing"

func TestPersistentDriverInterface(t *testing.T) {
	// Compile-time check: a mock implements PersistentDriver.
	var _ PersistentDriver = (*mockPersistentDriver)(nil)
}

type mockPersistentDriver struct{}

func (m *mockPersistentDriver) Name() string  { return "mock" }
func (m *mockPersistentDriver) Start() error  { return nil }
func (m *mockPersistentDriver) Stop() error   { return nil }
func (m *mockPersistentDriver) Health() error { return nil }
