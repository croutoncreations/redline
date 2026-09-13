package ai.redline.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewmodel.compose.viewModel

/** The screens reachable from the bottom bar. */
private enum class Tab(val label: String) {
    CAPACITY("Usage"),
    QUEUE("Queue"),
    RUNS("Runs"),
}

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val settings = RedlineSettings(this)
        val holder = CoreClientHolder(settings)

        setContent {
            val pairingModel: PairingViewModel = viewModel(
                factory = object : ViewModelProvider.Factory {
                    @Suppress("UNCHECKED_CAST")
                    override fun <T : ViewModel> create(modelClass: Class<T>): T =
                        PairingViewModel(CorePairingSource(), settings) as T
                },
            )
            val pairingState by pairingModel.state.collectAsState()

            // Bumped when the credential is cleared, which re-reads settings
            // and drops straight back to the pairing screen. Without it the
            // app would keep showing a dashboard it can no longer fetch, since
            // settings.isPaired is a plain read rather than observable state.
            var pairingGeneration by remember { mutableIntStateOf(0) }

            // Pairing state is read once at launch and then follows the view
            // model, so a successful pairing moves straight to the app without
            // needing a restart.
            val paired = remember(pairingGeneration, pairingState.isPaired) {
                settings.isPaired || pairingState.isPaired
            }
            if (!paired) {
                PairingScreen(state = pairingState, onScanned = pairingModel::pair)
                return@setContent
            }

            val usageModel: UsageViewModel = viewModel(
                factory = object : ViewModelProvider.Factory {
                    @Suppress("UNCHECKED_CAST")
                    override fun <T : ViewModel> create(modelClass: Class<T>): T =
                        UsageViewModel(CoreUsageSource(holder)) as T
                },
            )
            val runsModel: RunsViewModel = viewModel(
                factory = object : ViewModelProvider.Factory {
                    @Suppress("UNCHECKED_CAST")
                    override fun <T : ViewModel> create(modelClass: Class<T>): T =
                        RunsViewModel(CoreRunsSource(holder)) as T
                },
            )
            val queueModel: QueueViewModel = viewModel(
                factory = object : ViewModelProvider.Factory {
                    @Suppress("UNCHECKED_CAST")
                    override fun <T : ViewModel> create(modelClass: Class<T>): T =
                        QueueViewModel(CoreQueueSource(holder)) as T
                },
            )

            var tab by remember { mutableStateOf(Tab.CAPACITY) }
            val usageState by usageModel.state.collectAsState()
            val runsState by runsModel.state.collectAsState()
            val queueState by queueModel.state.collectAsState()

            // The queue picker needs the provider list, which the usage screen
            // already fetched. Passing it across avoids a second request for
            // something the app knows.
            val providerIds = usageState.view?.providers?.map { it.id }.orEmpty()
            LaunchedEffect(providerIds) {
                if (providerIds.isNotEmpty()) queueModel.setProviders(providerIds)
            }

            // Refreshing on resume is the guaranteed floor: whatever background
            // work did or did not happen, the numbers are correct whenever the
            // user is actually looking at them. Only the visible screen
            // refreshes, so the background one does not spend the desktop's
            // time on data nobody is reading.
            OnResume {
                when (tab) {
                    Tab.CAPACITY -> usageModel.refresh()
                    Tab.QUEUE -> queueModel.refresh()
                    Tab.RUNS -> runsModel.refresh()
                }
            }

            // Live updates run only while the capacity tab is actually on
            // screen. A stream held open behind another tab, or while the app
            // is backgrounded, would keep the radio awake for data nobody is
            // reading. Refresh-on-resume remains the floor underneath it.
            OnStartStop(
                active = tab == Tab.CAPACITY,
                onStart = usageModel::startLive,
                onStop = usageModel::stopLive,
            )

            Column(Modifier.fillMaxSize()) {
                Box(Modifier.weight(1f)) {
                    when (tab) {
                        Tab.CAPACITY -> UsageScreen(
                            state = usageState,
                            onRetry = usageModel::refresh,
                            onControlProvider = { id, control ->
                                usageModel.controlProvider(id, control)
                            },
                            onViewQueue = { id ->
                                queueModel.selectProvider(id)
                                tab = Tab.QUEUE
                            },
                            onUnpair = {
                                // Drop the relayed session too: it was
                                // authenticated with the credential being
                                // forgotten, so holding it open would leave a
                                // live tunnel to a desktop this phone is no
                                // longer paired with.
                                holder.dropRelay()
                                settings.clear()
                                pairingModel.forget()
                                pairingGeneration++
                            },
                        )

                        Tab.QUEUE -> QueueScreen(
                            state = queueState,
                            onSelectProvider = queueModel::selectProvider,
                            onRefresh = queueModel::refreshUsage,
                            onRun = { candidate -> runsModel.dispatch(candidate.taskId) },
                            onRetry = queueModel::refresh,
                        )

                        Tab.RUNS -> RunsScreen(
                            state = runsState,
                            onRetry = runsModel::refresh,
                            onSelectRun = { run -> runsModel.loadLogs(run.id) },
                            onDismissRun = runsModel::clearLogs,
                            onDispatch = runsModel::dispatch,
                            onDismissDispatch = runsModel::clearDispatchResult,
                            onSelectStream = { run, stream ->
                                runsModel.loadLogs(run.id, stream)
                            },
                            onControlTask = runsModel::controlTask,
                        )
                    }
                }
                TabBar(
                    selected = tab,
                    onSelect = { chosen ->
                        tab = chosen
                        // Switching tabs should show current data, not whatever
                        // was loaded the last time this tab was open.
                        when (chosen) {
                            Tab.CAPACITY -> usageModel.refresh()
                            Tab.QUEUE -> queueModel.refresh()
                            Tab.RUNS -> {
                                runsModel.refresh()
                                // Opening the runs tab is the moment the user
                                // has seen what happened, so the badge clears
                                // here rather than needing its own gesture.
                                runsModel.markAllRead()
                            }
                        }
                    },
                )
            }
        }
    }
}

