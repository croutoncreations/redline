package ai.redline.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
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
    CAPACITY("Capacity"),
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

            // Pairing state is read once at launch and then follows the view
            // model, so a successful pairing moves straight to the app without
            // needing a restart.
            val paired = settings.isPaired || pairingState.isPaired
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

            var tab by remember { mutableStateOf(Tab.CAPACITY) }
            val usageState by usageModel.state.collectAsState()
            val runsState by runsModel.state.collectAsState()

            // Refreshing on resume is the guaranteed floor: whatever background
            // work did or did not happen, the numbers are correct whenever the
            // user is actually looking at them. Only the visible screen
            // refreshes, so the background one does not spend the desktop's
            // time on data nobody is reading.
            OnResume {
                when (tab) {
                    Tab.CAPACITY -> usageModel.refresh()
                    Tab.RUNS -> runsModel.refresh()
                }
            }

            Column(Modifier.fillMaxSize()) {
                Box(Modifier.weight(1f)) {
                    when (tab) {
                        Tab.CAPACITY -> UsageScreen(
                            state = usageState,
                            onRetry = usageModel::refresh,
                        )

                        Tab.RUNS -> RunsScreen(
                            state = runsState,
                            onRetry = runsModel::refresh,
                            onSelectRun = { run -> runsModel.loadLogs(run.id) },
                            onDismissRun = runsModel::clearLogs,
                            onDispatch = runsModel::dispatch,
                            onDismissDispatch = runsModel::clearDispatchResult,
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
                            Tab.RUNS -> runsModel.refresh()
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
