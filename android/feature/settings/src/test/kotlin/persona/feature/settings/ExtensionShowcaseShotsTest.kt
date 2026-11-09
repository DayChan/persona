package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionView
import persona.core.designsystem.*
import java.io.File
import coil3.ImageLoader
import coil3.asImage
import coil3.decode.DataSource
import coil3.intercept.Interceptor
import coil3.request.ErrorResult
import coil3.request.SuccessResult
import persona.core.contracts.api.ExtensionImageSource
import persona.core.contracts.api.ExtensionViewItem

/**
 * The component showcase extension's pages (#1852: schema/extension/examples/showcase, copied from the extension's snapshots/) drawn
 * on the JVM with Robolectric's native graphics, to compare with the web's, iOS's and the Mac's screenshots of the same
 * views. Every page in every style, light and dark. Written to
 * `build/extension-showcase-shots/` (or `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h4000dp-xhdpi")
class ExtensionShowcaseShotsTest {
    @get:Rule val rule = createComposeRule()

    private val pages: Map<String, ExtensionView> = File("../../../schema/extension/examples/showcase").listFiles { f -> f.extension == "json" }!!
        .sortedBy { it.name }
        .associate { it.nameWithoutExtension to signed(JsonCodec.decodeFromString(ExtensionView.serializer(), it.readText())) }

    /**
     * Pictures as the app gets them (UI version 6): each `ref` with the path the hub signs for it, and every picture,
     * the hub's or an https one, drawn here at once (no network), but `missing`, which fails as an unreachable one would.
     */
    private val pictures: ImageLoader by lazy {
        val context = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>()
        ImageLoader.Builder(context).coroutineContext(kotlinx.coroutines.Dispatchers.Unconfined).components {
            add(Interceptor { chain ->
                val key = when (val data = chain.request.data) { is ExtensionHubPicture -> data.path.substringAfterLast('/'); else -> data.toString() }
                if (key.contains("missing")) ErrorResult(null, chain.request, IllegalStateException("missing"))
                else SuccessResult(ShowcasePictures.bitmap(key).asImage(), chain.request, DataSource.MEMORY)
            })
        }.build()
    }

    private val handle by lazy { ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {}, pictures = pictures) }

    // https pictures go to the app's ordinary loader: here the same one, which draws them without the network
    @org.junit.Before fun pictures() { coil3.SingletonImageLoader.setUnsafe(pictures) }
    @org.junit.After fun releasePictures() {
        coil3.SingletonImageLoader.reset()
        pictures.shutdown()
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/extension-showcase-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        // markdown is parsed off the main thread (the renderer's own coroutine), which waitForIdle does not wait for
        if (name.contains("markdown") || name.contains("detail")) repeat(3) { Thread.sleep(200); rule.mainClock.advanceTimeBy(100); rule.waitForIdle() }
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        assertTrue("$name drew nothing", other > 50)
    }

    private fun compact(name: String) = name == "card" || name.startsWith("card-") || name.startsWith("widget-") || name.startsWith("shortcut-")

    @Test fun readsEveryPage() {
        assertTrue("${pages.size} pages", pages.size > 30)
        // every component the schema has is on some page, and the app reads it as that component
        val types = pages.values.flatMap { v -> v.sections.flatMap { s -> s.items.map { it.type } } }.toSet()
        assertEquals(emptySet<String>(), setOf("text", "metric", "progress", "row", "button", "link", "field", "divider", "chart",
            "stat_grid", "segmented", "markdown", "table", "empty", "loading", "image", "grid", "carousel") - types)
        // and the UI version 6 parts (#5464): controls, actions, sparks, the new charts and fields, colours
        val items = pages.values.flatMap { v -> v.sections.flatMap { it.items } }
        assertEquals(setOf("switch", "slider", "stepper"), items.mapNotNull { it.control?.kind }.toSet())
        assertTrue(items.any { !it.actions.isNullOrEmpty() } && items.any { it.spark != null })
        assertEquals(emptySet<String>(), setOf("area", "candlestick", "donut", "pie") - items.filter { it.type == "chart" }.mapNotNull { it.kind }.toSet())
        assertEquals(emptySet<String>(), setOf("date", "time", "datetime", "multichoice", "multiline") - items.filter { it.type == "field" }.mapNotNull { it.kind }.toSet())
        // the app draws every page it reads: no v6 item is skipped as unknown
        items.filter { it.type in setOf("image", "grid", "carousel") }.forEach { assertTrue("${it.type} ${it.key}", ExtensionViewText.drawable(it)) }
    }

    @Test fun drawsEveryPage() {
        var name by mutableStateOf(pages.keys.first())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val view = pages.getValue(name)
                val state = ExtensionViewState(view, values = ExtensionViewText.fields(view), loadingMore = null)
                // each page its own composition, as each page of the app is (the search box keeps what was typed)
                key(name) { Box(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                    if (compact(name)) JCard(Modifier.padding(Tokens.Space.l)) { ExtensionCardContent(state, handle) }
                    else LazyColumn(Modifier.fillMaxWidth(), contentPadding = PaddingValues(horizontal = Tokens.Space.l, vertical = Tokens.Space.l)) {
                        extensionViewItems(name, state, handle)
                    }
                } }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (p in pages.keys) {
            name = p; dark = d; style = s
            capture("showcase-$p-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }
}

/** What the hub writes for a ref (UI version 6): its own signed path; the test's "signature" is the ref itself. */
private fun signedSource(src: ExtensionImageSource?): ExtensionImageSource? = src?.ref?.let { ref ->
    src.copy(path = "/api/v1/extensions/showcase/-/image/" + ref.replace(Regex("[^A-Za-z0-9_.-]"), "_"))
} ?: src

private fun signed(item: ExtensionViewItem): ExtensionViewItem = item.copy(
    src = signedSource(item.src),
    thumbnail = item.thumbnail?.let { t -> t.ref?.let { t.copy(path = signedSource(ExtensionImageSource(ref = it))?.path) } ?: t },
    avatar = item.avatar?.let { a -> a.ref?.let { a.copy(path = signedSource(ExtensionImageSource(ref = it))?.path) } ?: a },
    tiles = item.tiles?.map { it.copy(image = signedSource(it.image)) },
)

private fun signed(view: ExtensionView): ExtensionView = view.copy(sections = view.sections.map { s -> s.copy(items = s.items.map(::signed)) })

/** Pictures drawn for the showcase's refs and urls: a sky, a ground and a sun, in colours of the key, so each looks its own. */
internal object ShowcasePictures {
    fun bitmap(key: String): android.graphics.Bitmap {
        val bitmap = android.graphics.Bitmap.createBitmap(640, 360, android.graphics.Bitmap.Config.ARGB_8888)
        val canvas = android.graphics.Canvas(bitmap)
        val hash = key.hashCode()
        val hue = ((hash ushr 1) % 360).toFloat()
        fun hsv(h: Float, s: Float, v: Float) = android.graphics.Color.HSVToColor(floatArrayOf((h + 360) % 360, s, v))
        val paint = android.graphics.Paint(android.graphics.Paint.ANTI_ALIAS_FLAG)
        paint.shader = android.graphics.LinearGradient(0f, 0f, 0f, 360f, hsv(hue, 0.45f, 0.95f), hsv(hue + 30, 0.6f, 0.7f), android.graphics.Shader.TileMode.CLAMP)
        canvas.drawRect(0f, 0f, 640f, 360f, paint)
        paint.shader = null
        paint.color = hsv(hue + 120, 0.5f, 0.55f)
        canvas.drawOval(-120f, 230f, 760f, 520f, paint)
        paint.color = hsv(hue + 200, 0.25f, 1f)
        canvas.drawCircle(480f, 110f, 46f, paint)
        if (key.contains("map")) {
            paint.color = android.graphics.Color.argb(160, 255, 255, 255); paint.strokeWidth = 6f; paint.style = android.graphics.Paint.Style.STROKE
            canvas.drawRect(80f, 60f, 560f, 300f, paint); canvas.drawLine(320f, 60f, 320f, 300f, paint); canvas.drawLine(80f, 180f, 320f, 180f, paint)
        }
        return bitmap
    }
}
