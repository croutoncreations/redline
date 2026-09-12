package ai.redline.app

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * The runs screen: what the desktop has been doing.
 *
 * Failures are what people open this for, so the summary leads with the failed
 * count and failed rows are marked in the danger tone rather than being left to
 * blend in with successes.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RunsScreen(
    state: RunsUiState,
    onRetry: () -> Unit,
    onSelectRun: (RunSummary) -> Unit,
    onDismissRun: () -> Unit,
    onDispatch: (String) -> Unit,
    onDismissDispatch: () -> Unit,
    onSelectStream: (RunSummary, String) -> Unit = { _, _ -> },
    onControlTask: (String, String) -> Unit = { _, _ -> },
) {
    var selected by remember { mutableStateOf<RunSummary?>(null) }
    var showTasks by remember { mutableStateOf(false) }
    // Dispatch spends real capacity and can open a pull request, so it is
    // confirmed rather than fired on a single tap, matching the web version.
    var confirming by remember { mutableStateOf<TaskSummary?>(null) }

    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(modifier = Modifier.fillMaxSize()) {
            RunsHeader(state, onRunNow = { showTasks = true })

            state.lastDispatch?.let { DispatchBanner(it, onDismissDispatch) }

            when {
                state.loading && !state.hasData -> CenteredSpinner()
                !state.hasData && state.failure != null -> RunsFailure(state.failure, onRetry)
                state.runs != null -> RunList(
                    runs = state.runs.runs,
                    onSelect = { run ->
                        selected = run
                        onSelectRun(run)
                    },
                )

                else -> CenteredSpinner()
            }
        }
    }

    selected?.let { run ->
        ModalBottomSheet(
            onDismissRequest = {
                selected = null
                onDismissRun()
            },
            sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
            containerColor = Panel,
        ) {
            RunDetail(run, state) { stream -> onSelectStream(run, stream) }
        }
    }

    // One sheet, two states. Dismissing one ModalBottomSheet and opening
    // another in the same frame does not work: the outgoing sheet's animation
    // swallows the incoming one, and neither appears.
    if (showTasks) {
        ModalBottomSheet(
            onDismissRequest = {
                showTasks = false
                confirming = null
            },
            sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
            containerColor = Panel,
        ) {
            val pending = confirming
            if (pending == null) {
                TaskPicker(
                    tasks = state.tasks?.tasks.orEmpty(),
                    dispatching = state.dispatching,
                    onDispatch = { task -> confirming = task },
                    onControlTask = onControlTask,
                )
            } else {
                DispatchConfirm(
                    task = pending,
                    onCancel = { confirming = null },
                    onConfirm = {
                        confirming = null
                        showTasks = false
                        onDispatch(pending.id)
                    },
                )
            }
        }
    }
}

/**
 * Confirms a dispatch before spending capacity.
 *
 * A run consumes a subscription window and often opens a pull request, so a
 * mis-tap has a cost that is not trivially undone.
 */
@Composable
private fun DispatchConfirm(task: TaskSummary, onCancel: () -> Unit, onConfirm: () -> Unit) {
    Column(
        Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp)
            .padding(bottom = 28.dp),
    ) {
        Text("Run this task now?", color = TextPrimary, fontSize = 18.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(6.dp))
        Text(task.name.ifBlank { task.id }, color = TextPrimary, fontSize = 14.sp)
        Spacer(Modifier.height(2.dp))
        Text(
            "This spends provider capacity. The scheduler can still hold it back.",
            color = TextMuted,
            fontSize = 12.sp,
        )
        Spacer(Modifier.height(18.dp))
        Row {
            Text(
                "Cancel",
                color = TextPrimary,
                fontSize = 14.sp,
                modifier = Modifier
                    .clip(RoundedCornerShape(8.dp))
                    .background(Line)
                    .clickable(onClick = onCancel)
                    .padding(horizontal = 18.dp, vertical = 10.dp),
            )
            Spacer(Modifier.width(10.dp))
            Text(
                "Run now",
                color = Accent,
                fontSize = 14.sp,
                fontWeight = FontWeight.Medium,
                modifier = Modifier
                    .clip(RoundedCornerShape(8.dp))
                    .background(AccentSoft)
                    .clickable(onClick = onConfirm)
                    .padding(horizontal = 18.dp, vertical = 10.dp),
            )
        }
    }
}

