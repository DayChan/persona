package proto

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
	"sync"
)

// A machine's session list travels whole on every change: the node sends it to its manager (sessions.changed), the
// manager sends it on to the hub (fleet.sessions), and a busy machine lists 150 sessions of about 1 KB of which one
// changed. Decoding all of them again took about 1.3 ms and 900 KB each time, on each hop. ScanSessionList reads the
// list's elements as bytes, and a SessionListDecoder decodes only those whose bytes it has not seen in the last list:
// the others are the sessions it decoded then, copied.

// SessionListDecoder decodes the successive session lists of one machine. Decoding is a function of the bytes, so a
// session whose bytes were in the last list is that list's session again: each is kept as decoded, apart from what the
// caller was handed, and handed out as a copy (a struct copy: its slices and pointers are shared, and never changed in
// place by the callers, as for any kept list). The zero value is ready; safe for concurrent use.
type SessionListDecoder struct {
	mu   sync.Mutex
	gen  uint64
	memo map[string]*listedSession // the element's bytes → their session, for the elements of the last list
}

// listedSession is one element of the last list: its bytes (the memo's key) and their decoding.
type listedSession struct {
	raw string
	s   Session
	gen uint64 // the last Decode that listed it
}

// Decode decodes rows, the elements of a "sessions" array as ScanSessionList read them, into the list json.Unmarshal
// gives for that array (an empty list for no rows), with the same options and the same failures: an element that does
// not decode fails the whole list. raws[i] are the bytes list[i] was decoded from, the same string for the same
// bytes as long as they stay in the lists, so a caller can keep what it makes of a session and tell by them that the
// session is the same.
func (d *SessionListDecoder) Decode(rows []jsontext.Value) (list []Session, raws []string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.memo == nil {
		d.memo = make(map[string]*listedSession, len(rows))
	}
	d.gen++
	list, raws = make([]Session, len(rows)), make([]string, len(rows))
	for i, row := range rows {
		if e := d.memo[string(row)]; e != nil {
			list[i], raws[i], e.gen = e.s, e.raw, d.gen
			continue
		}
		// As the list's own decoder decodes each element: Session.UnmarshalJSONFrom with encoding/json's options.
		if err = jsonv2.Unmarshal(row, &list[i], json.DefaultOptionsV1()); err != nil {
			list, raws = nil, nil
			break
		}
		e := &listedSession{raw: string(row), s: list[i], gen: d.gen}
		d.memo[e.raw], raws[i] = e, e.raw
	}
	for k, e := range d.memo {
		if e.gen != d.gen {
			delete(d.memo, k) // not in this list: only the last list's sessions are kept, a failed one's too
		}
	}
	return list, raws, err
}

// DecodeChangedSessions turns ScanSessionList's and SessionListDecoder's use on (the hub's fleet.sessions, the
// manager's sessions.changed): false decodes and encodes every list whole, as before them.
const DecodeChangedSessions = true

// maxListMembers bounds the members ScanSessionList reads besides the list: events carry two or three.
const maxListMembers = 16

// emptyObject is the rest of an object that has only its list.
var emptyObject = []byte("{}")

// ScanSessionList reads data, an event's params, for the elements of its "sessions" array, each a slice of data, and
// the object's other members, in order, as an object of their own (rest). ok is false unless decoding rest with
// json.Unmarshal and the elements as a []Session gives what json.Unmarshal gives for data: data is one valid object;
// "sessions", written without escapes, is one of its members and an array; no member name is given twice, written
// with escapes, or read as "sessions" by json.Unmarshal's case-insensitive matching; and it has at most
// maxListMembers other members. When ok is false, decode data with json.Unmarshal: it then decodes it as it always
// has, a repeated or differently cased "sessions" included.
func ScanSessionList(data []byte) (rest []byte, rows []jsontext.Value, ok bool) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(data), json.DefaultOptionsV1()) // parses data in place
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, nil, false
	}
	var names [maxListMembers + 1][]byte
	n, listed := 0, false
	for dec.PeekKind() != '}' {
		name, err := readRaw(dec, data)
		if err != nil || n == len(names) || bytes.IndexByte(name, '\\') >= 0 {
			return nil, nil, false
		}
		bare := name[1 : len(name)-1]
		for _, seen := range names[:n] {
			if bytes.Equal(seen[1:len(seen)-1], bare) {
				return nil, nil, false
			}
		}
		names[n] = name
		n++
		if string(bare) == "sessions" {
			if rows, ok = scanRows(dec, data); !ok {
				return nil, nil, false
			}
			listed = true
			continue
		}
		if foldsToSessions(bare) {
			return nil, nil, false
		}
		val, err := readRaw(dec, data)
		if err != nil {
			return nil, nil, false
		}
		if rest == nil {
			rest = append(make([]byte, 0, len(name)+len(val)+16), '{')
		} else {
			rest = append(rest, ',')
		}
		rest = append(append(append(rest, name...), ':'), val...)
	}
	if _, err := dec.ReadToken(); err != nil { // the object's end
		return nil, nil, false
	}
	if _, err := dec.ReadToken(); err != io.EOF || !listed { // nothing after it but space
		return nil, nil, false
	}
	if rest == nil {
		return emptyObject, rows, true
	}
	return append(rest, '}'), rows, true
}

// scanRows reads the "sessions" member's value: an array, its elements as slices of data. A list is never empty for
// json.Unmarshal (an empty array is an empty list), so neither are rows.
func scanRows(dec *jsontext.Decoder, data []byte) ([]jsontext.Value, bool) {
	if dec.PeekKind() != '[' {
		return nil, false // null or another type: json.Unmarshal says what that is
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, false
	}
	rows := make([]jsontext.Value, 0, min(len(data)/512, 4096)+8) // sessions are rarely under 512 bytes
	for dec.PeekKind() != ']' {
		v, err := readRaw(dec, data)
		if err != nil {
			return nil, false
		}
		rows = append(rows, v)
	}
	_, err := dec.ReadToken()
	return rows, err == nil
}

// readRaw reads the next value (or member name) as the slice of data it was read from.
func readRaw(dec *jsontext.Decoder, data []byte) (jsontext.Value, error) {
	v, err := dec.ReadValue()
	if err != nil {
		return nil, err
	}
	end := int(dec.InputOffset())
	return jsontext.Value(data[end-len(v) : end : end]), nil
}

// foldsToSessions: json.Unmarshal could read a member of this name (other than "sessions" itself) into a field named
// "sessions": the same letters in another case, Unicode folding included, with or without dashes and underscores.
func foldsToSessions(name []byte) bool {
	var buf [64]byte
	kept := buf[:0]
	for _, c := range name {
		if c != '-' && c != '_' {
			if len(kept) == len(buf) {
				return false // far longer than "sessions" in any folding
			}
			kept = append(kept, c)
		}
	}
	return bytes.EqualFold(kept, []byte("sessions"))
}

// AppendSession appends to b the bytes json.Marshal gives for s (appendSession): what the hub writes into a list's one
// buffer itself. False when s holds a value it leaves to encoding/json (a time outside the years 0-9999, a nested
// value that does not encode, Session's fields changed since session_wire.go was written): encode s with json then.
func AppendSession(b []byte, s *Session) ([]byte, bool) { return appendSession(b, s) }
