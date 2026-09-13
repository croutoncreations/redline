package ai.redline.app

import android.Manifest
import android.content.pm.PackageManager
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.common.InputImage
import java.util.concurrent.Executors

/**
 * The pairing screen: point the phone at the QR code from `redline pair --qr`.
 *
 * Scanning happens entirely on device. Sending a frame containing a pairing
 * credential to a cloud recogniser would hand it to a third party, which is
 * exactly what pairing exists to avoid.
 */
@Composable
fun PairingScreen(state: PairingUiState, onScanned: (String) -> Unit) {
    val context = LocalContext.current
    var hasCamera by remember {
        mutableStateOf(
            ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) ==
                PackageManager.PERMISSION_GRANTED,
        )
    }
    val requestCamera = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted -> hasCamera = granted }

    LaunchedEffect(Unit) {
        if (!hasCamera) requestCamera.launch(Manifest.permission.CAMERA)
    }

    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(
            Modifier
                .fillMaxSize()
                .padding(24.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            Image(
                painter = painterResource(R.drawable.redline_mark),
                contentDescription = null,
                modifier = Modifier.height(40.dp),
            )
            Spacer(Modifier.height(20.dp))
            Text("Pair with Redline", color = TextPrimary, fontSize = 20.sp)
            Spacer(Modifier.height(8.dp))
            Text(
                "Run redline pair --qr on your Mac, then point the camera at the code.",
                color = TextMuted,
                fontSize = 13.sp,
                textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(24.dp))

            Box(
                Modifier
                    .fillMaxWidth()
                    .height(320.dp)
                    .clip(RoundedCornerShape(16.dp))
                    .background(Panel),
                contentAlignment = Alignment.Center,
            ) {
                when {
                    state.working -> Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        CircularProgressIndicator(color = Accent)
                        Spacer(Modifier.height(12.dp))
                        Text("Pairing", color = TextMuted, fontSize = 13.sp)
                    }

                    hasCamera -> QrScanner(onScanned = onScanned)

                    else -> Column(
                        Modifier.padding(24.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        Text(
                            "Redline needs the camera to scan the pairing code.",
                            color = TextMuted,
                            fontSize = 13.sp,
                            textAlign = TextAlign.Center,
                        )
                        Spacer(Modifier.height(12.dp))
                        Text(
                            "Allow camera access",
                            color = Accent,
                            fontSize = 14.sp,
                            modifier = Modifier.clickable {
                                requestCamera.launch(Manifest.permission.CAMERA)
                            },
                        )
                    }
                }
            }

            if (state.error != null) {
                Spacer(Modifier.height(16.dp))
                Text(
                    state.error,
                    color = Danger,
                    fontSize = 13.sp,
                    textAlign = TextAlign.Center,
                )
            }
        }
    }
}

/**
 * A camera preview that reports QR codes as it sees them.
 *
 * The same code is reported on many consecutive frames; de-duplication is the
 * view model's job, since only it knows whether a redemption is already in
 * flight.
 */
@Composable
private fun QrScanner(onScanned: (String) -> Unit) {
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current
    val executor = remember { Executors.newSingleThreadExecutor() }
    val scanner = remember {
        BarcodeScanning.getClient(
            com.google.mlkit.vision.barcode.BarcodeScannerOptions.Builder()
                .setBarcodeFormats(Barcode.FORMAT_QR_CODE)
                .build(),
        )
    }

    DisposableEffect(Unit) {
        onDispose {
            executor.shutdown()
            scanner.close()
        }
    }

    AndroidView(
        modifier = Modifier.fillMaxSize(),
        factory = { viewContext ->
            val previewView = PreviewView(viewContext)
            val providerFuture = ProcessCameraProvider.getInstance(viewContext)
            providerFuture.addListener({
                val provider = providerFuture.get()
                val preview = androidx.camera.core.Preview.Builder().build().also {
                    it.setSurfaceProvider(previewView.surfaceProvider)
                }
                val analysis = ImageAnalysis.Builder()
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                    .build()
                analysis.setAnalyzer(executor) { image ->
                    scanFrame(image, scanner, onScanned)
                }
                provider.unbindAll()
                provider.bindToLifecycle(
                    lifecycleOwner,
                    CameraSelector.DEFAULT_BACK_CAMERA,
                    preview,
                    analysis,
                )
            }, ContextCompat.getMainExecutor(viewContext))
            previewView
        },
    )
}

@androidx.annotation.OptIn(androidx.camera.core.ExperimentalGetImage::class)
private fun scanFrame(
    proxy: ImageProxy,
    scanner: com.google.mlkit.vision.barcode.BarcodeScanner,
    onScanned: (String) -> Unit,
) {
    val media = proxy.image
    if (media == null) {
        proxy.close()
        return
    }
    val image = InputImage.fromMediaImage(media, proxy.imageInfo.rotationDegrees)
    scanner.process(image)
        .addOnSuccessListener { barcodes ->
            barcodes.firstNotNullOfOrNull { it.rawValue }?.let(onScanned)
        }
        // The frame must be closed whatever happens, or the camera stalls once
        // the buffer fills.
        .addOnCompleteListener { proxy.close() }
}
