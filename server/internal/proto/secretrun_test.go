package proto

import (
	"strings"
	"testing"
)

// What a password card may show as a command: printable ASCII only, so what it shows is what runs.
func TestSecretRunCommandProblem(t *testing.T) {
	for cmd, want := range map[string]string{
		"sudo -S -k -p '' launchctl list":                 "",
		"printf '%s\\n' \"a b\" | tr a-z A-Z; echo $HOME": "",
		strings.Repeat("x", SecretRunMaxCommandLen):       "",
		"":    "it is empty",
		"   ": "it is empty",
		strings.Repeat("x", SecretRunMaxCommandLen+1): "longer than",
		"brew​ services start x":                      "character 5 is U+200B", // zero-width space: brew<ZWSP> is another file
		"true rm -rf /tmp/x":                          "character 5 is U+2028", // drawn as a line break, run as one line
		"ls /tmp":                                     "character 3 is U+00A0", // looks like a space, is part of the word
		"ecаo hi":                                     "character 3 is U+0430", // Cyrillic а in place of a
		"true\tfalse":                                 "character 5 is U+0009",
		"true\nfalse":                                 "character 5 is U+000A",
		"echo \x1b[2Khidden":                          "character 6 is U+001B",
		"echo \xff":                                   "character 6 is U+FFFD", // invalid UTF-8
	} {
		got := SecretRunCommandProblem(cmd)
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("SecretRunCommandProblem(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// What the card may show as the purpose: any language, but nothing invisible or that breaks the line.
func TestSecretRunPurposeShows(t *testing.T) {
	for text, want := range map[string]bool{
		"Install the launch daemon for the backup job": true,
		"安装备份任务的启动守护进程":                                true,
		"Backup at 2 am":      true, // a no-break space is visible as a space
		"Install​ the daemon": false,
		"Install $ rm -rf /":  false,
		"Install the daemon":  false,
		"Install ‮evil":       false,
		"Install":            false, // private use
		"Install\U000e0001":   false, // a tag character (Cf)
		"Install\U0010fffe":   false, // unassigned
		"Install\tthe daemon": false,
		"Install\xff":         false,
	} {
		if got := SecretRunPurposeShows(text); got != want {
			t.Errorf("SecretRunPurposeShows(%q) = %v, want %v", text, got, want)
		}
	}
}

// How a command runs sudo: only where a command starts, how many times, with -S or not, after a pipe or not; and so
// whether its stdin gets the password.
func TestSecretRunSudoOf(t *testing.T) {
	for cmd, want := range map[string]SecretRunSudo{
		"sudo -S -k -p '' launchctl list":                {Calls: 1},
		"sudo -k -p '' -S whoami":                        {Calls: 1},
		"sudo -kS whoami":                                {Calls: 1},
		"sudo -u root -S whoami":                         {Calls: 1},
		"sudo --user=root --stdin whoami":                {Calls: 1},
		"sudo --prompt '' -S true":                       {Calls: 1},
		"/usr/bin/sudo -S true":                          {Calls: 1},
		"sudo -E -H -S env":                              {Calls: 1},
		"sudo --preserve-env=PATH -S env":                {Calls: 1},
		`sudo -p "pw: " -S true`:                         {Calls: 1},
		"time sudo -S -k -p '' true":                     {Calls: 1},
		"false || sudo -S -k -p '' true":                 {Calls: 1}, // || is not a pipe
		"true && sudo -S -k -p '' true":                  {Calls: 1},
		"sudo -S -k -p '' sh -c 'a | sudo b'":            {Calls: 1}, // inside quotes: not seen
		"FOO=1 sudo -S true && sudo -S -k true":          {Calls: 2},
		"grep 'sudo' /etc/group":                         {},
		"echo hello":                                     {},
		`echo "sudo is not called here"`:                 {},
		"which sudo":                                     {},
		"echo sudo":                                      {},
		"ls -l /usr/bin/sudo":                            {},
		"sudo whoami":                                    {Calls: 1, WithoutStdin: true},
		"sudo -k whoami":                                 {Calls: 1, WithoutStdin: true},
		"sudo -p -S whoami":                              {Calls: 1, WithoutStdin: true}, // -S is -p's argument
		"sudo -u -S whoami":                              {Calls: 1, WithoutStdin: true},
		"true; sudo whoami":                              {Calls: 1, WithoutStdin: true},
		"(sudo whoami)":                                  {Calls: 1, WithoutStdin: true},
		"/usr/bin/sudo whoami":                           {Calls: 1, WithoutStdin: true},
		"sudo -- -S":                                     {Calls: 1, WithoutStdin: true},
		"sudo -s":                                        {Calls: 1, WithoutStdin: true},
		"nohup sudo whoami":                              {Calls: 1, WithoutStdin: true},
		"env FOO=1 sudo whoami":                          {Calls: 1, WithoutStdin: true},
		`FOO="a b" sudo whoami`:                          {Calls: 1, WithoutStdin: true},
		"sudo -S true && sudo rm x":                      {Calls: 2, WithoutStdin: true},
		"sudo -S -- whoami; $(sudo -k id)":               {Calls: 2, WithoutStdin: true},
		"if sudo -n true; then sudo whoami; fi":          {Calls: 2, WithoutStdin: true},
		"echo x | sudo tee /etc/x":                       {Calls: 1, WithoutStdin: true, AfterPipe: true},
		"echo x | sudo -S -k -p '' tee /etc/x":           {Calls: 1, AfterPipe: true},
		"sudo -S -k -p '' launchctl bootstrap gui/ x.pl": {Calls: 1},
	} {
		got := SecretRunSudoOf(cmd)
		if got != want {
			t.Errorf("SecretRunSudoOf(%q) = %+v, want %+v", cmd, got, want)
		}
		if gets := want.Calls == 1 && !want.WithoutStdin && !want.AfterPipe; got.GetsPassword() != gets {
			t.Errorf("%q: GetsPassword = %v, want %v", cmd, got.GetsPassword(), gets)
		}
	}
}
