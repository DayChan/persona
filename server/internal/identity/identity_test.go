package identity

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
)

func cleanRootEnv(t *testing.T) {
	t.Helper()
	current, legacy := compat.EnvNames("PERSONA_HOME")
	for _, key := range []string{current, legacy} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnvironment(t *testing.T) {
	cleanRootEnv(t)
	if value, ok := LookupEnv("PERSONA_HOME"); value != "" || ok {
		t.Fatal("unset variable must remain absent")
	}
	t.Setenv("OTHER_HOME", "unrelated")
	if value, ok := LookupEnv("PERSONA_HOME"); value != "" || ok {
		t.Fatal("unrelated variables must not supply a root")
	}
	t.Setenv("PERSONA_HOME", "profile")
	if Getenv("PERSONA_HOME") != "profile" {
		t.Fatal("configured environment was ignored")
	}
	t.Setenv("PERSONA_HOME", "")
	if value, ok := LookupEnv("PERSONA_HOME"); value != "" || !ok {
		t.Fatal("explicit empty values must remain present")
	}
	_, legacySession := compat.EnvNames("PERSONA_SESSION_ID")
	base := []string{"PERSONA_SESSION_ID=wrong", "PERSONA_SESSION_ID=older", "PATH=first", "PATH=second", "invalid"}
	for _, updates := range [][]string{{"PERSONA_SESSION_ID=right"}, {"PERSONA_SESSION_ID=obsolete", "PERSONA_SESSION_ID=right", "invalid"}} {
		want := []string{"PERSONA_SESSION_ID=right", "PATH=second", legacySession + "=right"}
		if got := Environment(base, updates...); !reflect.DeepEqual(got, want) {
			t.Fatalf("environment: %v", got)
		}
	}
	if got := Environment(base, "PERSONA_SESSION_ID=", "NEW=value=with=equals"); !reflect.DeepEqual(got, []string{"PERSONA_SESSION_ID=", "PATH=second", legacySession + "=", "NEW=value=with=equals"}) {
		t.Fatalf("empty values or new keys lost: %v", got)
	}
	t.Setenv("PERSONA_PERSONA_ID", "avatar")
	if Getenv("PERSONA_PERSONA_ID") != "avatar" {
		t.Fatal("environment suffix must not be rewritten")
	}
}

func TestStateDirectory(t *testing.T) {
	cleanRootEnv(t)
	for _, scenario := range []string{"fresh", "existing", "external", "relative-external", "dangling-external", "file", "symlink-file", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".persona")
			external := filepath.Join(home, "external")
			mkdir := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			link := func(target string) {
				t.Helper()
				if err := os.Symlink(target, root); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "existing":
				mkdir(root)
			case "external":
				mkdir(external)
				link(external)
			case "relative-external":
				mkdir(external)
				link("external")
			case "dangling-external":
				link(external)
			case "file":
				if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink-file":
				if err := os.WriteFile(external, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				link(external)
			case "cycle":
				link(".persona")
			}
			got, err := PrepareHome(home)
			if scenario == "dangling-external" || scenario == "file" || scenario == "symlink-file" || scenario == "cycle" {
				if err == nil {
					t.Fatal("invalid state directory was accepted")
				}
				if _, err := os.Lstat(root); err != nil {
					t.Fatal("invalid existing state was removed", err)
				}
				if scenario == "dangling-external" {
					if _, err := os.Lstat(external); !os.IsNotExist(err) {
						t.Fatal("dangling target was created", err)
					}
				}
				return
			}
			if err != nil || got != root {
				t.Fatalf("root=%s want=%s err=%v", got, root, err)
			}
			payload := []byte("unchanged token and state")
			if err := os.WriteFile(filepath.Join(root, "hub.json"), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareHome(home); err != nil {
				t.Fatal("repeat initialization:", err)
			}
			data, err := os.ReadFile(filepath.Join(root, "hub.json"))
			if err != nil || string(data) != string(payload) {
				t.Fatal("state was overwritten", err)
			}
			entries, err := os.ReadDir(home)
			wantEntries := 1 // one store, no alias under the former name (#1445)
			if scenario == "external" || scenario == "relative-external" {
				wantEntries = 2
			}
			if err != nil || len(entries) != wantEntries {
				t.Fatal("initialization created another state directory", err)
			}
			if scenario == "fresh" {
				st, err := os.Lstat(root)
				if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0o700 {
					t.Fatal("fresh state must be a private real directory", err)
				}
			}
		})
	}
}

