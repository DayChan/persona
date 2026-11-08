package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// kbDecodeParams is strict so a misspelled update cannot silently succeed without applying the intended change.
func kbDecodeParams(method string, raw json.RawMessage, out any) error {
	if len(raw) > 1<<20 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return kbInvalidParams(method, "expected an object of at most 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return kbInvalidParams(method, err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return kbInvalidParams(method, "expected one object")
	}
	return nil
}

func kbInvalidParams(method, message string) error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + message}
}

func kbRPCKind(kind string) bool {
	switch kind {
	case kb.KindProject, kb.KindChat, kb.KindPerson, kb.KindDoc, kb.KindMeeting, kb.KindEvent, kb.KindRecording:
		return true
	}
	return false
}

func kbRPCError(err error) error {
	var conflict *kb.ConflictError
	if errors.As(err, &conflict) {
		data, _ := json.Marshal(assistantwire.KBConflict{Owners: conflict.Owners})
		return &rpc.Error{Code: assistantwire.CodeKBConflict, Message: "kb.update: " + conflict.Error(), Data: data}
	}
	if errors.Is(err, kb.ErrNotFound) {
		return &rpc.Error{Code: "not_found", Message: err.Error()}
	}
	return kbError(err)
}

// kbRPCRequest shares the engine with the apps and MCP tools, without invoking an HTTP handler or exposing agents.
func (s *Service) kbRPCRequest(ctx context.Context, method string, raw json.RawMessage) (any, bool, error) {
	var list assistantwire.KBListParams
	var get assistantwire.KBGetParams
	var day assistantwire.KBDayParams
	var search assistantwire.KBSearchParams
	var update assistantwire.KBUpdateParams
	var recent assistantwire.KBRecentParams
	var since, until time.Time
	var params any
	extension := ""
	if method == assistantwire.MethodKBExtensionUpdate {
		var env assistantwire.KBExtensionUpdate
		if err := kbDecodeParams(method, raw, &env); err != nil {
			return nil, true, err
		}
		if strings.TrimSpace(env.Extension) == "" {
			return nil, true, kbInvalidParams(method, "extension is required")
		}
		method, raw, extension = assistantwire.MethodKBUpdate, env.Params, env.StorageKey()
	}
	switch method {
	case assistantwire.MethodKBList:
		params = &list
	case assistantwire.MethodKBGet:
		params = &get
	case assistantwire.MethodKBDay:
		params = &day
	case assistantwire.MethodKBSearch:
		params = &search
	case assistantwire.MethodKBUpdate:
		if s.kbReadOnly(ctx) {
			return nil, true, kbError(kb.ErrReadOnly)
		}
		params = &update
	case assistantwire.MethodKBRecent:
		params = &recent
	default:
		return nil, false, nil
	}
	if err := kbDecodeParams(method, raw, params); err != nil {
		return nil, true, err
	}
	invalid := func(message string) (any, bool, error) { return nil, true, kbInvalidParams(method, message) }
	switch method {
	case assistantwire.MethodKBList:
		if !kbRPCKind(list.Kind) || list.Limit < 0 || list.Limit > 200 || list.Offset < 0 {
			return invalid("kind must name an entity kind; limit must be between 0 and 200; offset must be nonnegative")
		}
		if list.Limit == 0 {
			list.Limit = 30
		}
		if list.Since != "" {
			if _, err := time.Parse(time.RFC3339, list.Since); err != nil {
				return invalid("since must be an RFC 3339 time")
			}
		}
	case assistantwire.MethodKBRecent:
		var err error
		if since, err = time.Parse(time.RFC3339, recent.Since); err != nil {
			return invalid("since must be an RFC 3339 time")
		}
		if recent.Until != "" {
			if until, err = time.Parse(time.RFC3339, recent.Until); err != nil {
				return invalid("until must be an RFC 3339 time")
			}
		}
		if len(recent.Kinds) == 0 {
			recent.Kinds = []string{kb.KindChat, kb.KindDoc, kb.KindMeeting, kb.KindEvent, kb.KindRecording}
		}
		for _, k := range recent.Kinds {
			if !kbRPCKind(k) {
				return invalid("kinds must name entity kinds")
			}
		}
		if recent.Limit < 0 || recent.Limit > 200 || recent.Offset < 0 {
			return invalid("limit must be between 0 and 200; offset must be nonnegative")
		}
		if recent.Limit == 0 {
			recent.Limit = 50
		}
	case assistantwire.MethodKBGet:
		if !kbRPCKind(get.Kind) || strings.TrimSpace(get.ID) == "" {
			return invalid("kind and id are required")
		}
		if get.RecentChars == nil {
			n := 12000
			get.RecentChars = &n
		}
		if *get.RecentChars < 0 || *get.RecentChars > 20000 {
			return invalid("recent_chars must be between 0 and 20000")
		}
	case assistantwire.MethodKBDay:
		if strings.TrimSpace(day.ID) == "" {
			return invalid("id is required")
		}
		if _, err := time.Parse("2006-01-02", day.Day); err != nil {
			return invalid("day must be YYYY-MM-DD")
		}
	case assistantwire.MethodKBSearch:
		if strings.TrimSpace(search.Query) == "" || search.Limit < 0 || search.Limit > 200 {
			return invalid("query is required; limit must be between 0 and 200")
		}
		if search.Limit == 0 {
			search.Limit = 40
		}
	case assistantwire.MethodKBUpdate:
		if !kbRPCKind(update.Kind) || strings.TrimSpace(update.ID) == "" {
			return invalid("kind and id are required")
		}
		if extension == "" && (len(update.Evidence) > 0 || len(update.Unlock) > 0) {
			return invalid("evidence and unlock are for extensions' writes")
		}
		if msg := kbCheckEvidence(update.Evidence); msg != "" {
			return invalid(msg)
		}
		for _, f := range update.Unlock {
			if !kb.LockableField(f) {
				return invalid("unlock: unknown field " + f)
			}
		}
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, true, kbRPCError(err)
	}
	switch method {
	case assistantwire.MethodKBList:
		all := e.List(list.Kind, "")
		items := all
		if list.Tag != "" {
			items = e.List(list.Kind, list.Tag)
		}
		if list.Since != "" {
			since, _ := time.Parse(time.RFC3339, list.Since)
			items = slices.DeleteFunc(slices.Clone(items), func(en kb.Entry) bool { return en.Updated.Before(since) })
		}
		if items == nil {
			items = []kb.Entry{}
		}
		total := len(items)
		start := min(list.Offset, total)
		end := start + min(total-start, list.Limit)
		items = items[start:end]
		var next *int
		if end < total {
			next = &end
		}
		tags := kb.KindTags(all, 30)
		if tags == nil {
			tags = []string{}
		}
		return assistantwire.KBListResult{Items: items, Tags: tags, Total: total, NextOffset: next}, true, nil
	case assistantwire.MethodKBRecent:
		items := []kb.Entry{}
		for _, k := range slices.Compact(slices.Sorted(slices.Values(recent.Kinds))) {
			for _, en := range e.List(k, "") {
				if !en.Updated.Before(since) && (until.IsZero() || en.Updated.Before(until)) {
					items = append(items, en)
				}
			}
		}
		slices.SortStableFunc(items, func(a, b kb.Entry) int { return b.Updated.Compare(a.Updated) })
		total := len(items)
		start := min(recent.Offset, total)
		end := start + min(total-start, recent.Limit)
		var next *int
		if end < total {
			next = &end
		}
		return assistantwire.KBRecentResult{Items: items[start:end], Total: total, NextOffset: next}, true, nil
	case assistantwire.MethodKBGet:
		detail, err := e.Get(get.Kind, get.ID, *get.RecentChars)
		return assistantwire.KBGetResult{Detail: detail}, true, kbRPCError(err)
	case assistantwire.MethodKBDay:
		messages, prev, next, err := e.Day(day.ID, day.Day)
		if messages == nil {
			messages = []kb.DayMessage{}
		}
		return assistantwire.KBDayResult{Day: day.Day, Messages: messages, Prev: prev, Next: next}, true, kbRPCError(err)
	case assistantwire.MethodKBSearch:
		hits := e.Search(search.Query, search.Messages == nil || *search.Messages, search.Limit)
		if hits == nil {
			hits = []kb.Hit{}
		}
		return assistantwire.KBSearchResult{Hits: hits}, true, nil
	default:
		if extension != "" {
			res, err := e.ApplyExtension(update.Kind, update.ID, update.Update,
				kb.ExtensionWrite{Extension: extension, Evidence: update.Evidence, Unlock: update.Unlock})
			if err != nil {
				return nil, true, kbRPCError(err)
			}
			return assistantwire.KBUpdateResult{ID: res.ID, Before: res.Before}, true, nil
		}
		id, err := e.Apply(update.Kind, update.ID, update.Update)
		return assistantwire.KBUpdateResult{ID: id}, true, kbRPCError(err)
	}
}

// kbCheckEvidence is why an update's evidence is invalid; "" when it is fine.
func kbCheckEvidence(list []kb.Evidence) string {
	if len(list) > 20 {
		return "at most 20 evidence entries"
	}
	for _, ev := range list {
		if strings.TrimSpace(ev.Chat) == "" || ev.N < 0 || utf8.RuneCountInString(ev.Note) > 200 {
			return "evidence needs a chat, a nonnegative n and a note of at most 200 characters"
		}
		if _, err := time.Parse("2006-01-02", ev.Day); err != nil {
			return "evidence day must be YYYY-MM-DD"
		}
	}
	return ""
}
