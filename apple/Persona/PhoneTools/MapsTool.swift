import CoreLocation
import Foundation
import MapKit

/// Nearby search and handing a destination to Maps (#2281). Navigation happens in the Maps app: the tool says it opened
/// Maps, never that Persona navigates.
enum MapsTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_maps_search_nearby",
                    description: """
                    Search places near the user (or near the latitude / longitude given), such as "quiet cafe" or \
                    "pharmacy", with Apple Maps. Returns each place's name, address, latitude, longitude, distance in \
                    meters from the search center, category, phone and website, nearest first, and the source. \
                    Apple Maps does not give opening hours to apps: say so instead of guessing. Use \
                    phone_maps_navigate to hand one to Maps.
                    """,
                    inputSchema: Schema.object([
                        "query": Schema.string("What to look for, e.g. quiet cafe."),
                        "latitude": Schema.number("Search center latitude. Default: the phone's current location."),
                        "longitude": Schema.number("Search center longitude. Default: the phone's current location."),
                        "radius_meters": Schema.integer("How far around the center to look, default 1500, 100–50000.", min: 100, max: 50000),
                        "limit": Schema.integer("Maximum number of places, default 8, max 20.", min: 1, max: 20),
                    ], required: ["query"])
                ),
                source: .location,
                run: { args in try await searchNearby(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_maps_navigate",
                    description: """
                    Open directions to a place in the Apple Maps app, with a transport mode. Navigation then runs in \
                    Maps, not in Persona: tell the user Maps has opened, do not say you are navigating. Persona must \
                    be open on the iPhone.
                    """,
                    inputSchema: Schema.object([
                        "latitude": Schema.number("Destination latitude."),
                        "longitude": Schema.number("Destination longitude."),
                        "name": Schema.string("Destination name shown in Maps."),
                        "mode": Schema.string("driving (default), walking or transit.", values: ["driving", "walking", "transit"]),
                    ], required: ["latitude", "longitude"])
                ),
                source: .location,
                run: { args in try await navigate(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// A destination handed to Maps.
    nonisolated struct Destination: Equatable, Sendable {
        nonisolated enum Mode: String, Sendable { case driving, walking, transit }

        var latitude: Double
        var longitude: Double
        var name: String?
        var mode: Mode

        static func parse(_ args: JSONValue) throws -> Destination {
            guard let latitude = args["latitude"]?.double, (-90...90).contains(latitude),
                  let longitude = args["longitude"]?.double, (-180...180).contains(longitude) else {
                throw ToolFailure(.invalidArgument, "latitude (-90…90) and longitude (-180…180) are required")
            }
            let raw = ToolArgs.string(args, "mode") ?? Mode.driving.rawValue
            guard let mode = Mode(rawValue: raw) else {
                throw ToolFailure(.invalidArgument, "mode must be driving, walking or transit")
            }
            return Destination(latitude: latitude, longitude: longitude, name: ToolArgs.string(args, "name"), mode: mode)
        }

        var directionsMode: String {
            switch mode {
            case .driving: MKLaunchOptionsDirectionsModeDriving
            case .walking: MKLaunchOptionsDirectionsModeWalking
            case .transit: MKLaunchOptionsDirectionsModeTransit
            }
        }
    }

    #if os(iOS)
    static func searchNearby(_ args: JSONValue) async throws -> [ToolContent] {
        guard let query = ToolArgs.string(args, "query") else {
            throw ToolFailure(.invalidArgument, "query is required")
        }
        let radius = Double(ToolArgs.int(args, "radius_meters", fallback: 1500, min: 100, max: 50000))
        let limit = ToolArgs.int(args, "limit", fallback: 8, min: 1, max: 20)
        let center: CLLocation
        var centerSource = "given"
        if let latitude = args["latitude"]?.double, let longitude = args["longitude"]?.double {
            center = CLLocation(latitude: latitude, longitude: longitude)
        } else {
            switch LocationAccess.status() {
            case .granted, .limited, .notNeeded: break
            case .denied: throw ToolFailure(.denied, "Location")
            case .notDetermined: throw ToolFailure(.denied, "Location access has not been requested yet")
            }
            center = try await OneShotLocation.fetch(timeout: 15)
            centerSource = "current_location"
        }

        let request = MKLocalSearch.Request()
        request.naturalLanguageQuery = query
        request.region = MKCoordinateRegion(center: center.coordinate, latitudinalMeters: radius * 2, longitudinalMeters: radius * 2)
        request.resultTypes = .pointOfInterest
        let items: [MKMapItem]
        do {
            items = try await MKLocalSearch(request: request).start().mapItems
        } catch {
            throw ToolFailure(.unavailable, "Apple Maps found nothing for \"\(query)\": \(error.localizedDescription)")
        }
        let rows = items.map { item -> (Double, JSONValue) in
            let place = item.placemark
            let distance = center.distance(from: CLLocation(latitude: place.coordinate.latitude, longitude: place.coordinate.longitude))
            return (distance, Output.object([
                ("name", Output.opt(item.name)),
                ("address", Output.opt(place.title)),
                ("latitude", .number(place.coordinate.latitude)),
                ("longitude", .number(place.coordinate.longitude)),
                ("distance_meters", .number(distance.rounded())),
                ("category", Output.opt(item.pointOfInterestCategory?.rawValue)),
                ("phone", Output.opt(item.phoneNumber)),
                ("url", Output.opt(item.url?.absoluteString)),
            ]))
        }
        let nearest = rows.filter { $0.0 <= radius * 1.5 }.sorted { $0.0 < $1.0 }.prefix(limit).map(\.1)
        return Output.json(Output.object([
            ("query", .string(query)),
            ("center", Output.object([("latitude", .number(center.coordinate.latitude)),
                                      ("longitude", .number(center.coordinate.longitude)),
                                      ("from", .string(centerSource))])),
            ("radius_meters", .number(radius)),
            ("count", .number(Double(nearest.count))),
            ("places", .array(Array(nearest))),
            ("source", .string("Apple Maps (MapKit local search)")),
            ("note", .string("Opening hours are not available from Apple Maps to apps.")),
        ]))
    }

    static func navigate(_ args: JSONValue) async throws -> [ToolContent] {
        let destination = try Destination.parse(args)
        try Foreground.require("open Maps")
        let coordinate = CLLocationCoordinate2D(latitude: destination.latitude, longitude: destination.longitude)
        let item = MKMapItem(placemark: MKPlacemark(coordinate: coordinate))
        item.name = destination.name
        let opened = item.openInMaps(launchOptions: [MKLaunchOptionsDirectionsModeKey: destination.directionsMode])
        guard opened else {
            throw ToolFailure(.unavailable, "iOS did not open Maps. Ask the user to open Persona and try again.")
        }
        return Output.json(Output.object([
            ("opened", .string("maps")),
            ("destination", Output.opt(destination.name)),
            ("latitude", .number(destination.latitude)),
            ("longitude", .number(destination.longitude)),
            ("mode", .string(destination.mode.rawValue)),
            ("note", .string("Directions are open in the Maps app; navigation runs there, not in Persona.")),
        ]))
    }
    #endif
}
