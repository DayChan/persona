package persona.core.hub.tunnel

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.asExecutor
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Runs blocking socket work on the IO pool. Cancelling does not wait for it: [onCancel] closes what it blocks on (a
 * socket read does not see a thread interrupt), on a thread of its own so a canceller holding a lock is not held up
 * by a slow close. What the work made after all goes to [onLate], since nobody takes it any more.
 */
internal suspend fun <T> blocking(
    onCancel: () -> Unit,
    onLate: (T) -> Unit = { (it as? AutoCloseable)?.close() },
    block: () -> T,
): T = suspendCancellableCoroutine { continuation ->
    continuation.invokeOnCancellation { Dispatchers.IO.asExecutor().execute(onCancel) }
    Dispatchers.IO.asExecutor().execute {
        val result = runCatching(block)
        result.fold(
            { value -> continuation.resume(value) { _, late, _ -> onLate(late) } },
            { error -> if (continuation.isActive) continuation.resumeWith(Result.failure(error)) },
        )
    }
}