func TestIndependentHomes(t *testing.T) {
	cleanRootEnv(t)
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "profile")
	t.Setenv("PERSONA_HOME", root)
	if got, err := PrepareHome(home); err != nil || got != root {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".persona")); !os.IsNotExist(err) {
		t.Fatal("custom environment changed default state")
	}
	if SocketDir(home) != root || !IsAssistantDir(filepath.Join(root, "assistant")) {
		t.Fatal("custom directory identity lost")
	}
	for _, path := range []string{"/tmp/.persona/assistant", "/tmp/.persona/health"} {
		if !IsAssistantDir(path) {
			t.Fatal(path)
		}
	}
	if IsAssistantDir("/tmp/project/assistant") {
		t.Fatal("ordinary project became the assistant")
	}
	for _, tc := range []struct{ value, want string }{{"~", home}, {"~/profile", filepath.Join(home, "profile")}, {"profile", filepath.Join(home, "profile")}, {"", filepath.Join(home, ".persona")}} {
		t.Setenv("PERSONA_HOME", tc.value)
		if got := DataDir(home); got != tc.want {
			t.Fatalf("%q: %s want %s", tc.value, got, tc.want)
		}
	}
}

// Both default directories exist as independent stores (an older installation's beside the current one, #744): a node
// whose service names one with PERSONA_HOME starts on it, the other is left as it is, and without PERSONA_HOME
// neither is picked.
func TestExplicitRootBesideAnIndependentStore(t *testing.T) {
	cleanRootEnv(t)
	home := t.TempDir()
	current, legacy := filepath.Join(home, ".persona"), filepath.Join(home, compat.StateDir)
	for _, dir := range []string{current, legacy} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "store"), []byte(dir), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	untouched := func() {
		t.Helper()
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 2 {
			t.Fatal("home changed", entries, err)
		}
		for _, dir := range []string{current, legacy} {
			st, err := os.Lstat(dir)
			if err != nil || !st.IsDir() {
				t.Fatal(dir, "is no longer a directory", err)
			}
			if b, err := os.ReadFile(filepath.Join(dir, "store")); err != nil || string(b) != dir {
				t.Fatal(dir, "store changed", err)
			}
		}
	}
	if _, err := PrepareHome(home); err == nil {
		t.Fatal("independent stores without PERSONA_HOME: one was picked")
	}
	untouched()
	for _, tc := range []struct{ value, want string }{
		{current, current}, {"~/.persona", current}, {".persona", current}, {legacy, legacy},
	} {
		t.Setenv("PERSONA_HOME", tc.value)
		got, err := PrepareHome(home)
		if err != nil || got != tc.want {
			t.Fatalf("PERSONA_HOME=%s: %s %v", tc.value, got, err)
		}
		if DataDir(home) != tc.want || SocketDir(home) != tc.want {
			t.Fatalf("PERSONA_HOME=%s: data %s socket %s", tc.value, DataDir(home), SocketDir(home))
		}
		untouched()
	}
}

func TestConcurrentFirstRun(t *testing.T) {
	cleanRootEnv(t)
	home := t.TempDir()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := PrepareHome(home); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".persona" {
		t.Fatal("concurrent initialization did not create exactly one store", entries, err)
	}
}

// The former-name alias (#1445): an installation still under the former name gets the current name as an alias, so
// both reach the one store, also when several start at once; one under the current name gets none, and an alias an
// operator left (or removed) stays as it is.
func TestFormerNameAlias(t *testing.T) {
	cleanRootEnv(t)
	t.Run("a former-name store gets the current name", func(t *testing.T) {
		home := t.TempDir()
		legacy := filepath.Join(home, compat.StateDir)
		if err := os.MkdirAll(legacy, 0o700); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// either name, once the alias is there: the one store
				got, err := PrepareHome(home)
				a, aerr := os.Stat(got)
				b, berr := os.Stat(legacy)
				if err != nil || aerr != nil || berr != nil || !os.SameFile(a, b) {
					t.Error(got, err)
				}
			}()
		}
		wg.Wait()
		if target, err := os.Readlink(filepath.Join(home, ".persona")); err != nil || target != compat.StateDir {
			t.Fatalf("current name: %q %v", target, err)
		}
	})
	t.Run("a current-name store keeps an alias that is there, and gets none back once removed", func(t *testing.T) {
		home := t.TempDir()
		current, legacy := filepath.Join(home, ".persona"), filepath.Join(home, compat.StateDir)
		if err := os.MkdirAll(current, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(".persona", legacy); err != nil {
			t.Fatal(err)
		}
		if got, err := PrepareHome(home); err != nil || got != current {
			t.Fatal(got, err)
		}
		if target, err := os.Readlink(legacy); err != nil || target != ".persona" {
			t.Fatalf("the operator's alias changed: %q %v", target, err)
		}
		if err := os.Remove(legacy); err != nil {
			t.Fatal(err)
		}
		if got, err := PrepareHome(home); err != nil || got != current {
			t.Fatal(got, err)
		}
		if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
			t.Fatal("the former name came back", err)
		}
	})
}
