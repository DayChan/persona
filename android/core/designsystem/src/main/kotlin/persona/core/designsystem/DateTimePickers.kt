package persona.core.designsystem

import androidx.compose.material3.DatePicker
import androidx.compose.material3.DatePickerDefaults
import androidx.compose.material3.DatePickerDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.SelectableDates
import androidx.compose.material3.TimePicker
import androidx.compose.material3.TimePickerDefaults
import androidx.compose.material3.rememberDatePickerState
import androidx.compose.material3.rememberTimePickerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.res.stringResource
import java.time.LocalDate
import java.time.LocalTime
import java.time.ZoneOffset

// The style's date and time pickers (an extension's date, time and datetime fields, UI version 6): Material's, in the
// palette's surface, accent and ink. Dates go in and out as LocalDate (the picker counts in UTC days), times as LocalTime.

/** Picks a day from [earliest] to [latest] (either open), starting at [initial] (today when none). */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun JDatePickerDialog(initial: LocalDate?, onPick: (LocalDate) -> Unit, onDismiss: () -> Unit, earliest: LocalDate? = null, latest: LocalDate? = null) {
    val palette = PersonaTheme.palette
    val bounds = remember(earliest, latest) {
        object : SelectableDates {
            override fun isSelectableDate(utcTimeMillis: Long): Boolean {
                val day = java.time.Instant.ofEpochMilli(utcTimeMillis).atZone(ZoneOffset.UTC).toLocalDate()
                return (earliest == null || !day.isBefore(earliest)) && (latest == null || !day.isAfter(latest))
            }
            override fun isSelectableYear(year: Int): Boolean = (earliest == null || year >= earliest.year) && (latest == null || year <= latest.year)
        }
    }
    val start = initial ?: LocalDate.now().let { today -> when {
        earliest != null && today.isBefore(earliest) -> earliest
        latest != null && today.isAfter(latest) -> latest
        else -> today
    } }
    val state = rememberDatePickerState(initialSelectedDateMillis = start.atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli(), selectableDates = bounds)
    val colors = DatePickerDefaults.colors(containerColor = palette.surfaceOpaque, titleContentColor = palette.ink2, headlineContentColor = palette.ink,
        weekdayContentColor = palette.ink3, dayContentColor = palette.ink, selectedDayContainerColor = palette.accent,
        todayDateBorderColor = palette.accent, todayContentColor = palette.accent, selectedYearContainerColor = palette.accent)
    DatePickerDialog(
        onDismissRequest = onDismiss, colors = colors, shape = PersonaTheme.shapes.panelShape, tonalElevation = androidx.compose.ui.unit.Dp.Hairline,
        confirmButton = {
            JButton(stringResource(R.string.designsystem_done), {
                state.selectedDateMillis?.let { onPick(java.time.Instant.ofEpochMilli(it).atZone(ZoneOffset.UTC).toLocalDate()) }
                onDismiss()
            }, kind = JButtonKind.Primary, size = JButtonSize.Medium, enabled = state.selectedDateMillis != null)
        },
        dismissButton = { JButton(stringResource(R.string.designsystem_cancel), onDismiss, kind = JButtonKind.Quiet, size = JButtonSize.Medium) },
    ) { DatePicker(state, colors = colors, showModeToggle = false) }
}

/** Picks a time of day on a 24-hour dial, starting at [initial] (now when none); [title] says what it is for. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun JTimePickerDialog(initial: LocalTime?, onPick: (LocalTime) -> Unit, onDismiss: () -> Unit, title: String) {
    val palette = PersonaTheme.palette
    val start = initial ?: LocalTime.now()
    val state = rememberTimePickerState(initialHour = start.hour, initialMinute = start.minute, is24Hour = true)
    JDialog(onDismiss, title = title, confirm = stringResource(R.string.designsystem_done) to { onPick(LocalTime.of(state.hour, state.minute)); onDismiss() }) {
        TimePicker(state, colors = TimePickerDefaults.colors(clockDialColor = palette.sunken, selectorColor = palette.accent,
            containerColor = palette.surfaceOpaque, timeSelectorSelectedContainerColor = palette.accentSoft,
            timeSelectorUnselectedContainerColor = palette.sunken, timeSelectorSelectedContentColor = palette.ink,
            timeSelectorUnselectedContentColor = palette.ink2, clockDialSelectedContentColor = ExtensionThemeColors.on(palette.accent),
            clockDialUnselectedContentColor = palette.ink))
    }
}
