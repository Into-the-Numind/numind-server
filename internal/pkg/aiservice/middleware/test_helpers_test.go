package middleware

// test_helpers_test.go contains shared test utilities used across
// all middleware *_test.go files.

// mockLogger captures Warn/Error calls for assertions in tests.
type mockLogger struct {
	warns  []string
	errors []string
}

func (m *mockLogger) Warnw(msg string, _ ...interface{})  { m.warns = append(m.warns, msg) }
func (m *mockLogger) Errorw(msg string, _ ...interface{}) { m.errors = append(m.errors, msg) }
