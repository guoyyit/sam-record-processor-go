package samproc

// Test-only failure injection hooks. Zero disables the hook.
var (
	// injectSortFailChunks makes produceChunks fail after this many
	// chunks have been durably journaled.
	injectSortFailChunks int

	// injectMergeFailLines makes mergeInputs fail after this many
	// output lines have been written.
	injectMergeFailLines int

	// injectCheckpointBytes overrides the merge fsync/checkpoint
	// interval; zero keeps the 1 MiB default.
	injectCheckpointBytes int64
)

// errInjected is returned by failure-injection hooks.
type errInjected struct{ what string }

func (e *errInjected) Error() string { return "injected failure: " + e.what }
