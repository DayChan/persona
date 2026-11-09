package assistantwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// KBHistory is manager.json's kb.history: the import of chats' older history into the knowledge base
// (docs/assistant/knowledge.md "History import"). Off unless Enabled; a source runs only when it is listed here, in
// the order listed, and only once the owner approved its plan in the assistant chat (kb_history_resume).
//
//	"history": {"enabled": true, "sources": {"<connector id>": {"profile": "careful", "needs_search_index": false}}}
type KBHistory struct {
	Enabled bool `json:"enabled,omitempty"`
	// Sources are the connectors to import from, in the order manager.json lists them (one runs at a time).
	Sources []KBHistorySource `json:"-"`
}

// KBHistorySource is one connector's entry in kb.history.sources: its pacing profile and overrides. Every range is
// [least, most], drawn afresh each time; a field left out keeps the profile's.
type KBHistorySource struct {
	Name string `json:"-"` // the connector id (the key in sources)
	// Profile: "standard" or "careful" ("" careful, the safer one).
	Profile string `json:"profile,omitempty"`
	// NeedsSearchIndex (default true): the source waits for the knowledge base's persistent search index, since its
	// history is large enough to make search too slow without one. false runs it before the index exists.
	NeedsSearchIndex *bool `json:"needs_search_index,omitempty"`
	// PageLimit is the page size asked for (a hint: a source may return more or fewer).
	PageLimit int `json:"page_limit,omitempty"`
	// SlowFactor (≥ 1; 0 or 1: none) multiplies every interval and rest, e.g. 2 after a stop the import caused.
	SlowFactor float64 `json:"slow_factor,omitempty"`

	IntervalSeconds      *KBRange `json:"interval_seconds,omitempty"`       // between two calls
	BatchCalls           *KBRange `json:"batch_calls,omitempty"`            // calls between two rests
	RestMinutes          *KBRange `json:"rest_minutes,omitempty"`           // a rest between batches
	LongRestEveryHours   *KBRange `json:"long_rest_every_hours,omitempty"`  // [0, 0]: no long rests
	LongRestMinutes      *KBRange `json:"long_rest_minutes,omitempty"`      // a long rest
	QuietPeriodsPerDay   *KBRange `json:"quiet_periods_per_day,omitempty"`  // [0, 0]: none
	QuietHours           *KBRange `json:"quiet_hours,omitempty"`            // the length of one quiet period
	SwitchChatPages      *KBRange `json:"switch_chat_pages,omitempty"`      // pages read in a chat before moving to another
	ImageIntervalSeconds *KBRange `json:"image_interval_seconds,omitempty"` // between two image downloads
}

// SearchIndexNeeded is NeedsSearchIndex with its default (true).
func (s KBHistorySource) SearchIndexNeeded() bool {
	return s.NeedsSearchIndex == nil || *s.NeedsSearchIndex
}

// Source is the entry named name, false when kb.history does not list it.
func (h *KBHistory) Source(name string) (KBHistorySource, bool) {
	if h == nil {
		return KBHistorySource{}, false
	}
	for _, s := range h.Sources {
		if s.Name == name {
			return s, true
		}
	}
	return KBHistorySource{}, false
}

// On: kb.history is there and enabled.
func (h *KBHistory) On() bool { return h != nil && h.Enabled }

// KBRange is [least, most] in JSON; a single number is both.
type KBRange [2]float64

func (r *KBRange) UnmarshalJSON(b []byte) error {
	var one float64
	if err := json.Unmarshal(b, &one); err == nil {
		if one < 0 {
			return fmt.Errorf("the range %v is negative", one)
		}
		*r = KBRange{one, one}
		return nil
	}
	var two []float64
	if err := json.Unmarshal(b, &two); err != nil || len(two) != 2 {
		return errors.New("a range is [least, most] or one number")
	}
	if two[0] < 0 || two[1] < two[0] {
		return fmt.Errorf("the range [%v, %v] is not 0 ≤ least ≤ most", two[0], two[1])
	}
	*r = KBRange{two[0], two[1]}
	return nil
}

func (r KBRange) MarshalJSON() ([]byte, error) { return json.Marshal([]float64{r[0], r[1]}) }

type kbHistoryJSON struct {
	Enabled bool            `json:"enabled,omitempty"`
	Sources json.RawMessage `json:"sources,omitempty"`
}

// UnmarshalJSON reads sources as an object and keeps the order of its keys: the order the sources run in.
func (h *KBHistory) UnmarshalJSON(b []byte) error {
	var raw kbHistoryJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	h.Enabled, h.Sources = raw.Enabled, nil
	if len(raw.Sources) == 0 || string(raw.Sources) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Sources))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("kb.history.sources is an object of connector id → settings")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := t.(string)
		var s KBHistorySource
		if err := dec.Decode(&s); err != nil {
			return fmt.Errorf("kb.history.sources.%s: %w", name, err)
		}
		if s.SlowFactor < 0 {
			return fmt.Errorf("kb.history.sources.%s: slow_factor %v is negative", name, s.SlowFactor)
		}
		if name == "" {
			continue
		}
		s.Name = name
		replaced := false
		for i := range h.Sources {
			if h.Sources[i].Name == name { // a key written twice: the last one, as encoding/json does
				h.Sources[i], replaced = s, true
			}
		}
		if !replaced {
			h.Sources = append(h.Sources, s)
		}
	}
	return nil
}

// MarshalJSON writes sources as an object in their order.
func (h KBHistory) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	if h.Enabled {
		b.WriteString(`"enabled":true`)
	}
	if len(h.Sources) > 0 {
		if h.Enabled {
			b.WriteString(",")
		}
		b.WriteString(`"sources":{`)
		for i, s := range h.Sources {
			if i > 0 {
				b.WriteString(",")
			}
			k, _ := json.Marshal(s.Name)
			v, err := json.Marshal(s)
			if err != nil {
				return nil, err
			}
			b.Write(k)
			b.WriteString(":")
			b.Write(v)
		}
		b.WriteString("}")
	}
	b.WriteString("}")
	return b.Bytes(), nil
}
