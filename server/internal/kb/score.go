package kb

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// modelScore is a 0–100 relevance score as a model writes it in its JSON. Models sometimes quote it ("80") or write a
// fraction (80.5): both read as that number, rounded. Anything else (a word, a value outside 0–100, an object) reads
// as -1, which applyTags ignores, so the chat, project, recording or item keeps the relevance it had. Before #1099
// such a field failed the whole answer: a chat's summary, a project's rewrite, a recording's title and summary, or a
// batch of up to 40 tagged items.
// null and an absent field leave it at 0, as the int field it replaces did.
type modelScore int

func (s *modelScore) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	text := string(data)
	if len(data) > 0 && data[0] == '"' {
		if json.Unmarshal(data, &text) != nil {
			*s = -1
			return nil
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		*s = -1
		return nil
	}
	r := math.Round(f)
	if !(r >= 0 && r <= 100) { // NaN and ±Inf fail this too
		*s = -1
		return nil
	}
	*s = modelScore(r)
	return nil
}
