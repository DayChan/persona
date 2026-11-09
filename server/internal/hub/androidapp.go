package hub

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

func (h *Hub) androidAppRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/app/android/latest", h.androidAppLatest)
	api.HandleFunc("GET /api/v1/app/android/apk", h.androidAppAPK)
}

func (h *Hub) androidAppManifest(w http.ResponseWriter) ([]byte, string, bool) {
	dir := filepath.Join(h.cfg.dir, "apps", "android")
	b, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		androidAppError(w, err)
		return nil, "", false
	}
	return b, dir, true
}

func (h *Hub) androidAppLatest(w http.ResponseWriter, r *http.Request) {
	b, _, ok := h.androidAppManifest(w)
	if !ok {
		return
	}
	var release map[string]json.RawMessage
	if err := json.Unmarshal(b, &release); err != nil {
		writeErr(w, http.StatusInternalServerError, i18n.Err("hub-androidapp.bad_manifest"))
		return
	}
	var checksum string
	if err := json.Unmarshal(release["sha256"], &checksum); err != nil || !validAPKDigest(checksum) {
		writeErr(w, http.StatusInternalServerError, i18n.Err("hub-androidapp.bad_manifest"))
		return
	}
	// Existing clients already honor download_url, including its query string.
	release["download_url"] = json.RawMessage(strconv.Quote("/api/v1/app/android/apk?sha256=" + strings.ToLower(checksum)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(release); err != nil {
		h.logger.Printf("android update metadata response: %v", err)
		return
	}
}

func (h *Hub) androidAppAPK(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(h.cfg.dir, "apps", "android")
	checksum := r.URL.Query().Get("sha256")
	if checksum != "" {
		if !validAPKDigest(checksum) {
			writeErr(w, http.StatusBadRequest, i18n.Err("hub-androidapp.bad_manifest"))
			return
		}
		h.serveAndroidAPK(w, r, dir, strings.ToLower(checksum))
		return
	}
	b, dir, ok := h.androidAppManifest(w)
	if !ok {
		return
	}
	// Content-addressed files let publication replace only the manifest atomically.
	var release struct {
		SHA256 string `json:"sha256"`
	}
	err := json.Unmarshal(b, &release)
	digest, decodeErr := hex.DecodeString(release.SHA256)
	if err != nil || decodeErr != nil || len(digest) != 32 {
		writeErr(w, http.StatusInternalServerError, i18n.Err("hub-androidapp.bad_manifest"))
		return
	}
	h.serveAndroidAPK(w, r, dir, release.SHA256)
}

func validAPKDigest(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == 32
}

func (h *Hub) serveAndroidAPK(w http.ResponseWriter, r *http.Request, dir, checksum string) {
	f, err := os.Open(filepath.Join(dir, checksum+".apk"))
	if err != nil {
		androidAppError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, http.StatusInternalServerError, i18n.Err("hub-androidapp.unreadable_apk"))
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="persona.apk"`)
	w.Header().Set("ETag", `"`+checksum+`"`)
	http.ServeContent(w, r, "persona.apk", info.ModTime(), f)
}

func androidAppError(w http.ResponseWriter, err error) {
	if os.IsNotExist(err) {
		writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-androidapp.not_published"))
		return
	}
	writeErr(w, http.StatusInternalServerError, i18n.Err("hub-androidapp.unreadable_release"))
}
