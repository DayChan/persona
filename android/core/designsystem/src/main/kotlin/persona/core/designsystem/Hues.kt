package persona.core.designsystem

import androidx.compose.ui.graphics.Color

// Named colours (an extension's `Color`, UI version 6, docs/extensions.md "UI version 6"): a category, not a meaning (that is
// Tone). Each has a solid [StatusColors.dot] for icons, lines and an on tile's edge (3:1 on the page in every style), a
// [StatusColors.text] for a badge's words (4.5:1), a [StatusColors.tint] wash and the ink on the solid colour. Text of a
// page is never drawn in them.

/** The eleven named colours an extension may pick. */
enum class JHue { Red, Orange, Amber, Yellow, Green, Teal, Blue, Indigo, Purple, Pink, Gray;

    companion object {
        /** The extension's name for it (`red`, `amber`…); one this app does not know is none (the quiet ink). */
        fun of(name: String?): JHue? = entries.firstOrNull { it.name.equals(name, ignoreCase = true) }
    }
}

private class HueValues(val dot: Color, val text: Color)

private fun rgb(value: Long) = Color(0xFF000000L or value)

private val LightHues = mapOf(
    JHue.Red to HueValues(rgb(0xD63A30), rgb(0xB3261E)),
    JHue.Orange to HueValues(rgb(0xD9590C), rgb(0xA84300)),
    JHue.Amber to HueValues(rgb(0xB87A00), rgb(0x8A5A00)),
    JHue.Yellow to HueValues(rgb(0xA68A00), rgb(0x6E5A00)),
    JHue.Green to HueValues(rgb(0x258A4F), rgb(0x1D7A4C)),
    JHue.Teal to HueValues(rgb(0x0E8A84), rgb(0x0B6E69)),
    JHue.Blue to HueValues(rgb(0x2E6FD0), rgb(0x2259B0)),
    JHue.Indigo to HueValues(rgb(0x4F55D9), rgb(0x3E43B8)),
    JHue.Purple to HueValues(rgb(0x7A5AF8), rgb(0x6544D6)),
    JHue.Pink to HueValues(rgb(0xD6337A), rgb(0xAD2662)),
    JHue.Gray to HueValues(rgb(0x7C7C82), rgb(0x5E5E62)),
)

private val DarkHues = mapOf(
    JHue.Red to HueValues(rgb(0xFF6B61), rgb(0xFF8A82)),
    JHue.Orange to HueValues(rgb(0xFF9F0A), rgb(0xFFB04A)),
    JHue.Amber to HueValues(rgb(0xFFC233), rgb(0xFFD066)),
    JHue.Yellow to HueValues(rgb(0xFFE14D), rgb(0xFFE97A)),
    JHue.Green to HueValues(rgb(0x4CC38A), rgb(0x6FD6A1)),
    JHue.Teal to HueValues(rgb(0x3CC9C0), rgb(0x6EDCD5)),
    JHue.Blue to HueValues(rgb(0x5B9BFF), rgb(0x8AB8FF)),
    JHue.Indigo to HueValues(rgb(0x8088FF), rgb(0xA3A9FF)),
    JHue.Purple to HueValues(rgb(0x9D8BFF), rgb(0xB9ACFF)),
    JHue.Pink to HueValues(rgb(0xFF6FA8), rgb(0xFF95BF)),
    JHue.Gray to HueValues(rgb(0x98989F), rgb(0xB5B5BB)),
)

/** How strongly an on tile, a badge or an icon's well is washed in its colour. */
const val HueTintAlpha = 0.14f

/** [hue] in this palette's mode; dark washes a little more, or the tint vanishes into the page. */
fun PersonaPalette.hue(hue: JHue): StatusColors {
    val values = (if (dark) DarkHues else LightHues).getValue(hue)
    val alpha = if (dark) HueTintAlpha + 0.04f else HueTintAlpha
    return StatusColors(values.dot, values.text, values.dot.copy(alpha = alpha), ExtensionThemeColors.on(values.dot))
}

/** A named colour, or the quiet ink when none (or one this app does not know). */
fun PersonaPalette.hueOr(hue: JHue?, quiet: Color = ink2): Color = hue?.let { hue(it).dot } ?: quiet
