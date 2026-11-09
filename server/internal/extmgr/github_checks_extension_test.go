package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type snapshotExtension struct {
	tools []proto.Tool
	text  string
	err   error
	calls int
}

func (*snapshotExtension) ID() string   { return "github" }
func (*snapshotExtension) Name() string { return "GitHub" }
func (*snapshotExtension) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapTools, connector.CapStatus}, Hosts: []string{"github.com"}}
}
func (*snapshotExtension) Status(context.Context) connector.Status {
	return connector.Status{State: connector.StateOK}
}
func (c *snapshotExtension) Tools() []proto.Tool { return c.tools }
func (c *snapshotExtension) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	c.calls++
	if name != githubSnapshotTool || string(args) != `{"number":12,"repo":"example/repo"}` {
		return "", &rpc.Error{Message: "wrong call"}
	}
	return c.text, c.err
}

const validSnapshot = `{"version":1,"host":"github.com","repo":"example/repo","number":12,"head":"0123456789abcdef0123456789abcdef01234567","head_after":"0123456789abcdef0123456789abcdef01234567","complete":true,"check_runs":[{"id":1,"name":"lint","status":"completed","conclusion":"failure"}],"statuses":[{"context":"preview","state":"pending"}]}`

func TestGitHubSnapshotExtensionContract(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	c := &snapshotExtension{tools: []proto.Tool{{Name: "github_checks"}}, text: validSnapshot}
	if err := h.connectors.Add(c); err != nil {
		t.Fatal(err)
	}
	if _, _, available, err := h.PRChecks(ctx, "example/repo", 12); available || err != nil || c.calls != 0 {
		t.Fatalf("old tool was called: %v %v %d", available, err, c.calls)
	}
	c.tools = []proto.Tool{{Name: githubSnapshotTool}}
	got, _, available, err := h.PRChecks(ctx, "example/repo", 12)
	if err != nil || !available || c.calls != 1 || !got.Pending || got.Count != 2 || len(got.Failures) != 1 || got.Failures[0] != "lint" || got.Fingerprint == "" {
		t.Fatalf("snapshot %+v available %v error %v", got, available, err)
	}
	for _, invalid := range []string{
		"human text with logs",
		strings.Replace(validSnapshot, `"complete":true`, `"complete":false`, 1),
		strings.Replace(validSnapshot, `"host":"github.com"`, `"host":"github.example.com"`, 1),
		strings.Replace(validSnapshot, `"head_after":"0123456789abcdef0123456789abcdef01234567"`, `"head_after":"fedcba9876543210fedcba9876543210fedcba98"`, 1),
		strings.Replace(validSnapshot, `"number":12`, `"number":13`, 1),
		strings.Replace(validSnapshot, `"statuses":[{"context":"preview","state":"pending"}]`, `"statuses":null`, 1),
		strings.Replace(validSnapshot, `"version":1`, `"version":2`, 1),
		strings.Replace(validSnapshot, `"complete":true`, `"complete":true,"log":"not allowed"`, 1),
		strings.Replace(validSnapshot, `"statuses":[{"context":"preview","state":"pending"}]`, `"statuses":[{"context":"preview","state":"pending"},{"context":"preview","state":"success"}]`, 1),
		validSnapshot + ` {}`,
	} {
		c.text = invalid
		if _, _, available, _ := h.PRChecks(ctx, "example/repo", 12); available {
			t.Fatalf("accepted invalid snapshot %s", invalid)
		}
	}
}

func TestGitHubSnapshotExtensionRateLimit(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	c := &snapshotExtension{tools: []proto.Tool{{Name: githubSnapshotTool}}, err: &rpc.Error{Message: "rate limit", Code: proto.ToolErrorRateLimited, Data: json.RawMessage(`{"retry_after":180}`)}}
	if err := h.connectors.Add(c); err != nil {
		t.Fatal(err)
	}
	_, reset, available, err := h.PRChecks(ctx, "example/repo", 12)
	if err != nil || !available || reset.Before(time.Now().Add(2*time.Minute)) {
		t.Fatalf("rate limit %v %v %v", available, reset, err)
	}
}
