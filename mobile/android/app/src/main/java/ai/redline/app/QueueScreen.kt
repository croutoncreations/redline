package ai.redline.app

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * The dispatch queue: what would run next, and why everything else will not.
 *
 * Mirrors the web dashboard's queue tab. The ordering is the scheduler's own,
 * preserved rather than re-sorted, so this shows the queue that will actually
 * run instead of a plausible-looking different one.
 */
@Composable
fun QueueScreen(
    state: QueueUiState,
    onSelectProvider: (String) -> Unit,
    onRefresh: () -> Unit,
    onRun: (QueueCandidate) -> Unit,
    onRetry: () -> Unit,
) {
    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize()) {
            QueueHeader(state, onSelectProvider, onRefresh)

            when {
                state.loading && state.queue == null -> CenteredWork()
                state.queue == null && state.failure != null -> QueueFailure(state.failure, onRetry)
                state.queue != null -> CandidateList(state.queue, onRun)
                else -> CenteredWork()
            }
        }
    }
}

@Composable
private fun QueueHeader(
    state: QueueUiState,
    onSelectProvider: (String) -> Unit,
    onRefresh: () -> Unit,
) {
    Column(
        Modifier
            .fillMaxWidth()
            .background(Panel)
            .padding(horizontal = 16.dp, vertical = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                "DISPATCH QUEUE",
                color = TextPrimary,
                fontWeight = FontWeight.Bold,
                fontSize = 16.sp,
                letterSpacing = 1.sp,
            )
            Spacer(Modifier.weight(1f))
            Text(
                if (state.refreshing) "Refreshing" else "Refresh",
                color = if (state.refreshing) TextMuted else Accent,
                fontSize = 13.sp,
                modifier = Modifier.clickable(enabled = !state.refreshing, onClick = onRefresh),
            )
        }

        // One provider's queue at a time, matching the web dashboard's picker.
        if (state.providers.size > 1) {
            Spacer(Modifier.height(10.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                state.providers.forEach { provider ->
                    val selected = provider == state.selectedProvider
                    Text(
                        provider,
                        color = if (selected) Background else TextPrimary,
                        fontSize = 12.sp,
                        fontWeight = if (selected) FontWeight.Bold else FontWeight.Normal,
                        modifier = Modifier
                            .clip(RoundedCornerShape(20.dp))
                            .background(if (selected) Accent else Line)
                            .clickable { onSelectProvider(provider) }
                            .padding(horizontal = 12.dp, vertical = 6.dp),
                    )
                }
            }
        }

        state.queue?.let { queue ->
            Spacer(Modifier.height(8.dp))
            Text(
                queueSubtitle(queue),
                color = if (queue.snapshotStale) Warn else TextMuted,
                fontSize = 12.sp,
            )
        }
    }
}

/**
 * Summarises the snapshot behind the queue.
 *
 * A stale snapshot is called out because the ordering is only as trustworthy as
 * the usage numbers it was computed from.
 */
private fun queueSubtitle(queue: QueueView): String {
    val parts = mutableListOf<String>()
    if (queue.snapshotLabel.isNotBlank()) {
        parts += "Snapshot ${queue.snapshotLabel}"
    }
    if (queue.snapshotStale) parts += "stale"
    if (!queue.dispatchAvailable && queue.providerReason.isNotBlank()) {
        parts += queue.providerReason
    }
    parts += "${queue.readyCount} ready"
    if (queue.blockedCount > 0) parts += "${queue.blockedCount} blocked"
    return parts.joinToString(" · ")
}

@Composable
private fun CandidateList(queue: QueueView, onRun: (QueueCandidate) -> Unit) {
    if (queue.candidates.isEmpty()) {
        Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            Text("No tasks are queued for this provider.", color = TextMuted, fontSize = 14.sp)
        }
        return
    }
    LazyColumn(
        contentPadding = PaddingValues(12.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(queue.candidates, key = { it.taskId }) { candidate ->
            CandidateRow(candidate, onRun)
        }
    }
}

