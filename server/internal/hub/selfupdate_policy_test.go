package hub

import (
	"context"
	"github.com/nwverse/persona/server/internal/hubedge"
	"github.com/nwverse/persona/server/internal/proto"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/update"
)

func TestHubOnlyUpdateNeverFetchesOrInstallsNodePackages(t *testing.T) {
	p := newPublisherWith(t, "v2-202611021700", 202611021700, func(m *update.Manifest) {
		// This advertised platform has no download endpoint. A Hub-only update must
		// succeed without ever requesting it.
		m.Files["plan9-arm"] = update.File{Name: "personad-plan9-arm.gz", Size: 1}
	})
	h, _, _, execs := updatingHub(t, p, "release")
	if h.devicesConfig().AutoUpdateNodes {
		t.Fatal("node updates enabled by default")
	}
	nodes := filepath.Join(t.TempDir(), "absent-nodes")
	t.Setenv("PERSONA_NODE_BINARIES", nodes)
	if _, err := h.upd.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.upd.begin(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Hub-only restart", func() bool { return len(execs()) == 1 })
	var pending update.Pending
	if ok, err := update.Load(update.PendingPath(h.upd.dir), &pending); !ok || err != nil {
		t.Fatalf("pending %v %v", ok, err)
	}
	if pending.Nodes != "" {
		t.Fatalf("staged nodes: %s", pending.Nodes)
	}
	h.upd.updateNodes(context.Background(), t.TempDir(), pending.FromVersion)
	if _, err := os.Stat(nodes); !os.IsNotExist(err) {
		t.Fatalf("node directory recreated: %v", err)
	}
	h.cfg.AutoUpdateNodes = true
	if !h.devicesConfig().AutoUpdateNodes {
		t.Fatal("explicit policy not handed to manager")
	}
}

// A hub that updated itself alone sets aside the unlabelled node builds of its previous version: the manager would
// hand them out as the hub's own version, and Redeploy would put the old node back (#1825).
func TestHubOnlyUpdateSetsAsideOldNodeBuilds(t *testing.T) {
	nodes := t.TempDir()
	t.Setenv("PERSONA_NODE_BINARIES", nodes)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(nodes, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("personad-linux-arm64", "v1 build")           // left by the last deploy: passes for the hub's version
	write("personad-darwin-arm64", "a release's build") // labelled: says what it is, stays
	write("personad-darwin-arm64.version", "v1-202611010000\n")
	write("persona-node-linux-arm64", "persona-node") // only ever used with a matching version file
	write("persona-node-linux-arm64.version", "v1\n")
	write("personad-linux-arm64.gz", "an archive") // not a build
	h := newTestHub(t)
	h.version = "v2"

	h.upd.updateNodes(context.Background(), "", "v2") // the same version again: still the hub's own
	if _, err := os.Stat(filepath.Join(nodes, "personad-linux-arm64")); err != nil {
		t.Fatalf("a reinstall of the same version moved the hub's own build: %v", err)
	}

	h.upd.updateNodes(context.Background(), "", "v1")
	if _, err := os.Stat(filepath.Join(nodes, "personad-linux-arm64")); !os.IsNotExist(err) {
		t.Fatalf("the v1 build still passes for v2: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(nodes, "personad-linux-arm64.prev")); err != nil || string(b) != "v1 build" {
		t.Fatalf("set aside: %q %v", b, err)
	}
	for _, kept := range []string{"personad-darwin-arm64", "personad-darwin-arm64.version", "persona-node-linux-arm64",
		"persona-node-linux-arm64.version", "personad-linux-arm64.gz"} {
		if _, err := os.Stat(filepath.Join(nodes, kept)); err != nil {
			t.Errorf("%s: %v", kept, err)
		}
	}

	h.cfg.AutoUpdateNodes = true // node updates on: the release's builds replace them instead (TestUpdateNodesBinariesInPlace)
	write("personad-linux-amd64", "v2 build")
	h.version = "v3"
	h.upd.updateNodes(context.Background(), "", "v2")
	if _, err := os.Stat(filepath.Join(nodes, "personad-linux-amd64")); err != nil {
		t.Fatalf("set aside with node updates on: %v", err)
	}
}

func TestUpdaterReadsPrivateFileSource(t *testing.T) {
	h := newTestHub(t)
	dir := t.TempDir()
	manifest := `{"version":"v2","build":202611021700,"files":{"linux-amd64":{"name":"personad-linux-amd64.gz"}}}`
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	h.build.UpdateURL = "file://" + dir
	if _, err := h.upd.check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHubOnlySlotKeepsRemoteAgentsIndependent(t *testing.T) {
	p := newPublisher(t, "v2-202611021700", 202611021700)
	h, m, _, _ := updatingHub(t, p, "release")
	setSessions(m, proto.Session{SessionID: "long-task", Status: proto.StatusWorking, Host: proto.EngineHerdr})
	h.slot = &hubedge.Slot{}
	if busy := h.updateBusy(); len(busy) != 0 {
		t.Fatalf("remote node held Hub-only switch: %+v", busy)
	}
	setSessions(m, proto.Session{SessionID: "direct-assistant", Status: proto.StatusWorking})
	if len(h.updateBusy()) != 1 {
		t.Fatal("unknown remote assistant was not protected")
	}
	m.info.NodeUpdate = true
	if len(h.updateBusy()) != 0 {
		t.Fatal("resident node should survive transport reconnect")
	}
	m.info.NodeUpdate = false
	setSessions(m, proto.Session{SessionID: "long-task", Status: proto.StatusWorking, Host: proto.EngineHerdr})
	h.cfg.AutoUpdateNodes = true
	if len(h.updateBusy()) != 1 {
		t.Fatal("fleet update did not wait for agent")
	}
	h.cfg.AutoUpdateNodes = false
	m.cfg.Kind = "local"
	if len(h.updateBusy()) != 1 {
		t.Fatal("local agent was not protected")
	}
}
