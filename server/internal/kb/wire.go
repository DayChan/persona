package kb

import "github.com/nwverse/persona/server/internal/kbwire"

// ItemFromWire is an item a connector handed over (kbwire.Item) as the store keeps it.
func ItemFromWire(w kbwire.Item) Item {
	return Item{Kind: w.Kind, Source: w.Source, ID: w.ID, Title: w.Title, URL: w.URL, At: w.At, Where: w.Where, Projects: w.Projects,
		Note: w.Note, Seen: w.Seen, Signals: kbwire.CanonicalSignals(w.Signals), Tagging: Tagging{Importance: w.Importance, Relevance: w.Relevance, Tags: w.Tags,
			Locked: w.Locked, TaggedAt: w.TaggedAt, Pinned: w.Pinned, LockedFields: w.LockedFields}} // provenance is the store's, never a connector's
}
