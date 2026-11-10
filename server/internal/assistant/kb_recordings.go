package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/kb"
)

// The phone's recording sync (docs/protocol/protocol.md "Recording sync", kb/recordings.go): transcripts into the knowledge base,
// keyed by the recording's UUID. The hub relays these like the other knowledge-base routes.

// codeRecordingDeleted: the recording was deleted here; the phone drops it instead of uploading it again.
const codeRecordingDeleted = "recording_deleted"

func (s *Service) recordingRoutes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	s.handle("PUT /api/v1/recordings/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		var u kb.RecordingUpload
		// The transcript is at most 4 MB of text; JSON escaping and the segments' numbers on top.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&u); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		status, err := e.PutRecording(r.PathValue("id"), u, time.Now())
		switch {
		case errors.Is(err, kb.ErrRecordingDeleted):
			apierr.WriteCode(w, http.StatusGone, codeRecordingDeleted, err)
			return
		case errors.Is(err, kb.ErrBadRecording):
			apierr.Write(w, http.StatusBadRequest, err)
			return
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		id, _ := kb.RecordingID(r.PathValue("id"))
		if status == kb.RecordingStored && u.Complete {
			s.kbWake() // its summary and minutes now, not at the next round
		}
		apierr.WriteJSON(w, map[string]any{"id": id, "revision": u.Revision, "status": status})
	})
	s.handle("DELETE /api/v1/recordings/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		deleted, err := e.DeleteRecording(r.PathValue("id"), time.Now())
		switch {
		case errors.Is(err, kb.ErrBadRecording):
			apierr.Write(w, http.StatusBadRequest, err)
			return
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"deleted": deleted})
	})
	s.handle("GET /api/v1/recordings", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		kept, deleted := e.RecordingStates()
		apierr.WriteJSON(w, map[string]any{"days": e.RecordingDays(), "recordings": kept, "deleted": deleted})
	})
	s.minutesRoutes(engine)
}
