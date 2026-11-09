import Foundation
import Testing
@testable import Persona

/// Saved Places' view model against a FakeHub (#1598): the list as the hub keeps it, adding and changing a place, a
/// refused save, delete and its rollback; and the editor's rules that need neither the hub nor MapKit.
@MainActor
struct SavedPlacesModelTests {
    private static let places = #"""
        [{"id":"pl1","name":"家","address":"1 Example Road","latitude":31.2304,"longitude":121.4737,"radius_meters":150,
          "created_at":"2026-11-08T09:00:00Z","updated_at":"2026-11-08T09:00:00Z"},
         {"id":"pl2","name":"公司","latitude":31.24,"longitude":121.5,"radius_meters":300,
          "created_at":"2026-11-08T09:01:00Z","updated_at":"2026-11-08T09:01:00Z"}]
        """#

    private final class Sink {
        var errors: [String] = []
        var toasts: [String] = []
    }

    private func make(_ hub: FakeHub, _ sink: Sink) -> SavedPlacesModel {
        SavedPlacesModel(deps: .init(api: { hub.client }, report: { sink.errors.append($0.localizedDescription) },
                                     toast: { sink.toasts.append($0.message) }, open: { _ in }))
    }

    private func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/places", json: #"{"places":\#(Self.places)}"#)
        return hub
    }

    @Test func listsThePlacesInTheHubsOrder() async {
        let page = make(hub(), Sink())
        await page.load()
        #expect(page.loaded && page.error == nil)
        #expect(page.places.map(\.id) == ["pl1", "pl2"])
        #expect(page.places[0].address == "1 Example Road" && page.places[0].radiusMeters == 150)
        #expect(page.places[1].address == "", "no address is an empty one")
    }

    @Test func aFailedLoadKeepsTheErrorAndStillFinishes() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/places") { _ in throw APIError(status: 503, message: "no database") }
        let page = make(hub, Sink())
        await page.load()
        #expect(page.loaded && page.error != nil && page.places.isEmpty)
    }

    /// A new place goes up as a POST with its name trimmed and its coordinates and radius; the hub's copy joins the list.
    @Test func addingPostsThePlaceAndKeepsWhatTheHubReturns() async throws {
        let hub = hub()
        hub.on("POST", "/api/v1/assistant/places") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as! [String: Any]
            #expect(body["name"] as? String == "健身房")
            #expect(body["latitude"] as? Double == 31.2 && body["longitude"] as? Double == 121.4)
            #expect(body["radius_meters"] as? Int == 300)
            return Data(#"{"place":{"id":"pl3","name":"健身房","latitude":31.2,"longitude":121.4,"radius_meters":300}}"#.utf8)
        }
        let page = make(hub, Sink())
        await page.load()
        let saved = await page.save(APIClient.SavedPlace(name: " 健身房 ", latitude: 31.2, longitude: 121.4, radiusMeters: 300))
        #expect(saved?.id == "pl3")
        #expect(page.places.map(\.id) == ["pl1", "pl2", "pl3"])
        #expect(hub.sent("POST", "/api/v1/assistant/places").count == 1)
    }

    @Test func changingPutsThePlaceAndReplacesItsRow() async {
        let hub = hub()
        hub.on("PUT", "/api/v1/assistant/places/pl2", json: #"{"place":{"id":"pl2","name":"公司","address":"3 Example Road","latitude":31.25,"longitude":121.51,"radius_meters":500}}"#)
        let page = make(hub, Sink())
        await page.load()
        var p = page.places[1]
        p.address = "3 Example Road"
        p.radiusMeters = 500
        let saved = await page.save(p)
        #expect(saved?.radiusMeters == 500)
        #expect(page.places.count == 2 && page.places[1].address == "3 Example Road" && page.places[1].latitude == 31.25)
    }

    /// The hub refuses a name another place has: the user is told why, nothing changes and the editor stays open.
    @Test func aRefusedSaveReportsAndChangesNothing() async {
        let hub = hub()
        hub.on("POST", "/api/v1/assistant/places") { _ in throw APIError(status: 409, message: "There is already a place called “家”.") }
        let sink = Sink()
        let page = make(hub, sink)
        await page.load()
        let saved = await page.save(APIClient.SavedPlace(name: "家", latitude: 1, longitude: 1))
        #expect(saved == nil)
        #expect(sink.errors.count == 1)
        #expect(page.places.map(\.id) == ["pl1", "pl2"])
    }

    @Test func deleteRemovesTheRowAndSaysSo() async {
        let hub = hub()
        hub.on("DELETE", "/api/v1/assistant/places/pl1", json: #"{"ok":true}"#)
        let sink = Sink()
        let page = make(hub, sink)
        await page.load()
        await page.delete(page.places[0])
        #expect(page.places.map(\.id) == ["pl2"])
        #expect(sink.toasts.count == 1 && sink.errors.isEmpty)
    }

    @Test func aFailedDeleteBringsTheRowBack() async {
        let hub = hub()
        hub.on("DELETE", "/api/v1/assistant/places/pl1") { _ in throw APIError(status: 403, message: "read only") }
        let sink = Sink()
        let page = make(hub, sink)
        await page.load()
        await page.delete(page.places[0])
        #expect(page.places.map(\.id) == ["pl1", "pl2"], "reloaded from the hub")
        #expect(sink.errors.count == 1)
    }

    @Test func theEditorSavesANamedPlaceWithALocationOnly() {
        let place = APIClient.SavedPlace(name: "家", latitude: 31.2, longitude: 121.4)
        #expect(SavedPlaceText.canSave(place, located: true))
        #expect(!SavedPlaceText.canSave(place, located: false), "a location must be picked first")
        #expect(!SavedPlaceText.canSave(APIClient.SavedPlace(name: "  ", latitude: 31.2, longitude: 121.4), located: true))
    }

    @Test func aRadiusOutsideThePresetsIsOffered() {
        #expect(SavedPlaceText.radii(with: 150) == SavedPlaceText.radii)
        #expect(SavedPlaceText.radii(with: 250) == [100, 150, 250, 300, 500, 1000, 2000])
        #expect(SavedPlaceText.radii.contains(APIClient.SavedPlace.defaultRadius))
        #expect(SavedPlaceText.radii.allSatisfy { APIClient.SavedPlace.radiusRange.contains($0) })
    }

    @Test func aPlaceWithoutAnAddressShowsItsCoordinates() {
        let p = APIClient.SavedPlace(name: "公司", latitude: 31.24, longitude: 121.5)
        #expect(SavedPlaceText.detail(p) == SavedPlaceText.coordinates(p))
        #expect(SavedPlaceText.detail(APIClient.SavedPlace(name: "家", address: "1 Example Road")) == "1 Example Road")
    }
}
