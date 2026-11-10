package node

// addHookWaiter tracks one blocking hook under its call key, initializing the set on first use.
func addHookWaiter[T comparable](waiting map[string][]T, key string, waiter T) map[string][]T {
	if waiting == nil {
		waiting = map[string][]T{}
	}
	waiting[key] = append(waiting[key], waiter)
	return waiting
}

// removeHookWaiter stops tracking waiter and drops the key once its last waiter is gone.
func removeHookWaiter[T comparable](waiting map[string][]T, key string, waiter T) map[string][]T {
	waiters := waiting[key]
	for i, other := range waiters {
		if other == waiter {
			waiters = append(waiters[:i:i], waiters[i+1:]...)
			break
		}
	}
	if len(waiters) == 0 {
		delete(waiting, key)
	} else {
		waiting[key] = waiters
	}
	return waiting
}
