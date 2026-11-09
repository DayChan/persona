import CoreLocation
import MapKit
import SwiftUI

/// Settings › Assistant › Saved Places (#1598): home, the office and the other places the user names. A reminder by place
/// ("remind me to buy milk on the way home") goes off at the coordinates kept here; the assistant adds one only after
/// the user confirmed it in a chat, and the user can correct any of them here.
struct SavedPlacesView: View {
    @State private var page = SavedPlacesModel()
    @State private var editing: APIClient.SavedPlace?
    @State private var creating: APIClient.SavedPlace?

    var body: some View {
        List {
            if !page.places.isEmpty {
                Section {
                    ForEach(page.places) { p in row(p) }
                } footer: {
                    Text("Persona uses these for reminders like “remind me to buy milk on the way home”. It adds a place here only after you confirm it in a chat.")
                }
            }
        }
        .themedPage()
        .overlay {
            if !page.loaded {
                ProgressView()
            } else if let error = page.error, page.places.isEmpty {
                ContentUnavailableView("Couldn't Load Saved Places", systemImage: Theme.Icon.error, description: Text(error))
            } else if page.places.isEmpty {
                ContentUnavailableView("No Saved Places", systemImage: Theme.Icon.place,
                                       description: Text("Add home, the office and other places you go, so a reminder like “remind me to buy milk on the way home” goes off at the right spot."))
            }
        }
        .pageTitle(String(localized: "Saved Places"))
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Button { creating = APIClient.SavedPlace() } label: { ThemedIcon(Theme.Icon.add) }
                    .accessibilityLabel("New Place")
                    .accessibilityIdentifier("saved-places-add")
            }
        }
        .navigationDestination(item: $editing) { p in
            SavedPlaceEditor(place: p, save: page.save, delete: page.delete)
        }
        .sheet(item: $creating) { p in
            NavigationStack { SavedPlaceEditor(place: p, save: page.save, delete: page.delete) }
        }
        .task { await load() }
        .refreshable { await page.load() }
    }

    private func row(_ p: APIClient.SavedPlace) -> some View {
        Button { editing = p } label: { SavedPlaceRow(place: p) }
            .foregroundStyle(.primary)
            .accessibilityIdentifier("saved-place-\(p.id)")
            .swipeActions(edge: .trailing) {
                Button(role: .destructive) { Task { await page.delete(p) } } label: { Label("Delete", systemImage: Theme.Icon.delete) }
            }
            .contextMenu {
                Button { editing = p } label: { Label("Edit [Message Menu 051836]", systemImage: Theme.Icon.compose) }
                Button(role: .destructive) { Task { await page.delete(p) } } label: { Label("Delete", systemImage: Theme.Icon.delete) }
            }
    }

    private func load() async {
        await page.load()
        #if DEBUG
        switch UserDefaults.standard.string(forKey: "openAssistantPage") {
        case "place" where editing == nil: editing = page.places.first
        case "newPlace" where creating == nil: creating = APIClient.SavedPlace()
        default: break
        }
        #endif
    }
}

/// One saved place: its mark, its name, its address (or coordinates) and the reminder's radius.
struct SavedPlaceRow: View {
    let place: APIClient.SavedPlace

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.place)
                .font(Theme.Font.heading)
                .foregroundStyle(.white)
                .scaledFrame(width: Theme.Size.wellM, height: Theme.Size.wellM)
                .background(Theme.accent.gradient, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(place.name).font(Theme.Font.heading).lineLimit(1)
                Text(SavedPlaceText.detail(place)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: Theme.Space.s)
            Text(SavedPlaceText.radius(place.radiusMeters)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .contentShape(.rect)
    }
}

/// Adding or changing a saved place: its name, where it is (searched by address or name, or the current location),
/// a map with the reminder's circle, and the circle's radius.
struct SavedPlaceEditor: View {
    @Environment(\.dismiss) private var dismiss
    @State private var draft: APIClient.SavedPlace
    /// It has coordinates: a saved place always does, a new one once a result or the current location is picked.
    @State private var located: Bool
    @State private var query = ""
    @State private var results: [LocationTool.Found] = []
    @State private var searching = false
    @State private var locating = false
    /// Why the search or the current location gave nothing.
    @State private var findError: String?
    @State private var saving = false
    @State private var confirmDelete = false
    /// Presented as the "new place" sheet: nothing is saved unless Add is tapped.
    private let creating: Bool
    private let opened: APIClient.SavedPlace
    var save: (APIClient.SavedPlace) async -> APIClient.SavedPlace?
    var delete: (APIClient.SavedPlace) async -> Void

    init(place: APIClient.SavedPlace, save: @escaping (APIClient.SavedPlace) async -> APIClient.SavedPlace?,
         delete: @escaping (APIClient.SavedPlace) async -> Void) {
        _draft = State(initialValue: place)
        _located = State(initialValue: !place.id.isEmpty)
        creating = place.id.isEmpty
        opened = place
        self.save = save
        self.delete = delete
    }

    private var canSave: Bool { SavedPlaceText.canSave(draft, located: located) && (creating || draft != opened) }

