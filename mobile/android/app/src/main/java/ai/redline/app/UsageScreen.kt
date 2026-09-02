package ai.redline.app

import androidx.compose.foundation.background
import androidx.compose.foundation.Image
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
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

private val Background = Color(0xFF0D0F12)
private val Panel = Color(0xFF15181D)
private val Line = Color(0xFF272C34)
private val TextPrimary = Color(0xFFE6E9EE)
private val TextMuted = Color(0xFF91989E)
private val Accent = Color(0xFFFF5A52)
private val Good = Color(0xFF72D9A4)
private val Warn = Color(0xFFEFBD62)
private val Danger = Color(0xFFFF7B72)

/** Matches the web dashboard's thresholds so both surfaces agree. */
private fun toneFor(percent: Int): Color = when {
    percent < 15 -> Danger
    percent < 35 -> Warn
    else -> Good
}

@Composable
fun UsageScreen(state: UsageUiState, onRetry: () -> Unit) {
    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(modifier = Modifier.fillMaxSize()) {
            Header(state)
            when {
                // Only show a spinner with nothing behind it on a cold start;
                // a refresh over existing data should not blank the screen.
                state.loading && !state.hasData -> CenteredProgress()
                !state.hasData && state.failure != null -> FailureMessage(state.failure, onRetry)
                state.view != null -> ProviderList(state)
                else -> CenteredProgress()
            }
        }
    }
}

@Composable
private fun Header(state: UsageUiState) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(Panel)
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Image(
                painter = painterResource(R.drawable.redline_mark),
                // The wordmark beside it already names the app, so announcing
                // this too would just make a screen reader say it twice.
                contentDescription = null,
                modifier = Modifier.height(17.dp),
            )
            Spacer(Modifier.width(8.dp))
            Text(
                "REDLINE",
                color = TextPrimary,
                fontWeight = FontWeight.Bold,
                fontSize = 16.sp,
                letterSpacing = 1.sp,
            )
            Spacer(Modifier.weight(1f))
            if (state.failure != null && state.hasData) {
                // Showing cached numbers without saying so would be misleading.
                Text("Offline", color = Warn, fontSize = 12.sp)
            }
        }
        Spacer(Modifier.height(2.dp))
        Text("Capacity", color = TextMuted, fontSize = 12.sp)
    }
}

@Composable
private fun CenteredProgress() {
    Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        CircularProgressIndicator(color = Accent)
    }
}

@Composable
private fun FailureMessage(failure: UsageUiState.Failure, onRetry: () -> Unit) {
    val message = when (failure) {
        UsageUiState.Failure.UNAUTHORIZED ->
            "Redline rejected this device's credentials. Pair it again from the desktop app."
        UsageUiState.Failure.UNREACHABLE ->
            "Cannot reach Redline. Check that the desktop app is running and on the same network."
    }
    Box(modifier = Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Text(message, color = TextMuted, fontSize = 14.sp)
            Spacer(Modifier.height(16.dp))
            Text(
                "Tap to retry",
                color = Accent,
                fontSize = 14.sp,
                modifier = Modifier
                    .clip(RoundedCornerShape(8.dp))
                    .background(Panel)
                    .clickable(onClick = onRetry)
                    .padding(horizontal = 16.dp, vertical = 10.dp)
                    .semantics { contentDescription = "Retry loading usage" },
            )
        }
    }
}

@Composable
private fun ProviderList(state: UsageUiState) {
    val providers = state.view?.providers ?: emptyList()
    LazyColumn(
        contentPadding = PaddingValues(vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(providers, key = { it.id }) { provider -> ProviderCard(provider) }
    }
}

@Composable
private fun ProviderCard(provider: ProviderUsage) {
    Card(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
    ) {
        Column(modifier = Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    provider.provider.replaceFirstChar { it.uppercase() },
                    color = TextPrimary,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 16.sp,
                )
                Spacer(Modifier.weight(1f))
                Text(providerStatus(provider), color = TextMuted, fontSize = 12.sp)
            }

            if (provider.error.isNotEmpty()) {
                Spacer(Modifier.height(8.dp))
                Text(provider.error, color = Danger, fontSize = 12.sp)
            }

            provider.session?.let {
                Spacer(Modifier.height(12.dp))
                Meter("5-hour window", it.remainingPercent, it.resetsInSeconds, it.resetInferred)
            }
            provider.weekly?.let {
                Spacer(Modifier.height(12.dp))
                Meter("Weekly allowance", it.remainingPercent, it.resetsInSeconds, it.resetInferred)
            }
            provider.pools.forEach { pool ->
                Spacer(Modifier.height(12.dp))
                Meter(pool.label, pool.remainingPercent, pool.resetsInSeconds, pool.resetInferred)
            }
        }
    }
}

@Composable
private fun Meter(label: String, percent: Int, resetsInSeconds: Long, resetInferred: Boolean) {
    Column(modifier = Modifier.semantics {
        contentDescription = "$label: $percent percent remaining, resets ${formatCountdown(resetsInSeconds)}"
    }) {
        Row {
            Text(label, color = TextPrimary, fontSize = 13.sp)
            Spacer(Modifier.weight(1f))
            Text(
                "$percent% left",
                color = TextPrimary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                fontFamily = FontFamily.Monospace,
            )
        }
        Spacer(Modifier.height(6.dp))
        LinearProgressIndicator(
            progress = { percent / 100f },
            modifier = Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(3.dp)),
            color = toneFor(percent),
            trackColor = Line,
        )
        Spacer(Modifier.height(4.dp))
        Text(
            // An inferred reset is a guess, and saying so is cheaper than
            // being subtly wrong.
            if (resetInferred) {
                "Resets ~${formatCountdown(resetsInSeconds)}"
            } else {
                "Resets in ${formatCountdown(resetsInSeconds)}"
            },
            color = TextMuted,
            fontSize = 11.sp,
        )
    }
}

@Preview(showBackground = true, backgroundColor = 0xFF0D0F12)
@Composable
private fun UsageScreenPreview() {
    UsageScreen(
        state = UsageUiState(
            view = UsageView(
                providers = listOf(
                    ProviderUsage(
                        id = "claude-main",
                        provider = "claude",
                        session = Window(remainingPercent = 100, resetsInSeconds = 18000),
                        weekly = Window(remainingPercent = 93, resetsInSeconds = 259200),
                        pools = listOf(
                            Pool(
                                key = "model:fable:weekly",
                                label = "Fable",
                                remainingPercent = 100,
                                resetsInSeconds = 259200,
                                resetInferred = true,
                            ),
                        ),
                    ),
                    ProviderUsage(
                        id = "codex-main",
                        provider = "codex",
                        weekly = Window(remainingPercent = 47, resetsInSeconds = 432000),
                    ),
                ),
            ),
        ),
        onRetry = {},
    )
}