@Composable
private fun RunsHeader(state: RunsUiState, onRunNow: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(Panel)
            .padding(horizontal = 16.dp, vertical = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                "RUNS",
                color = TextPrimary,
                fontWeight = FontWeight.Bold,
                fontSize = 16.sp,
                letterSpacing = 1.sp,
            )
            Spacer(Modifier.weight(1f))
            if (state.failure != null && state.hasData) {
                Text("Offline", color = Warn, fontSize = 12.sp)
                Spacer(Modifier.width(12.dp))
            }
            Text(
                "Run now",
                color = Accent,
                fontSize = 13.sp,
                fontWeight = FontWeight.Medium,
                modifier = Modifier.clickable(onClick = onRunNow),
            )
        }
        Spacer(Modifier.height(2.dp))
        Text(runsSubtitle(state), color = TextMuted, fontSize = 12.sp)
    }
}

/** Leads with failures, because that is what the screen is for. */
private fun runsSubtitle(state: RunsUiState): String {
    val runs = state.runs ?: return "Recent activity"
    val parts = mutableListOf("${runs.totalCount} recent")
    if (runs.runningCount > 0) parts += "${runs.runningCount} running"
    if (runs.failedCount > 0) parts += "${runs.failedCount} failed"
    return parts.joinToString(" · ")
}

@Composable
private fun RunList(runs: List<RunSummary>, onSelect: (RunSummary) -> Unit) {
    if (runs.isEmpty()) {
        Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            Text("No runs yet", color = TextMuted, fontSize = 14.sp)
        }
        return
    }
    LazyColumn(
        contentPadding = PaddingValues(12.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(runs, key = { it.id }) { run -> RunRow(run, onSelect) }
    }
}

@Composable
private fun RunRow(run: RunSummary, onSelect: (RunSummary) -> Unit) {
    Card(
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onSelect(run) },
    ) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(run)
                Spacer(Modifier.width(10.dp))
                // The task's name, not its id: it is what someone recognises
                // without translating a slug in their head.
                Text(
                    run.name.ifBlank { run.taskId },
                    color = TextPrimary,
                    fontSize = 15.sp,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier.weight(1f),
                )
                Spacer(Modifier.width(8.dp))
                Text(run.relativeLabel, color = TextMuted, fontSize = 12.sp)
            }

            // A one-line preview of what the run concluded, which is often
            // enough to avoid opening it at all.
            if (run.summary.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(run.summary, color = TextMuted, fontSize = 12.sp, maxLines = 2)
            }

            Spacer(Modifier.height(6.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    run.shortId,
                    color = TextMuted,
                    fontSize = 12.sp,
                    fontFamily = FontFamily.Monospace,
                )
                Spacer(Modifier.width(10.dp))
                Text(statusLabel(run), color = statusTone(run), fontSize = 12.sp)
                Spacer(Modifier.weight(1f))
                Text(run.durationLabel, color = TextMuted, fontSize = 12.sp)
            }

            if (run.metaLabel.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(
                    run.metaLabel,
                    color = TextMuted,
                    fontSize = 11.sp,
                    fontFamily = FontFamily.Monospace,
                )
            }
            // The failure reason is the whole point of the row, so it is shown
            // inline rather than hidden behind a tap.
            if (run.error.isNotBlank()) {
                Spacer(Modifier.height(6.dp))
                Text(run.error, color = Danger, fontSize = 12.sp, maxLines = 2)
            }
            if (run.pullRequestUrl.isNotBlank()) {
                Spacer(Modifier.height(6.dp))
                Text("Pull request opened", color = Good, fontSize = 12.sp)
            }
        }
    }
}

@Composable
private fun StatusDot(run: RunSummary) {
    Box(
        Modifier
            .size(8.dp)
            .clip(CircleShape)
            .background(statusTone(run))
            // Colour alone would not be readable to a screen reader, or to
            // someone who cannot distinguish these two hues.
            .semantics { contentDescription = statusLabel(run) },
    )
}

private fun statusLabel(run: RunSummary): String = when {
    run.running -> "Running"
    run.succeeded -> "Succeeded"
    else -> "Failed"
}

private fun statusTone(run: RunSummary): Color = when {
    run.running -> Warn
    run.succeeded -> Good
    else -> Danger
}

