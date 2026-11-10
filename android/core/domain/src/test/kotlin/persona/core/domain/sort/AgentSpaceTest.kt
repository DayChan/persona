package persona.core.domain.sort

import persona.core.contracts.Session
import org.junit.Assert.*
import org.junit.Test

/** The "By machine and space" tree these tests were written for (the default grouping is now "By repository"). */
private val MachineSpaceTree = AgentListSettings(grouping = AgentGrouping.MachineSpace)

class AgentSpaceTest {
    @Test fun unresolvedProjectsKeepRepositoryIdentityAndNeverImpersonateAWorkspace() {
        fun row(id: String, cwd: String, root: String? = null, space: String? = null) = AgentSession(
            Session(machineId = "machine", sessionId = id, cwd = cwd, repoRoot = root,
                state = "working", spaceId = space, spaceName = if (space == null) null else "project"))
        val rows = listOf(
            row("hosted", "/code/project", space = "workspace"),
            row("root", "/code/project"),
            row("tree", "/trees/task", root = "/code/project"),
            row("other", "/elsewhere/project"),
            row("empty", ""),
        )
        val spaces = agentSpaces(rows)
        assertEquals(4, spaces.size)
        assertEquals(4, spaces.map { it.preferenceKey }.toSet().size)
        assertEquals(setOf("root", "tree"), spaces.single { s -> s.members.any { it.session.sessionId == "root" } }
            .members.map { it.session.sessionId }.toSet())
        val groups = buildAgentList(rows, settings = AgentListSettings(grouping = AgentGrouping.Space)).groups
        assertEquals(1, groups.count { it.title.shown == "project" })
        assertEquals(spaces.map { it.id }.toSet(), agentSpaces(rows.reversed()).map { it.id }.toSet())
        val blankSpace = rows[1].copy(session = rows[1].session.copy(spaceId = ""))
        assertEquals(agentSpaces(listOf(rows[1])).single().id, agentSpaces(listOf(blankSpace)).single().id)
    }

    @Test fun detailKeepsTabOrderAndMuteExceptionsOverrideSpace() {
        val idle = AgentSession(Session(machineId = "machine", sessionId = "idle", state = "idle", spaceId = "workspace", spaceName = "项目", slotIndex = 1))
        val active = AgentSession(idle.session.copy(sessionId = "active", state = "working", slotIndex = 2))
        val space = agentSpaces(listOf(active, idle, AgentSession(idle.session.copy(sessionId = "closed", state = "offline")))).single()
        assertEquals(listOf(idle.id, active.id), space.members.map { it.id })
        val pinned = AgentPreferences().togglePin(idle.id).togglePin(active.id)
        assertEquals(listOf(idle.id, active.id), pinned.sessions)
        assertEquals(listOf(idle.id, active.id), space.members.map { it.id })
        assertEquals(listOf(active.id), buildAgentList(listOf(idle, active), settings = MachineSpaceTree).groups.single().visibleRows.map { it.id })
        val muted = AgentPreferences(spaces = listOf(space.preferenceKey))
        val allowed = muted.toggleMute(idle.id, space.preferenceKey)
        assertFalse(allowed.mutes(idle))
        assertEquals(setOf(idle.id), allowed.allowed)
        val remuted = allowed.toggleMute(idle.id, space.preferenceKey)
        assertTrue(remuted.mutes(idle))
        // Muting again inside a muted space is the agent's own mute (iOS rule): it survives unmuting the space.
        assertEquals(listOf(idle.id), remuted.sessions)
        assertTrue(remuted.allowed.isEmpty())
        assertTrue(remuted.copy(spaces = emptyList()).mutes(idle))
        assertTrue(allowed.copy(sessions = listOf(idle.id)).mutes(idle))
        assertFalse(AgentPreferences().toggleMute(idle.id, null).toggleMute(idle.id, null).mutes(idle))
    }
}
