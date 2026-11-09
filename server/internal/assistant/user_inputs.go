package assistant

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
)

// userInputRoutes exposes only the temporary full-history count/positions. #2071's richer navigation contract
// remains a separate task; no preview generation, target-window API, state table or model call is added here.
func (s *Service) userInputRoutes() {
	s.handle("GET /api/v1/sessions/{mid}/{sid}/user-inputs", func(w http.ResponseWriter, r *http.Request) {
		m, ok := s.fleetMachine(r.PathValue("mid"))
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("unknown machine"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var index proto.UserInputIndex
		if err := s.nodeCall(ctx, m.ID, "session.user-inputs", map[string]any{"session_id": r.PathValue("sid")}, &index); err != nil {
			apierr.Call(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		apierr.WriteJSON(w, index)
	})
}