@Composable
private fun RunDetail(
    run: RunSummary,
    state: RunsUiState,
    onSelectStream: (String) -> Unit,
) {
    Column(
        Modifier
            .fillMaxWidth()
            // A run with a long summary and a full timeline is taller than the
            // sheet, and without this the log controls below are unreachable.
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 16.dp)
            .padding(bottom = 24.dp),
    ) {
        Text(
            run.name.ifBlank { run.taskId },
            color = TextPrimary,
            fontSize = 18.sp,
            fontWeight = FontWeight.Bold,
        )
        Spacer(Modifier.height(4.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(statusLabel(run), color = statusTone(run), fontSize = 13.sp)
            Spacer(Modifier.width(10.dp))
            Text(
                "${run.shortId} · ${run.durationLabel} · ${run.relativeLabel}",
                color = TextMuted,
                fontSize = 12.sp,
                fontFamily = FontFamily.Monospace,
            )
        }

        if (run.summary.isNotBlank()) {
            Spacer(Modifier.height(14.dp))
            Text("Summary", color = TextMuted, fontSize = 12.sp)
            Spacer(Modifier.height(4.dp))
            Text(run.summary, color = TextPrimary, fontSize = 14.sp)
        }

        if (run.error.isNotBlank()) {
            Spacer(Modifier.height(14.dp))
            Text("Error", color = TextMuted, fontSize = 12.sp)
            Spacer(Modifier.height(4.dp))
            Text(run.error, color = Danger, fontSize = 14.sp)
        }

        // The timeline shows where a run spent its time, and where it stopped
        // when it failed.
        state.events?.events?.takeIf { it.isNotEmpty() }?.let { events ->
            Spacer(Modifier.height(14.dp))
            Text("Timeline", color = TextMuted, fontSize = 12.sp)
            Spacer(Modifier.height(6.dp))
            events.forEach { event ->
                Row(Modifier.padding(vertical = 3.dp)) {
                    Text(event.label, color = TextPrimary, fontSize = 12.sp)
                    Spacer(Modifier.weight(1f))
                    Text(
                        event.sinceStartLabel,
                        color = TextMuted,
                        fontSize = 11.sp,
                        fontFamily = FontFamily.Monospace,
                    )
                }
            }
        }

        Spacer(Modifier.height(14.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Output", color = TextMuted, fontSize = 12.sp)
            Spacer(Modifier.weight(1f))
            // A failing run usually explains itself on stderr, so both streams
            // need to be reachable rather than just the one.
            listOf("stdout", "stderr").forEach { stream ->
                val active = state.logStream == stream
                Text(
                    stream,
                    color = if (active) Accent else TextMuted,
                    fontSize = 11.sp,
                    fontFamily = FontFamily.Monospace,
                    modifier = Modifier
                        .padding(start = 8.dp)
                        .clip(RoundedCornerShape(6.dp))
                        .background(if (active) AccentSoft else Line)
                        .clickable { onSelectStream(stream) }
                        .padding(horizontal = 10.dp, vertical = 5.dp),
                )
            }
        }
        Spacer(Modifier.height(6.dp))
        when {
            state.logsLoading -> Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(color = Accent, modifier = Modifier.size(16.dp))
                Spacer(Modifier.width(8.dp))
                Text("Loading logs", color = TextMuted, fontSize = 12.sp)
            }

            state.logs.isNullOrBlank() -> Text("No output", color = TextMuted, fontSize = 12.sp)

            else -> Box(
                Modifier
                    .fillMaxWidth()
                    .heightIn(max = 280.dp)
                    .clip(RoundedCornerShape(8.dp))
                    .background(Background)
                    .padding(10.dp),
            ) {
                // Logs are pre-formatted and often wide, so they scroll
                // horizontally rather than being wrapped into unreadable soup.
                // Vertical scrolling belongs to the sheet: nesting a second
                // vertical scroller inside it would trap the gesture here.
                Text(
                    state.logs,
                    color = TextPrimary,
                    fontSize = 11.sp,
                    fontFamily = FontFamily.Monospace,
                    modifier = Modifier.horizontalScroll(rememberScrollState()),
                )
            }
        }
    }
}

