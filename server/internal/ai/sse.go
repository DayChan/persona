package ai

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// SSEEvents calls event with the data of each server-sent event in body, in order, until event returns true or body
// ends. An event's data lines are joined with "\n" (the one space after "data:" dropped) and a blank line ends it; a
// data line that follows data already making a whole JSON object starts the next event, for relays that send one
// data line per event with no blank line between (such data could not take another line and still decode). Comments
// and other fields are skipped, and the last event needs no blank line after it. The error is reading body's. The
// Responses runtime (respond) and the provider probe both read their streams with it, so the probe passes exactly
// the streams the runtime can read.
func SSEEvents(body io.Reader, event func(data string) (stop bool)) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var lines []string
	flush := func() (stop bool) {
		if len(lines) == 0 {
			return false
		}
		data := strings.Join(lines, "\n")
		lines = lines[:0]
		return event(data)
	}
	for sc.Scan() { // ScanLines strips the \r of a CRLF line end too
		line := sc.Text()
		if line == "" { // a blank line ends the event
			if flush() {
				return nil
			}
			continue
		}
		value, ok := strings.CutPrefix(line, "data:")
		if !ok { // a comment or another field
			continue
		}
		if wholeObject(lines) && flush() {
			return nil
		}
		lines = append(lines, strings.TrimPrefix(value, " "))
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

// wholeObject says the data lines read so far already make a whole JSON object. Only data whose last line ends the
// object is decoded, so an event of many lines is not checked again at each one.
func wholeObject(lines []string) bool {
	if len(lines) == 0 || !strings.HasSuffix(strings.TrimSpace(lines[len(lines)-1]), "}") {
		return false
	}
	return json.Valid([]byte(strings.Join(lines, "\n")))
}