    var body: some View {
        List {
            Section {
                TextField("Name", text: $draft.name, prompt: Text("Home, Office…"))
                    .accessibilityIdentifier("saved-place-name")
            } footer: {
                Text("What you call it in a chat, as in “remind me when I get home”.")
            }
            Section {
                if located { whereItIs }
                TextField("Search for an Address or Place", text: $query)
                    .submitLabel(.search)
                    .onSubmit { Task { await search() } }
                    .accessibilityIdentifier("saved-place-search")
                if searching {
                    ProgressView().frame(maxWidth: .infinity)
                }
                ForEach(results, id: \.self) { f in
                    Button { pick(f) } label: {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(f.name ?? f.address ?? SavedPlaceText.coordinates(APIClient.SavedPlace(latitude: f.latitude, longitude: f.longitude)))
                                .font(Theme.Font.body).lineLimit(1)
                            if let address = f.address, address != f.name {
                                Text(address).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
                            }
                        }
                        .contentShape(.rect)
                    }
                    .foregroundStyle(.primary)
                }
                Button { Task { await useCurrentLocation() } } label: {
                    if locating {
                        ProgressView()
                    } else {
                        Label("Use Current Location", systemImage: Theme.Icon.currentLocation).lineLimit(1)
                    }
                }
                .disabled(locating)
                .accessibilityIdentifier("saved-place-current")
            } header: {
                Text("Location")
            } footer: {
                if let findError { Text(findError) }
            }
            Section {
                Picker("Reminder Radius", selection: $draft.radiusMeters) {
                    ForEach(SavedPlaceText.radii(with: draft.radiusMeters), id: \.self) { r in
                        Text(SavedPlaceText.radius(r)).tag(r)
                    }
                }
                .lineLimit(1)
            } footer: {
                Text("A reminder goes off when you cross this circle. Indoors your phone's location is often 50–100 m off, so a smaller circle can miss.")
            }
            if !creating {
                Section {
                    Button(role: .destructive) { confirmDelete = true } label: {
                        Label("Delete Place", systemImage: Theme.Icon.delete).lineLimit(1)
                    }
                }
            }
        }
        .themedPage()
        .navigationTitle(creating ? String(localized: "New Place") : (draft.name.isEmpty ? String(localized: "Saved Places") : draft.name))
        .navigationBarTitleDisplayMode(.inline)
        .sheetCancel(unsaved: draft != opened, when: creating) { dismiss() }
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button { Task { await persist() } } label: {
                    if saving { ProgressView() } else { Text(creating ? String(localized: "Add [Beautiful U I Bridge 7a8a11]") : String(localized: "Save")).lineLimit(1) }
                }
                .disabled(!canSave || saving)
                .accessibilityIdentifier("saved-place-save")
            }
        }
        .interactiveDismissDisabled(saving)
        .confirmationDialog("Delete “\(opened.name)”?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                Task { await delete(opened) }
                dismiss()
            }
        } message: {
            Text("Reminders already set for this place keep going off where they are.")
        }
    }

    /// Where the place is: the map with the reminder's circle, and its address.
    @ViewBuilder private var whereItIs: some View {
        let center = CLLocationCoordinate2D(latitude: draft.latitude, longitude: draft.longitude)
        let span = CLLocationDistance(draft.radiusMeters) * 4
        Map(initialPosition: .region(MKCoordinateRegion(center: center, latitudinalMeters: span, longitudinalMeters: span)),
            interactionModes: []) {
            MapCircle(center: center, radius: CLLocationDistance(draft.radiusMeters))
                .foregroundStyle(Theme.accent.opacity(0.18))
                .stroke(Theme.accent, lineWidth: Theme.Size.lineMedium)
            Marker(draft.name, systemImage: Theme.Icon.place, coordinate: center)
                .tint(Theme.accent)
        }
        .id("\(draft.latitude),\(draft.longitude),\(draft.radiusMeters)") // a new pick or radius recenters it
        .scaledFrame(height: Theme.Size.placeMap)
        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .listRowInsets(EdgeInsets(top: Theme.Space.s, leading: Theme.Space.s, bottom: Theme.Space.s, trailing: Theme.Space.s))
        .accessibilityLabel(Text(SavedPlaceText.detail(draft)))
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if !draft.address.isEmpty { Text(draft.address).font(Theme.Font.body).lineLimit(2) }
            Text(SavedPlaceText.coordinates(draft)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    private func search() async {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return }
        searching = true
        defer { searching = false }
        findError = nil
        do {
            results = try await LocationTool.find(q, limit: 6)
        } catch {
            results = []
        }
        if results.isEmpty { findError = String(localized: "No places found for “\(q)”.") }
    }

    private func pick(_ f: LocationTool.Found) {
        Haptics.tap()
        draft.latitude = f.latitude
        draft.longitude = f.longitude
        draft.address = f.address ?? f.name ?? ""
        if draft.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, let name = f.name { draft.name = String(name.prefix(40)) }
        located = true
        results = []
        query = ""
        findError = nil
    }

    private func useCurrentLocation() async {
        locating = true
        defer { locating = false }
        findError = nil
        switch await LocationAccess.request() {
        case .granted, .limited, .notNeeded: break
        case .denied, .notDetermined:
            findError = String(localized: "Allow Persona to use your location in Settings › Privacy & Security › Location Services, or search for the address.")
            return
        }
        do {
            let location = try await OneShotLocation.fetch(timeout: 15)
            draft.latitude = location.coordinate.latitude
            draft.longitude = location.coordinate.longitude
            draft.address = await LocationTool.address(of: location) ?? ""
            located = true
            results = []
        } catch {
            findError = String(localized: "Couldn't get your location. Try again outdoors, or search for the address.")
        }
    }

    private func persist() async {
        saving = true
        defer { saving = false }
        if await save(draft) != nil {
            Haptics.success()
            dismiss()
        }
    }
}
