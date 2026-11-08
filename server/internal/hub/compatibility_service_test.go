package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
)

func TestCompatibleHubBinaryPreservesOneInstallation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			e, _ := testSetupEnv(t, setupTestHome(t), setupOptions{}, &fakeCommands{}, "systemctl")
			current := filepath.Join(e.root, "bin", "personad")
			old := filepath.Join(e.root, "bin", compat.DaemonBinary)
			want := current
			if legacy {
				if err := os.Rename(current, old); err != nil {
					t.Fatal(err)
				}
				want = old
			}
			want = realPath(t, want)
			got, err := e.hubBinary()
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("primary %q, want %q", got, want)
			}
			if !legacy {
				// personad's own units: no binary under the former name (#1445)
				if _, err := os.Lstat(old); !os.IsNotExist(err) {
					t.Fatal("the former binary name was made", err)
				}
				return
			}
			a, ae := os.Stat(current)
			b, be := os.Stat(old)
			if ae != nil || be != nil || !os.SameFile(a, b) {
				t.Fatal("executable names diverged")
			}
			if _, err = e.hubBinary(); err != nil {
				t.Fatal(err)
			}
			// Atomic replacement of the primary must leave its alias attached.
			incoming := filepath.Join(t.TempDir(), "replacement")
			os.WriteFile(incoming, []byte("replacement"), 0o755)
			if err := copyFile(incoming, got, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{current, old} {
				data, err := os.ReadFile(name)
				if err != nil || string(data) != "replacement" {
					t.Fatalf("alias lost after swap: %q %v", name, err)
				}
			}
		})
	}
}

// An installation whose units still run under the former names (their ExecStart names the former binary) keeps that
// binary name next to personad (#1445).
func TestCompatibleHubBinaryKeepsTheFormerNameForFormerUnits(t *testing.T) {
	e, _ := testSetupEnv(t, setupTestHome(t), setupOptions{}, &fakeCommands{}, "systemctl")
	current := filepath.Join(e.root, "bin", "personad")
	old := filepath.Join(e.root, "bin", compat.DaemonBinary)
	if err := os.MkdirAll(e.unitDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{".service", ".socket"} {
		if err := os.WriteFile(filepath.Join(e.unitDir(), compat.HubService+kind), []byte("[Service]\nExecStart="+old+" hub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.hubBinary(); err != nil {
		t.Fatal(err)
	}
	a, ae := os.Stat(current)
	b, be := os.Stat(old)
	if ae != nil || be != nil || !os.SameFile(a, b) {
		t.Fatal("the former units' binary name is missing", ae, be)
	}
}

func TestCompatibleHubBinaryRejectsIndependentFiles(t *testing.T) {
	e, _ := testSetupEnv(t, setupTestHome(t), setupOptions{}, &fakeCommands{})
	old := filepath.Join(e.root, "bin", compat.DaemonBinary)
	os.WriteFile(old, []byte("independent"), 0o755)
	if _, err := e.hubBinary(); err == nil {
		t.Fatal("accepted independent installations")
	}
	if b, _ := os.ReadFile(old); string(b) != "independent" {
		t.Fatal("existing binary was overwritten")
	}
}

func TestCompatibleHubUnitsKeepLegacyEnvironmentAndSocket(t *testing.T) {
	cmds := &fakeCommands{}
	e, _ := testSetupEnv(t, setupTestHome(t), setupOptions{}, cmds, "systemctl")
	// Absence, rather than an explicit empty value, preserves socket mode.
	os.Unsetenv("PERSONA_HUB_SOCKET")
	_, oldSocketEnv := compat.EnvNames("PERSONA_HUB_SOCKET")
	t.Setenv(oldSocketEnv, "")
	os.Unsetenv(oldSocketEnv)
	os.MkdirAll(e.unitDir(), 0o755)
	service := "[Service]\nExecStart=" + filepath.Join(e.root, "bin", compat.DaemonBinary) + " hub\nEnvironmentFile=%h/.config/private/hub.env\n"
	socket := "[Socket]\nListenStream=18662\nService=" + compat.HubService + ".service\n"
	svcPath := filepath.Join(e.unitDir(), compat.HubService+".service")
	sockPath := filepath.Join(e.unitDir(), compat.HubSocket)
	os.WriteFile(svcPath, []byte(service), 0o644)
	os.WriteFile(sockPath, []byte(socket), 0o644)
	bin, err := e.hubBinary()
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := binaryChanged(bin)
	noteBinary(bin, sum)
	if err := e.systemdService(context.Background(), &Config{Listen: ":18662"}, bin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(svcPath); string(b) != service {
		t.Fatal("private environment file was lost")
	}
	if b, _ := os.ReadFile(sockPath); string(b) != socket {
		t.Fatal("unchanged socket was rewritten")
	}
	for _, call := range cmds.calls {
		if strings.Contains(call, "personad") || strings.Contains(call, "restart") || strings.Contains(call, "stop ") {
			t.Fatalf("unneeded identity switch or restart: %s", call)
		}
	}
	os.WriteFile(filepath.Join(e.unitDir(), "personad.service"), []byte("independent"), 0o644)
	if _, err := e.compatibleHubUnits(); err == nil {
		t.Fatal("independent service families were accepted")
	}
}
