package persona.core.contracts

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionPackMember

class ExtensionDetailTest {
    @Test fun installedDetailsRetainDependencyStatesAndPackConstraints() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{
            "id":"example-pack",
            "depends":[
                {"id":"base","name":"Base","version":">=2.0.0","state":"old","installed":"1.0.0"},
                {"id":"missing","state":"missing"},
                {"id":"waiting","state":"not_ready"},
                {"id":"disabled","state":"disabled"},
                {"id":"ready","state":"ok"},
                {"id":"future","state":"future-state"}
            ],
            "extension_pack":[{"id":"member","market":"example","version":"^1.0.0"},{"id":"other"}]
        }"""))
        assertEquals(listOf("old", "missing", "not_ready", "disabled", "ok", "future-state"), detail.depends.map { it.state })
        assertEquals("Base", detail.depends.first().name)
        assertEquals(">=2.0.0", detail.depends.first().version)
        assertEquals("1.0.0", detail.depends.first().installed)
        assertEquals(listOf(ExtensionPackMember("member", "example", "^1.0.0"), ExtensionPackMember("other")), detail.extensionPack)
    }

    @Test fun olderHubsHaveNoDependencyOrMemberSections() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{"id":"example"}"""))
        assertTrue(detail.depends.isEmpty())
        assertTrue(detail.extensionPack.isEmpty())
    }
}
