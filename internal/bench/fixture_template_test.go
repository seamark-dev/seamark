package bench

import "testing"

// templateGenerator materializes a synthetic fixture once, during the owning
// test's setup, then hands every caller an independent writable copy. The
// generation runs eagerly so a failure names the test, and the returned
// closure copies the immutable template instead of generating it again: it
// never touches *testing.T, mutates the template, or caches by instance ID.
//
// Use this only for inspected, regular-file synthetic fixtures. Rewriting
// closures that mutate generated state, or generation that is itself under
// test, must keep calling the real generator.
func templateGenerator(t *testing.T, generate func(string) error) func(string) error {
	t.Helper()

	template := t.TempDir()
	if err := generate(template); err != nil {
		t.Fatalf("generate fixture template: %v", err)
	}

	return func(dst string) error {
		return copyTree(template, dst)
	}
}
