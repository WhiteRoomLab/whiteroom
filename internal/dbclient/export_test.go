package dbclient

// SetPrefetchBudget lowers what a transaction keeps of prefetched objects and pages to n bytes, and returns the function
// that puts it back. A test that calls it does not run at the same time as another that reads.
func SetPrefetchBudget(n int64) (restore func()) {
	old := prefetchBudget
	prefetchBudget = n
	return func() { prefetchBudget = old }
}