@Composable
private fun TabBar(selected: Tab, onSelect: (Tab) -> Unit) {
    Row(
        Modifier
            .fillMaxWidth()
            .background(Panel)
            .padding(vertical = 10.dp),
    ) {
        Tab.entries.forEach { tab ->
            val active = tab == selected
            Box(
                Modifier
                    .weight(1f)
                    .clickable { onSelect(tab) },
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    tab.label,
                    color = if (active) TextPrimary else TextMuted,
                    fontSize = 13.sp,
                    fontWeight = if (active) FontWeight.Bold else FontWeight.Normal,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.height(20.dp),
                )
            }
        }
    }
}

/**
 * Keeps a subscription alive only while [active] and the screen is in the
 * foreground.
 *
 * Both conditions matter: a stream left running behind another tab or a
 * backgrounded app costs battery and data for something nobody is looking at.
 */
@Composable
private fun OnStartStop(active: Boolean, onStart: () -> Unit, onStop: () -> Unit) {
    val owner = LocalLifecycleOwner.current
    val start by rememberUpdatedState(onStart)
    val stop by rememberUpdatedState(onStop)

    // Keyed on the lifecycle owner and the active flag only. Keying on
    // anything that changes per frame would dispose and re-run this on every
    // recomposition, and the disposal calls stop() -- which is how a live
    // stream ends up reporting itself as offline while frames are arriving.
    DisposableEffect(owner, active) {
        if (!active) {
            stop()
            return@DisposableEffect onDispose { }
        }
        // addObserver replays the events already reached, so ON_START arrives
        // on registration when the screen is foreground. Starting again here
        // would tear down the subscription that had just been created.
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_START -> start()
                Lifecycle.Event.ON_STOP -> stop()
                else -> Unit
            }
        }
        owner.lifecycle.addObserver(observer)
        onDispose {
            owner.lifecycle.removeObserver(observer)
            stop()
        }
    }
}

/** Runs [action] every time the screen returns to the foreground. */
@Composable
private fun OnResume(action: () -> Unit) {
    val owner = LocalLifecycleOwner.current
    val current by rememberUpdatedState(action)
    DisposableEffect(owner) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) current()
        }
        owner.lifecycle.addObserver(observer)
        onDispose { owner.lifecycle.removeObserver(observer) }
    }
}
