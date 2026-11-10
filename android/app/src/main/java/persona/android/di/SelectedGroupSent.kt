package persona.android.di

import persona.core.contracts.api.HubSent
import persona.core.contracts.api.SentItem
import persona.core.contracts.api.SentPage

/**
 * Settings › Sent as you on the selected hub group (docs/android/android.md "Multiple hubs"): each read goes to that group's hub, and
 * the rows' machine ids come back in the form the merged lists use (GroupKeys: the group's suffix on every group but the
 * primary one), so the chat a row opens is found in the group it belongs to. [selected] says which group that is, read once
 * per call: the hub it reads and the suffix it applies are the same group's.
 */
internal class SelectedGroupSent(private val selected: () -> Group) : HubSent {
    /** A group's hub for the ledger and the suffix its machine ids carry in the merged lists (null: the primary group). */
    class Group(val hub: HubSent, val namespace: String?)

    override suspend fun page(before: Long?, limit: Int?, connector: String?): SentPage {
        val group = selected()
        return group.hub.page(before, limit, connector).inGroup(group.namespace)
    }

    override suspend fun recall(id: Long): SentItem? {
        val group = selected()
        return group.hub.recall(id)?.let { SentPage(listOf(it)).inGroup(group.namespace).items.single() }
    }
}