@Composable
private fun TaskPicker(
    tasks: List<TaskSummary>,
    dispatching: Boolean,
    onDispatch: (TaskSummary) -> Unit,
    onControlTask: (String, String) -> Unit,
) {
    Column(
        Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp)
            .padding(bottom = 24.dp),
    ) {
        Text("Run a task now", color = TextPrimary, fontSize = 18.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(2.dp))
        Text(
            "The scheduler still decides whether there is capacity.",
            color = TextMuted,
            fontSize = 12.sp,
        )
        Spacer(Modifier.height(12.dp))

        if (dispatching) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(color = Accent, modifier = Modifier.size(16.dp))
                Spacer(Modifier.width(8.dp))
                Text("Dispatching", color = TextMuted, fontSize = 13.sp)
            }
            return@Column
        }

        if (tasks.isEmpty()) {
            Text("No tasks are configured.", color = TextMuted, fontSize = 13.sp)
            return@Column
        }

        // Dispatchable tasks come first: they are what this sheet is for. The
        // rest stay visible so they can be enabled without leaving the phone,
        // which is the difference between viewing and operating.
        val ordered = tasks.sortedByDescending { it.dispatchable }

        LazyColumn(
            verticalArrangement = Arrangement.spacedBy(8.dp),
            modifier = Modifier.heightIn(max = 460.dp),
        ) {
            items(ordered, key = { it.id }) { task ->
                Card(
                    colors = CardDefaults.cardColors(containerColor = Background),
                    shape = RoundedCornerShape(10.dp),
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Row(
                        Modifier.padding(12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(
                                task.name.ifBlank { task.id },
                                color = if (task.enabled) TextPrimary else TextMuted,
                                fontSize = 14.sp,
                                fontWeight = FontWeight.Medium,
                            )
                            Spacer(Modifier.height(2.dp))
                            Text(
                                if (task.enabled) task.type else "${task.type} · disabled",
                                color = TextMuted,
                                fontSize = 11.sp,
                            )
                        }
                        Text(
                            if (task.enabled) "Disable" else "Enable",
                            color = TextMuted,
                            fontSize = 12.sp,
                            modifier = Modifier
                                .clip(RoundedCornerShape(8.dp))
                                .background(Line)
                                .clickable {
                                    onControlTask(
                                        task.id,
                                        if (task.enabled) "disable" else "enable",
                                    )
                                }
                                .padding(horizontal = 12.dp, vertical = 8.dp),
                        )
                        if (task.dispatchable) {
                            Spacer(Modifier.width(8.dp))
                            Text(
                                "Run",
                                color = Accent,
                                fontSize = 12.sp,
                                fontWeight = FontWeight.Medium,
                                modifier = Modifier
                                    .clip(RoundedCornerShape(8.dp))
                                    .background(AccentSoft)
                                    .clickable { onDispatch(task) }
                                    .padding(horizontal = 14.dp, vertical = 8.dp),
                            )
                        }
                    }
                }
            }
        }
    }
}

/**
 * Reports what dispatch actually did.
 *
 * "Started" and "held back" are opposite outcomes that look similar, so they
 * get different colours and the scheduler's own reason is shown verbatim.
 */
@Composable
private fun DispatchBanner(result: DispatchView, onDismiss: () -> Unit) {
    val tone = if (result.started) Good else Warn
    Row(
        Modifier
            .fillMaxWidth()
            .background(Panel)
            .clickable(onClick = onDismiss)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                when {
                    result.started -> "Run started"
                    result.refused -> "Not run"
                    else -> "Held back"
                },
                color = tone,
                fontSize = 13.sp,
                fontWeight = FontWeight.Medium,
            )
            if (result.reason.isNotBlank()) {
                Spacer(Modifier.height(2.dp))
                Text(result.reason, color = TextMuted, fontSize = 12.sp)
            }
        }
        Text("Dismiss", color = TextMuted, fontSize = 12.sp)
    }
}

@Composable
private fun CenteredSpinner() {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        CircularProgressIndicator(color = Accent)
    }
}

@Composable
private fun RunsFailure(failure: UsageUiState.Failure, onRetry: () -> Unit) {
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
                    UsageUiState.Failure.ENTITLEMENT_REFUSED ->
                        "Remote access needs a current subscription"
                },
                color = TextPrimary,
                fontSize = 16.sp,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                when (failure) {
                    UsageUiState.Failure.UNAUTHORIZED -> "Pair this device again."
                    UsageUiState.Failure.UNREACHABLE -> "Check the desktop is awake. Tap to retry."
                    UsageUiState.Failure.ENTITLEMENT_REFUSED ->
                        "Renew to use the relay. Your own network still works."
                },
                color = TextMuted,
                fontSize = 13.sp,
            )
        }
    }
}

@Preview
@Composable
private fun RunsPreview() {
    RunsScreen(
        state = RunsUiState(
            runs = RunListView(
                runs = listOf(
                    RunSummary(
                        id = "a", shortId = "7d501fd5", taskId = "release-notes",
                        succeeded = true, durationLabel = "1m 41s", relativeLabel = "15h ago",
                        summary = "Updated the changelog.",
                        pullRequestUrl = "https://example.com/pr/61",
                    ),
                    RunSummary(
                        id = "b", shortId = "b66a2bb0", taskId = "nightly-tests",
                        succeeded = false, durationLabel = "30m", relativeLabel = "2h ago",
                        error = "tests failed",
                    ),
                ),
                totalCount = 2, failedCount = 1,
            ),
        ),
        onRetry = {}, onSelectRun = {}, onDismissRun = {},
        onDispatch = {}, onDismissDispatch = {},
    )
}
