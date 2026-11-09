package persona.feature.settings

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/** The host-owned entry uses exactly the web's settings-sent.entry_label and hint. */
@RunWith(AndroidJUnit4::class)
class SentEntryTextTest {
    private fun check(title: String, hint: String) {
        val resources = ApplicationProvider.getApplicationContext<Context>().resources
        assertEquals(title, resources.getString(R.string.settings_sent_title))
        assertEquals(hint, resources.getString(R.string.settings_sent_subtitle))
    }

    @Config(sdk = [35], qualifiers = "en")
    @Test fun englishMatchesWeb() = check("Records sent as you", "What Persona sent in your name, and why it was allowed")

    @Config(sdk = [35], qualifiers = "zh")
    @Test fun simplifiedChineseMatchesWeb() = check("以你的名义发出的记录", "以你的名义发出过什么、为什么放行")

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun japaneseMatchesWeb() = check("あなたの名前で送信した記録", "あなたの名前で送信した内容と、許可された理由")
}
