@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.navigation

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ExtensionCatalogRouteCompatibilityTest {
    private val json = Json

    @Test fun savedCatalogWithoutManagerStillRestores() {
        val stored = """{"marketId":"example-market","extensionId":"example-notes"}"""
        val restored = json.decodeFromString(ExtensionCatalogRoute.serializer(), stored)
        assertEquals(ExtensionCatalogRoute("example-market", "example-notes"), restored)
        assertNull(restored.manager)
        assertEquals("persona.core.navigation.PluginCatalogRoute", ExtensionCatalogRoute.serializer().descriptor.serialName)
        assertEquals(json.parseToJsonElement(stored),
            json.parseToJsonElement(json.encodeToString(ExtensionCatalogRoute.serializer(), restored)))
    }

    @Test fun savedCatalogRetainsItsManager() {
        val route = ExtensionCatalogRoute("example-market", "example-notes", manager = "example-manager")
        assertEquals(route, json.decodeFromString(ExtensionCatalogRoute.serializer(),
            json.encodeToString(ExtensionCatalogRoute.serializer(), route)))
    }
}
