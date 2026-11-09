package nodecmd

import (
	"strings"
	"testing"
)

func TestCheckNodeStdioArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"--stdio"}} {
		if err := checkNodeStdioArgs(args); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
	for _, args := range [][]string{{"doctr"}, {"--atach"}, {"--stdio", "x"}, {"-stdio"}} {
		err := checkNodeStdioArgs(args)
		if err == nil || !strings.Contains(err.Error(), "不认识的 node 子命令") || !strings.Contains(err.Error(), "doctor") {
			t.Errorf("%q: %v", args, err)
		}
	}
}

func TestResidentArgs(t *testing.T) {
	for _, c := range []struct {
		args       []string
		supervised bool
	}{{nil, false}, {[]string{"--supervised"}, true}} {
		got, err := residentArgs(c.args)
		if err != nil || got != c.supervised {
			t.Errorf("%q: %v %v", c.args, got, err)
		}
	}
	// A mistyped or repeated flag is refused: ignored, it would leave the node unable to take updates without a word.
	for _, args := range [][]string{{"--supervise"}, {"supervised"}, {"--supervised", "--supervised"}, {"--supervised", "x"}, {"--stdio"}} {
		if got, err := residentArgs(args); err == nil || got || !strings.Contains(err.Error(), "--supervised") {
			t.Errorf("%q: %v %v", args, got, err)
		}
	}
}