@Composable
private fun CandidateRow(candidate: QueueCandidate, onRun: (QueueCandidate) -> Unit) {
    Card(
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.padding(14.dp)) {
            if (candidate.isNextUp) {
                Text(
                    "NEXT UP",
                    color = Good,
                    fontSize = 10.sp,
                    fontWeight = FontWeight.Bold,
                    letterSpacing = 1.sp,
                )
                Spacer(Modifier.height(6.dp))
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                // Priority drives the ordering, so it earns a place on the row.
                Text(
                    "P${candidate.priority}",
                    color = Accent,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Bold,
                    fontFamily = FontFamily.Monospace,
                )
                Spacer(Modifier.width(10.dp))
                Column(Modifier.weight(1f)) {
                    Text(
                        candidate.name.ifBlank { candidate.taskId },
                        color = TextPrimary,
                        fontSize = 14.sp,
                        fontWeight = FontWeight.Medium,
                    )
                    Spacer(Modifier.height(2.dp))
                    Text(
                        candidate.taskId,
                        color = TextMuted,
                        fontSize = 11.sp,
                        fontFamily = FontFamily.Monospace,
                    )
                }
                Spacer(Modifier.width(10.dp))
                Text(
                    "Run",
                    color = if (candidate.eligible) Accent else TextMuted,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier
                        .clip(RoundedCornerShape(8.dp))
                        .background(Line)
                        .clickable { onRun(candidate) }
                        .padding(horizontal = 14.dp, vertical = 8.dp),
                )
            }
            // Why a task will not run is the most useful thing on this screen:
            // without it the queue is a list of things mysteriously not
            // happening.
            if (candidate.reason.isNotBlank()) {
                Spacer(Modifier.height(8.dp))
                Text(
                    candidate.reason,
                    color = TextMuted,
                    fontSize = 12.sp,
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(6.dp))
                        .background(Background)
                        .padding(horizontal = 8.dp, vertical = 6.dp),
                )
            }
        }
    }
}

@Composable
private fun CenteredWork() {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        CircularProgressIndicator(color = Accent)
    }
}

@Composable
private fun QueueFailure(failure: UsageUiState.Failure, onRetry: () -> Unit) {
    Box(
        Modifier
            .fillMaxSize()
            .clickable(onClick = onRetry),
        contentAlignment = Alignment.Center,
    ) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Text(
                when (failure) {
                    UsageUiState.Failure.UNAUTHORIZED -> "Redline rejected this device"
                    UsageUiState.Failure.UNREACHABLE -> "Cannot reach Redline"
                },
                color = TextPrimary,
                fontSize = 16.sp,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                when (failure) {
                    UsageUiState.Failure.UNAUTHORIZED -> "Pair this device again."
                    UsageUiState.Failure.UNREACHABLE -> "Check the desktop is awake. Tap to retry."
                },
                color = TextMuted,
                fontSize = 13.sp,
            )
        }
    }
}

@Preview
@Composable
private fun QueuePreview() {
    QueueScreen(
        state = QueueUiState(
            providers = listOf("claude-main", "codex-main"),
            selectedProvider = "claude-main",
            queue = QueueView(
                snapshotLabel = "1m ago",
                dispatchAvailable = true,
                readyCount = 1,
                blockedCount = 1,
                nextUpTaskId = "docs-refresh",
                candidates = listOf(
                    QueueCandidate(
                        taskId = "docs-refresh", name = "Fix stale documentation",
                        priority = 55, eligible = true, isNextUp = true,
                    ),
                    QueueCandidate(
                        taskId = "bug-hunt", name = "Find one real bug",
                        priority = 80, eligible = false, reason = "cooldown for 4h 27m",
                    ),
                ),
            ),
        ),
        onSelectProvider = {}, onRefresh = {}, onRun = {}, onRetry = {},
    )
}
