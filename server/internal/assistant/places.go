package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Saved places (docs/apple/phone-tools.md "Saved places", #1598): the places the user names (home, the office) with their
// coordinates. The user sets them in Settings › Assistant › Saved Places; the assistant reads them (list_places) for
// reminders by place and adds one (save_place) only after the user confirmed it in a conversation. Reminders take their
// coordinates from here, not from the assistant's memory, so the user can see and correct them.

// placesKey is where the places are kept, in the order they were added.
const placesKey = "assistant.places"

// The limits of a place. The radius is the reminder's circle (phone_reminders_create's radius_meters, PlaceTrigger on
// iOS): 150 m by default, 100–2000 m, since fixes indoors are often 50–100 m off.
const (
	placeRadiusDefault = 150
	placeRadiusMin     = 100
	placeRadiusMax     = 2000
	placeNameMax       = 40
	placeAddressMax    = 200
	placesMax          = 50
)

// Place is one of the user's saved places.
type Place struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Address      string    `json:"address,omitempty"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	RadiusMeters int       `json:"radius_meters"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// placeInput is a place as the app and save_place send it: the coordinates must be there, the radius may not.
type placeInput struct {
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
	RadiusMeters int      `json:"radius_meters"`
}

// placeBook serializes the changes: each reads the list, changes it and writes it back.
type placeBook struct {
	mu sync.Mutex
}

var (
	errPlacesStore      = i18n.Err("assistant-places.err_store")
	errPlaceReadOnly    = i18n.Err("assistant-places.err_read_only")
	errPlaceNotFound    = i18n.Err("assistant-places.err_not_found")
	errPlaceCoordinates = i18n.Err("assistant-places.err_coordinates")
	errPlaceName        = i18n.Err("assistant-places.err_name", i18n.Args{"max": placeNameMax})
	errPlaceAddress     = i18n.Err("assistant-places.err_address", i18n.Args{"max": placeAddressMax})
	errPlacesFull       = i18n.Err("assistant-places.err_too_many", i18n.Args{"max": placesMax})
)

// placeNameTaken is the error for a name another place has.
func placeNameTaken(name string) error {
	return i18n.Err("assistant-places.err_name_taken", i18n.Args{"name": name})
}

// placeText: s has no control characters (a name or an address is shown on one line).
func placeText(s string) bool {
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// place checks in and makes it a place (no id or times yet): the name and address trimmed, the coordinates on Earth,
// the radius defaulted and clamped.
func (in placeInput) place() (Place, error) {
	name, address := strings.TrimSpace(in.Name), strings.TrimSpace(in.Address)
	if name == "" || utf8.RuneCountInString(name) > placeNameMax || !placeText(name) {
		return Place{}, errPlaceName
	}
	if utf8.RuneCountInString(address) > placeAddressMax || !placeText(address) {
		return Place{}, errPlaceAddress
	}
	if in.Latitude == nil || in.Longitude == nil {
		return Place{}, errPlaceCoordinates
	}
	lat, lon := *in.Latitude, *in.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return Place{}, errPlaceCoordinates
	}
	radius := in.RadiusMeters
	switch {
	case radius <= 0:
		radius = placeRadiusDefault
	case radius < placeRadiusMin:
		radius = placeRadiusMin
	case radius > placeRadiusMax:
		radius = placeRadiusMax
	}
	return Place{Name: name, Address: address, Latitude: lat, Longitude: lon, RadiusMeters: radius}, nil
}

// sameName: two place names are the same place's (case and surrounding space aside).
func sameName(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// places are the saved places, in the order they were added.
func (s *Service) places(ctx context.Context) ([]Place, error) {
	st := s.store()
	if st == nil {
		return nil, errPlacesStore
	}
	var places []Place
	if _, err := st.Setting(ctx, placesKey, &places); err != nil {
		return nil, err
	}
	return places, nil
}

// changePlaces runs change on the saved places and keeps what it returns; one change at a time.
func (s *Service) changePlaces(ctx context.Context, change func([]Place) ([]Place, error)) error {
	s.placeBook.mu.Lock()
	defer s.placeBook.mu.Unlock()
	places, err := s.places(ctx)
	if err != nil {
		return err
	}
	next, err := change(slices.Clone(places))
	if err != nil {
		return err
	}
	return s.store().PutSetting(ctx, placesKey, next)
}

// addPlace adds p; a place of the same name is an error.
func (s *Service) addPlace(ctx context.Context, p Place) (Place, error) {
	err := s.changePlaces(ctx, func(places []Place) ([]Place, error) {
		if slices.ContainsFunc(places, func(x Place) bool { return sameName(x.Name, p.Name) }) {
			return nil, placeNameTaken(p.Name)
		}
		if len(places) >= placesMax {
			return nil, errPlacesFull
		}
		now := time.Now().UTC()
		p.ID, p.CreatedAt, p.UpdatedAt = fmt.Sprintf("pl%d", now.UnixNano()), now, now
		return append(places, p), nil
	})
	return p, err
}

// updatePlace changes place id to p's name, address, coordinates and radius; a name another place has is an error.
func (s *Service) updatePlace(ctx context.Context, id string, p Place) (Place, error) {
	err := s.changePlaces(ctx, func(places []Place) ([]Place, error) {
		i := slices.IndexFunc(places, func(x Place) bool { return x.ID == id })
		if i < 0 {
			return nil, errPlaceNotFound
		}
		if slices.ContainsFunc(places, func(x Place) bool { return x.ID != id && sameName(x.Name, p.Name) }) {
			return nil, placeNameTaken(p.Name)
		}
		p.ID, p.CreatedAt, p.UpdatedAt = id, places[i].CreatedAt, time.Now().UTC()
		places[i] = p
		return places, nil
	})
	return p, err
}

// savePlace adds p, or changes the place of the same name (save_place): it says whether it added one.
func (s *Service) savePlace(ctx context.Context, p Place) (Place, bool, error) {
	added := false
	err := s.changePlaces(ctx, func(places []Place) ([]Place, error) {
		now := time.Now().UTC()
		if i := slices.IndexFunc(places, func(x Place) bool { return sameName(x.Name, p.Name) }); i >= 0 {
			p.ID, p.CreatedAt, p.UpdatedAt = places[i].ID, places[i].CreatedAt, now
			places[i] = p
			return places, nil
		}
		if len(places) >= placesMax {
			return nil, errPlacesFull
		}
		p.ID, p.CreatedAt, p.UpdatedAt, added = fmt.Sprintf("pl%d", now.UnixNano()), now, now, true
		return append(places, p), nil
	})
	return p, added, err
}

// deletePlace removes place id; one that is not there is not an error (deleting twice is fine).
func (s *Service) deletePlace(ctx context.Context, id string) error {
	return s.changePlaces(ctx, func(places []Place) ([]Place, error) {
		return slices.DeleteFunc(places, func(x Place) bool { return x.ID == id }), nil
	})
}

// writePlaceError answers a failed read or change of the places with its status.
func writePlaceError(w http.ResponseWriter, err error) {
	var msg *i18n.Message
	switch {
	case errors.Is(err, errPlacesStore):
		apierr.Write(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, errPlaceNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.As(err, &msg) && msg.Key == "assistant-places.err_name_taken":
		apierr.Write(w, http.StatusConflict, err)
	case errors.As(err, &msg):
		apierr.Write(w, http.StatusBadRequest, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

func (s *Service) placeRoutes() {
	s.handle("GET /api/v1/assistant/places", func(w http.ResponseWriter, r *http.Request) {
		places, err := s.places(r.Context())
		if err != nil {
			writePlaceError(w, err)
			return
		}
		if places == nil {
			places = []Place{}
		}
		apierr.WriteJSON(w, map[string]any{"places": places})
	})
	// readPlace is the place in the request's body; false when it answered an error.
	readPlace := func(w http.ResponseWriter, r *http.Request) (Place, bool) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errPlaceReadOnly)
			return Place{}, false
		}
		var in placeInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return Place{}, false
		}
		p, err := in.place()
		if err != nil {
			writePlaceError(w, err)
			return Place{}, false
		}
		return p, true
	}
	s.handle("POST /api/v1/assistant/places", func(w http.ResponseWriter, r *http.Request) {
		p, ok := readPlace(w, r)
		if !ok {
			return
		}
		p, err := s.addPlace(r.Context(), p)
		if err != nil {
			writePlaceError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"place": p})
	})
	s.handle("PUT /api/v1/assistant/places/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := readPlace(w, r)
		if !ok {
			return
		}
		p, err := s.updatePlace(r.Context(), r.PathValue("id"), p)
		if err != nil {
			writePlaceError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"place": p})
	})
	s.handle("DELETE /api/v1/assistant/places/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errPlaceReadOnly)
			return
		}
		if err := s.deletePlace(r.Context(), r.PathValue("id")); err != nil {
			writePlaceError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// toolListPlaces answers list_places: the saved places as JSON, or a line saying there are none.
func (s *Service) toolListPlaces(ctx context.Context) (string, error) {
	places, err := s.places(ctx)
	if err != nil {
		return "", err
	}
	if len(places) == 0 {
		return "No saved places yet. The user can add them in Settings › Assistant › Saved Places; " +
			"or find the place with the user and save it with save_place once they confirmed it.", nil
	}
	type listed struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		Address      string  `json:"address,omitempty"`
		Latitude     float64 `json:"latitude"`
		Longitude    float64 `json:"longitude"`
		RadiusMeters int     `json:"radius_meters"`
	}
	out := make([]listed, len(places))
	for i, p := range places {
		out[i] = listed{p.ID, p.Name, p.Address, p.Latitude, p.Longitude, p.RadiusMeters}
	}
	b, err := json.Marshal(map[string]any{"places": out})
	return string(b), err
}

// toolSavePlace answers save_place: it adds the place, or changes the saved place of the same name.
func (s *Service) toolSavePlace(ctx context.Context, args json.RawMessage) (string, error) {
	var in placeInput
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	p, err := in.place()
	if err != nil {
		return "", errors.New(placeErrorText(err))
	}
	p, added, err := s.savePlace(ctx, p)
	if err != nil {
		return "", errors.New(placeErrorText(err))
	}
	verb := "Changed"
	if added {
		verb = "Saved"
	}
	return fmt.Sprintf("%s the place %q (%.6f, %.6f, radius %d m). The user sees it in Settings › Assistant › Saved Places.",
		verb, p.Name, p.Latitude, p.Longitude, p.RadiusMeters), nil
}

// placeErrorText is err in English, for the model.
func placeErrorText(err error) string {
	var msg *i18n.Message
	if errors.As(err, &msg) {
		return msg.Text(i18n.En)
	}
	return err.Error()
}
