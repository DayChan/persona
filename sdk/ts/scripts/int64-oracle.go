// Differential oracle for the official TypeScript SDK's Go lexical int64 contract.
// Run with a pinned Go toolchain and src/testdata/int64-cases.json; no network or hub.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type testCase struct {
	Name    string `json:"name"`
	Raw     string `json:"raw"`
	Error   bool   `json:"error"`
	Topic   string `json:"topic"`
	Replies string `json:"replies"`
}

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		panic("expected the int64 cases path and optional notification cases path")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var cases []testCase
	if err := json.Unmarshal(data, &cases); err != nil {
		panic(err)
	}
	cases = append(cases, testCase{Name: "deep unknown field", Raw: `{"unknown":` + strings.Repeat("[", 9000) + "0" + strings.Repeat("]", 9000) + `,"topic":6}`, Topic: "6", Replies: "0"})
	for _, c := range cases {
		var args struct {
			Topic   int64 `json:"topic"`
			Replies int64 `json:"replies"`
		}
		err := json.Unmarshal([]byte(c.Raw), &args)
		actual := map[string]any{"name": c.Name, "error": err != nil}
		if err == nil {
			actual["topic"] = strconv.FormatInt(args.Topic, 10)
			actual["replies"] = strconv.FormatInt(args.Replies, 10)
		}
		encoded, _ := json.Marshal(actual)
		fmt.Println(string(encoded))
		if (err != nil) != c.Error || err == nil && (actual["topic"] != c.Topic || actual["replies"] != c.Replies) {
			fmt.Fprintln(os.Stderr, "FAIL: int64 contract mismatch", c.Name)
			os.Exit(1)
		}
	}
	notificationCount := 0
	if len(os.Args) == 3 {
		data, err := os.ReadFile(os.Args[2])
		if err != nil {
			panic(err)
		}
		var notifications []struct {
			Name  string `json:"name"`
			Raw   string `json:"raw"`
			Error bool   `json:"error"`
			Count string `json:"count"`
			Frame string `json:"frame"`
		}
		if err := json.Unmarshal(data, &notifications); err != nil {
			panic(err)
		}
		for _, c := range notifications {
			var params struct {
				Count int64 `json:"count"`
				Frame int64 `json:"frame"`
			}
			err := json.Unmarshal([]byte(c.Raw), &params)
			if (err != nil) != c.Error || err == nil && (strconv.FormatInt(params.Count, 10) != c.Count || strconv.FormatInt(params.Frame, 10) != c.Frame) {
				fmt.Fprintln(os.Stderr, "FAIL: notification int64 contract mismatch", c.Name)
				os.Exit(1)
			}
		}
		notificationCount = len(notifications)
	}
	frameCases := []struct{ Raw, Want string }{
		{`{"params":{"n":1.0},"PARAMS":{"n":6}}`, `{"n":6}`},
		{`{"params":{"n":6},"Params":null}`, `null`},
		{`{"params":null,"params":{"n":6}}`, `{"n":6}`},
		{`{"Params":{"n":9223372036854775807}}`, `{"n":9223372036854775807}`},
	}
	for index, c := range frameCases {
		var frame struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(c.Raw), &frame); err != nil || string(frame.Params) != c.Want {
			fmt.Fprintln(os.Stderr, "FAIL: params RawMessage mismatch", index)
			os.Exit(1)
		}
		encoded, _ := json.Marshal(map[string]any{"frame_case": index, "params": string(frame.Params)})
		fmt.Println(string(encoded))
	}
	fmt.Fprintf(os.Stderr, "PASS: %d Go lexical int64 cases, %d notification cases and %d params RawMessage cases\n", len(cases), notificationCount, len(frameCases))
}
