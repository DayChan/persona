package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/proto"
)

// TestMain runs the tests on a database of their own (dbtest, #583): other runs on the same machine do not touch it.
func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// open connects to PERSONA_TEST_DATABASE_URL (a throwaway database; scripts/test/test-server.sh creates it on the hub machine)
// and wipes every table so each test starts clean.
func open(t testing.TB) *DB {
	t.Helper()
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	d, err := Open(context.Background(), url, AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	// Union of the integration branch's list and master's TruncateForTest: TruncateForTest alone
	// does not clear lock_tests, client_ops, pair_tokens or device_sessions.
	if _, err := d.pool.Exec(context.Background(), `TRUNCATE push_device_retirements, lock_tests, client_ops, devices, device_tokens, pair_tokens, device_sessions, live_activities, phone_tools, node_sessions, mail_accounts, interactions, settings, inbox_items, kb_entities, connector_secrets, machine_systems, machine_metrics, machine_usage, sent_messages, todos, todo_agents, assistant_always, sent_as_user CASCADE`); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLiveActivityStartBudget(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	const cooldown, maxAge = 10 * time.Minute, 8 * time.Hour
	if err := d.UpsertDevice(ctx, Device{DeviceID: "phone"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "phone", PushToStartToken: "start"}); err != nil {
		t.Fatal(err)
	}
	// Independent pools represent the two blue-green processes, not an in-memory mutex.
	other, err := Open(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan bool, 20)
	for i := range 20 {
		pool := d
		if i%2 == 1 {
			pool = other
		}
		wg.Go(func() {
			due, claimed, err := pool.ClaimLiveActivityStart(ctx, "phone", "start", at, cooldown, maxAge)
			if err != nil {
				t.Error(err)
			}
			if !due.Equal(at.Add(cooldown)) {
				t.Errorf("claim due=%s", due)
			}
			results <- claimed
		})
	}
	wg.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("two pools made %d claims, want 1", claims)
	}
	// Registration, a new episode, token rotation and client started_at are not the budget.
	if err := d.UpsertDevice(ctx, Device{DeviceID: "phone", APNSToken: "renewed"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "phone", PushToStartToken: "rotated", StartedAt: at.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	due, claimed, err := other.ClaimLiveActivityStart(ctx, "phone", "rotated", at.Add(cooldown-time.Microsecond), cooldown, maxAge)
	if err != nil || claimed || !due.Equal(at.Add(cooldown)) {
		t.Fatalf("renewal reset budget: due=%s claimed=%v err=%v", due, claimed, err)
	}
	// A committed fresh update token suppresses a stale process even at expiry.
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "phone", PushToStartToken: "rotated", PushToken: "update", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	due, claimed, err = other.ClaimLiveActivityStart(ctx, "phone", "rotated", at.Add(cooldown), cooldown, maxAge)
	if err != nil || claimed || !due.IsZero() {
		t.Fatalf("fresh token allowed start: %s %v %v", due, claimed, err)
	}
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "phone", PushToStartToken: "rotated"}); err != nil {
		t.Fatal(err)
	}
	_, claimed, err = other.ClaimLiveActivityStart(ctx, "phone", "old-token", at.Add(cooldown), cooldown, maxAge)
	if err != nil || claimed {
		t.Fatalf("stale token allowed start: %v %v", claimed, err)
	}
	_, claimed, err = other.ClaimLiveActivityStart(ctx, "phone", "rotated", at.Add(cooldown), cooldown, maxAge)
	if err != nil || !claimed {
		t.Fatalf("expiry failed: %v %v", claimed, err)
	}
	// A budget write error must roll back, never grant dispatch permission.
	if _, err := d.pool.Exec(ctx, `ALTER TABLE live_activity_start_budget ADD CONSTRAINT reject_test_claim CHECK (claimed_at < '2026-11-09 00:20:00+00')`); err != nil {
		t.Fatal(err)
	}
	_, claimed, err = d.ClaimLiveActivityStart(ctx, "phone", "rotated", at.Add(2*cooldown), cooldown, maxAge)
	if err == nil || claimed {
		t.Fatalf("failed budget save allowed push: %v %v", claimed, err)
	}
	if _, err := d.pool.Exec(ctx, `ALTER TABLE live_activity_start_budget DROP CONSTRAINT reject_test_claim`); err != nil {
		t.Fatal(err)
	}
	_, claimed, err = other.ClaimLiveActivityStart(ctx, "phone", "rotated", at.Add(2*cooldown), cooldown, maxAge)
	if err != nil || !claimed {
		t.Fatalf("failed claim did not roll back: %v %v", claimed, err)
	}
	// FK cleanup is confined to forgetting the device, not registering it again.
	if err := d.DeleteDevice(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM live_activity_start_budget`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("budget FK cleanup: n=%d err=%v", n, err)
	}
	t.Log("two independent pools: 20 concurrent attempts, one claim; renewal retained budget; failed save rolled back")
}

func TestLiveActivityStartParentLockOrder(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.UpsertDevice(ctx, Device{DeviceID: "phone"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "phone", PushToStartToken: "start"}); err != nil {
		t.Fatal(err)
	}
	// Retirement holds the parent first. A claimant must wait there, without
	// holding the activity row that retirement will delete next.
	parent, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Rollback(ctx)
	if _, err := parent.Exec(ctx, `SELECT 1 FROM devices WHERE device_id='phone' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := d.ClaimLiveActivityStart(claimCtx, "phone", "start", time.Now(), 10*time.Minute, 8*time.Hour)
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var blocked bool
		if err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE 'SELECT 1 FROM devices WHERE device_id=$1 FOR KEY SHARE%' AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("claim did not wait on parent lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := parent.Exec(ctx, `SELECT 1 FROM live_activities WHERE device_id='phone' FOR UPDATE NOWAIT`); err != nil {
		t.Fatalf("claim locked activity before parent: %v", err)
	}
	if err := parent.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	d := open(t)
	if err := d.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceSessions(t *testing.T) {
	d, ctx := open(t), context.Background()
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	insert := func(device string, start, end time.Duration, reason string) int64 {
		t.Helper()
		id, err := d.InsertDeviceSession(ctx, DeviceSession{DeviceID: device, ConnectedAt: base.Add(start),
			Source: "192.0.2.0/24", HeartbeatInterval: 20})
		if err != nil {
			t.Fatal(err)
		}
		if reason != "" {
			if err := d.FinishDeviceSession(ctx, id, base.Add(end), reason, 20); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	insert("device-a", -2*time.Hour, -30*time.Minute, "closed")
	a := insert("device-a", 10*time.Minute, 20*time.Minute, "pong_timeout")
	insert("device-a", 25*time.Minute, 30*time.Minute, "hub_stopped")
	insert("device-a", 50*time.Minute, 55*time.Minute, "closed")
	insert("device-a", 52*time.Minute, 58*time.Minute, "error")
	insert("device-b", 0, 0, "")
	history, err := d.DeviceSessions(ctx, "device-a", base, base.Add(time.Hour))
	if err != nil || len(history.Sessions) != 4 || history.Summary.Disconnects != 3 || history.Summary.LongestGapSeconds != 600 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if s := history.Sessions[0]; s.ID != a || s.Source != "192.0.2.0/24" || s.HeartbeatInterval != 20 || s.Reason != "pong_timeout" {
		t.Fatalf("session = %+v", s)
	}
	// The restart gap is excluded, overlapping sessions stay online, and the trailing gap is included.
	history, err = d.DeviceSessions(ctx, "device-a", base.Add(26*time.Minute), base.Add(time.Hour))
	if err != nil || len(history.Sessions) != 3 || history.Summary.Disconnects != 2 || history.Summary.LongestGapSeconds != 120 {
		t.Fatalf("window history = %+v, %v", history, err)
	}
	live := insert("device-a", 59*time.Minute, 0, "")
	if err := d.UpdateDeviceSessionHeartbeat(ctx, live, 240); err != nil {
		t.Fatal(err)
	}
	history, err = d.DeviceSessions(ctx, "device-a", base.Add(58*time.Minute), base.Add(time.Hour))
	if err != nil || len(history.Sessions) != 2 || history.Summary.Disconnects != 1 || history.Summary.LongestGapSeconds != 60 {
		t.Fatalf("live history = %+v, %v", history, err)
	}
	if s := history.Sessions[1]; s.DisconnectedAt != nil || s.HeartbeatInterval != 240 {
		t.Fatalf("live session = %+v", s)
	}
	if err := d.StopDeviceSessions(ctx, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishDeviceSession(ctx, live, base.Add(2*time.Hour), "error", 20); err != nil {
		t.Fatal(err)
	}
	history, err = d.DeviceSessions(ctx, "device-a", base.Add(59*time.Minute), base.Add(2*time.Hour))
	if err != nil || len(history.Sessions) != 1 || history.Sessions[0].Reason != "hub_stopped" ||
		history.Sessions[0].HeartbeatInterval != 240 || history.Summary.Disconnects != 0 || history.Summary.LongestGapSeconds != 0 {
		t.Fatalf("recovered history = %+v, %v", history, err)
	}
	old := insert("device-a", -16*24*time.Hour, -15*24*time.Hour, "replaced")
	if err := d.PruneDeviceSessions(ctx, base.Add(-14*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM device_sessions WHERE id = $1`, old).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pruned count = %d, %v", count, err)
	}
	history, err = d.DeviceSessions(ctx, "missing", base, base.Add(time.Hour))
	if err != nil || history.Sessions == nil || len(history.Sessions) != 0 || history.Summary != (DeviceSessionSummary{}) {
		t.Fatalf("empty history = %+v, %v", history, err)
	}
}

func TestClientOps(t *testing.T) {
	d, ctx := open(t), context.Background()
	op := ClientOp{ID: "00000000-0000-4000-8000-000000000001", DeviceID: "device", Kind: "message",
		Target: json.RawMessage(`{"path":"/api/v1/sessions/m/s/messages"}`), Digest: "digest"}
	var wg sync.WaitGroup
	claims := make(chan bool, 8)
	for i := 0; i < cap(claims); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stored, claimed, err := d.BeginClientOp(ctx, op)
			if err != nil || stored == nil || stored.State != "pending" || stored.Digest != op.Digest {
				t.Errorf("claim = %+v, %v", stored, err)
			}
			claims <- claimed
		}()
	}
	wg.Wait()
	close(claims)
	count := 0
	for claimed := range claims {
		if claimed {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("executors = %d", count)
	}
	result := json.RawMessage(`{"status":201,"body":"eyAib2siOiB0cnVlIH0K"}`)
	if err := d.FinishClientOp(ctx, op.ID, "done", result); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishClientOp(ctx, op.ID, "unknown", nil); err != nil {
		t.Fatal(err)
	}
	op.Digest = "changed"
	stored, claimed, err := d.BeginClientOp(ctx, op)
	if err != nil || claimed || stored.State != "done" || stored.Digest != "digest" {
		t.Fatalf("existing = %+v, claimed = %v, err = %v", stored, claimed, err)
	}
	var saved struct {
		Status int
		Body   []byte
	}
	if err := json.Unmarshal(stored.Result, &saved); err != nil || saved.Status != 201 || string(saved.Body) != "{ \"ok\": true }\n" {
		t.Fatalf("saved result = %s, %v", stored.Result, err)
	}
	op.ID = "00000000-0000-4000-8000-000000000002"
	if _, _, err := d.BeginClientOp(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := d.RecoverClientOps(ctx); err != nil {
		t.Fatal(err)
	}
	if stored, err := d.ClientOp(ctx, op.ID); err != nil || stored.State != "unknown" {
		t.Fatalf("recovered = %+v, %v", stored, err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE client_ops SET created_at = now() - interval '31 days' WHERE id = $1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.CleanupClientOps(ctx, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	if stored, err := d.ClientOp(ctx, op.ID); err != nil || stored != nil {
		t.Fatalf("expired = %+v, %v", stored, err)
	}
	if stored, err := d.ClientOp(ctx, "00000000-0000-4000-8000-000000000001"); err != nil || stored == nil {
		t.Fatalf("recent receipt removed: %+v, %v", stored, err)
	}
}

func TestDevices(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.UpsertDevice(ctx, Device{DeviceID: "a", Name: "iPhone", APNSToken: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDevice(ctx, Device{DeviceID: "a", Name: "iPhone", APNSToken: "t2", PushKey: "pk", RelayURL: "https://relay.example/relay", RelayGrant: "g", Credential: "dev_1", RelayError: "403 topic not served", Features: []string{"withdraw"}}); err != nil {
		t.Fatal(err)
	}
	list, err := d.Devices(ctx)
	if err != nil || len(list) != 1 || list[0].APNSToken != "t2" || list[0].PushKey != "pk" || list[0].RelayURL != "https://relay.example/relay" || list[0].RelayGrant != "g" || list[0].Credential != "dev_1" || list[0].RelayError != "403 topic not served" || !slices.Equal(list[0].Features, []string{"withdraw"}) {
		t.Fatalf("devices = %+v, %v", list, err)
	}
	if err := d.DeleteDevice(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.Devices(ctx); len(list) != 0 {
		t.Fatalf("device not deleted: %+v", list)
	}
}

// Prefs survive re-registration (the app posts its token on every launch) and can be set before the device has a token.
func TestDevicePrefs(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.SetDevicePrefs(ctx, "a", "", json.RawMessage(`{"done":"off"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDevice(ctx, Device{DeviceID: "a", Name: "iPhone", APNSToken: "t1"}); err != nil {
		t.Fatal(err)
	}
	list, err := d.Devices(ctx)
	if err != nil || len(list) != 1 || list[0].APNSToken != "t1" || string(list[0].Prefs) != `{"done": "off"}` {
		t.Fatalf("devices = %+v (%s), %v", list, list[0].Prefs, err)
	}
	if err := d.UpsertDevice(ctx, Device{DeviceID: "b", Name: "iPad"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range mustDevices(t, d) {
		if v.DeviceID == "b" && v.Prefs != nil {
			t.Fatalf("b has prefs %s", v.Prefs)
		}
	}
}

// A row only prefs made takes the credential that wrote them (revoking it removes the row); a registered one keeps its own.
func TestDevicePrefsCredential(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.SetDevicePrefs(ctx, "web-1", "dev_web", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDevice(ctx, Device{DeviceID: "phone", APNSToken: "t", Credential: "dev_phone"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDevicePrefs(ctx, "phone", "dev_web", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range mustDevices(t, d) {
		got[v.DeviceID] = v.Credential
	}
	if got["web-1"] != "dev_web" || got["phone"] != "dev_phone" {
		t.Fatalf("credentials = %v", got)
	}
}

func TestLiveActivities(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "a", PushToStartToken: "s1"}); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	if err := d.SaveLiveActivity(ctx, LiveActivity{DeviceID: "a", ActivityID: "act", PushToken: "p1", PushToStartToken: "s1", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	list, err := d.LiveActivities(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if v := list[0]; v.ActivityID != "act" || v.PushToken != "p1" || v.PushToStartToken != "s1" || !v.StartedAt.Equal(started) {
		t.Fatalf("%+v", v)
	}
}

func mustDevices(t *testing.T, d *DB) []Device {
	t.Helper()
	list, err := d.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestPhoneToolsReplace(t *testing.T) {
	d, ctx := open(t), context.Background()
	_ = d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "legacy", Tools: []proto.Tool{{Name: "b"}, {Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	_ = d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "legacy", Tools: []proto.Tool{{Name: "z"}, {Name: "a"}}})
	tools, err := d.PhoneTools(ctx)
	if err != nil || len(tools) != 1 || len(tools[0].Tools) != 2 || tools[0].Tools[0].Name != "z" || tools[0].Tools[1].Name != "a" {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
}

// phoneToolList returns n tools named like an app's phone_* announcement, every other one with a schema.
func phoneToolList(n int) []proto.Tool {
	tools := make([]proto.Tool, n)
	for i := range tools {
		tools[i] = proto.Tool{Name: fmt.Sprintf("phone_tool_%02d", i), Description: fmt.Sprintf("tool %d", i)}
		if i%2 == 0 {
			tools[i].InputSchema = json.RawMessage(fmt.Sprintf(`{"type":"object","x":%d}`, i))
		}
	}
	return tools
}

func TestPhoneToolsReplaceReorderedSubset(t *testing.T) {
	d, ctx := open(t), context.Background()
	full := phoneToolList(26)
	if err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "ios-device", Platform: "ios", Tools: full}); err != nil {
		t.Fatal(err)
	}
	// Reversed every third tool: the rest must be gone, the order must follow the new list.
	var subset []proto.Tool
	for i := len(full) - 1; i >= 0; i -= 3 {
		subset = append(subset, full[i])
	}
	subset[1].InputSchema = nil
	if err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "ios-device", Platform: "ios", Tools: subset}); err != nil {
		t.Fatal(err)
	}
	got, err := d.PhoneTools(ctx)
	if err != nil || len(got) != 1 || len(got[0].Tools) != len(subset) {
		t.Fatalf("registrations = %+v, %v", got, err)
	}
	for i, tool := range got[0].Tools {
		want := subset[i]
		schema := string(want.InputSchema)
		if schema == "" {
			schema = `{}`
		}
		if tool.Name != want.Name || tool.Description != want.Description || !jsonEqual(t, string(tool.InputSchema), schema) {
			t.Fatalf("tool %d = %+v, want %+v (schema %s)", i, tool, want, schema)
		}
	}
	var positions []int
	if err := d.pool.QueryRow(ctx, `SELECT array_agg(position ORDER BY position) FROM phone_tools WHERE device_id = 'ios-device'`).Scan(&positions); err != nil {
		t.Fatal(err)
	}
	for i, p := range positions {
		if p != i {
			t.Fatalf("positions = %v", positions)
		}
	}
}

func TestPhoneToolsRevokedCredentialKeepsPreviousTools(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.CreateDeviceToken(ctx, DeviceToken{ID: "credential-ios", Name: "Device", Kind: "phone", Role: "full"}, "hash-ios"); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "other-device", Platform: "ios", Tools: phoneToolList(3)}); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "other-device", Platform: "ios", CredentialID: "credential-ios", Tools: phoneToolList(26)[:2]}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.RevokeDeviceToken(ctx, "credential-ios"); err != nil || !ok {
		t.Fatalf("revoke = %v, %v", ok, err)
	}
	// Revoking deleted this credential's rows; put an unrelated row back on the same device to see it survive.
	if err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "other-device", Platform: "ios", Tools: phoneToolList(3)}); err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"credential-ios", "credential-missing"} {
		err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "other-device", Platform: "ios", CredentialID: credential, Tools: phoneToolList(26)})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s: err = %v, want pgx.ErrNoRows", credential, err)
		}
	}
	got, err := d.PhoneTools(ctx)
	if err != nil || len(got) != 1 || len(got[0].Tools) != 3 || got[0].CredentialID != "" {
		t.Fatalf("registrations after rejected writes = %+v, %v", got, err)
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return fmt.Sprint(x) == fmt.Sprint(y)
}

// BenchmarkReplacePhoneTools replaces one phone's 26-tool announcement, the write the hub makes on every connect.
func BenchmarkReplacePhoneTools(b *testing.B) {
	d, ctx := open(b), context.Background()
	if err := d.CreateDeviceToken(ctx, DeviceToken{ID: "credential-bench", Name: "Device", Kind: "phone", Role: "full"}, "hash-bench"); err != nil {
		b.Fatal(err)
	}
	registration := PhoneToolRegistration{DeviceID: "bench-device", Platform: "ios", CredentialID: "credential-bench", Tools: phoneToolList(26)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := d.ReplacePhoneTools(ctx, registration); err != nil {
			b.Fatal(err)
		}
	}
}

func TestNodeSessions(t *testing.T) {
	d, ctx := open(t), context.Background()
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	_ = d.PutNodeSession(ctx, "server", "s1", NodeSession{Launcher: "cc_ds", Cwd: "/x", StartedAt: start})
	_ = d.PutNodeSession(ctx, "server", "s1", NodeSession{Pane: "w1:p2", Launcher: "cc_ds", Cwd: "/x", StartedAt: start})
	_ = d.PutNodeSession(ctx, "mac", "s2", NodeSession{Launcher: "ccc"})
	got, err := d.NodeSessions(ctx, "server")
	if err != nil || len(got) != 1 || got["s1"].Pane != "w1:p2" || !got["s1"].StartedAt.Equal(start) || len(got["s1"].Tasks) != 0 {
		t.Fatalf("sessions = %+v, %v", got, err)
	}
	_ = d.PutNodeSession(ctx, "server", "s1", NodeSession{Launcher: "cc_ds", Cwd: "/x", StartedAt: start, Tasks: []string{"tail log", "sleep"}})
	if got, _ = d.NodeSessions(ctx, "server"); len(got["s1"].Tasks) != 2 || got["s1"].Tasks[0] != "tail log" {
		t.Fatalf("tasks = %v", got["s1"].Tasks)
	}
}

// state.load reaches node_sessions through node_sessions_started, so it reads only the 60-day window instead of every
// session the machine ever had, both planned for its machine and as the generic plan a cached statement may switch to.
func TestNodeSessionsStartedIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `PREPARE node_sessions_load(text) AS `+nodeSessionsSQL); err != nil {
		t.Fatal(err)
	}
	// A prepared statement outlives the transaction; the connection goes back to the pool.
	defer func() { _, _ = tx.Exec(ctx, `DEALLOCATE node_sessions_load`) }()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, `EXPLAIN EXECUTE node_sessions_load('server')`)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "node_sessions_started") || strings.Contains(plan, "node_sessions_pkey") {
			t.Fatalf("%s: NodeSessions does not use node_sessions_started:\n%s", mode, plan)
		}
	}
}

// A session without its node_model settings row still loads, with the model fields empty.
func TestNodeSessionsModelRow(t *testing.T) {
	d, ctx := open(t), context.Background()
	with := NodeSession{Launcher: "claude", Cwd: "/x", Model: "m", ModelFingerprint: "fp", ModelEffort: "high", AppMode: "plan", AppEffort: "low"}
	if err := d.PutNodeSession(ctx, "server", "with", with); err != nil {
		t.Fatal(err)
	}
	if err := d.PutNodeSession(ctx, "server", "without", NodeSession{Launcher: "claude", Model: "gone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM settings WHERE key = 'node_model.server.without'`); err != nil {
		t.Fatal(err)
	}
	got, err := d.NodeSessions(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	w := got["with"]
	if w.Model != "m" || w.ModelFingerprint != "fp" || w.ModelEffort != "high" || w.AppMode != "plan" || w.AppEffort != "low" || w.Launcher != "claude" {
		t.Fatalf("with = %+v", w)
	}
	wo, ok := got["without"]
	if !ok || wo.Launcher != "claude" || wo.Model != "" || wo.ModelFingerprint != "" || wo.ModelEffort != "" || wo.AppMode != "" || wo.AppEffort != "" {
		t.Fatalf("without = %+v, %v", wo, ok)
	}
}

// NodeSessions reaches each session's node_model row through settings_pkey, planned for its machine and as the
// generic plan a cached statement may switch to. settings keeps a row for every session ever started, so a plan that
// joins on the computed key and hashes the whole table instead must not come back.
func TestNodeSessionsSettingsLookup(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	// Ruling out nested loops leaves a join only a hash or merge over all of settings, which a per-row lookup is not.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off; SET LOCAL enable_nestloop = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `PREPARE node_sessions_q(text) AS `+nodeSessionsSQL); err != nil {
		t.Fatal(err)
	}
	// A prepared statement outlives the transaction; the connection goes back to the pool.
	defer func() { _, _ = tx.Exec(ctx, `DEALLOCATE node_sessions_q`) }()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, `EXPLAIN EXECUTE node_sessions_q('server')`)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, "using settings_pkey on settings") || !strings.Contains(plan, "Index Cond: (key = ") ||
			strings.Contains(plan, "Seq Scan on settings") || strings.Contains(plan, "Join") {
			t.Fatalf("%s: NodeSessions does not look settings up by key:\n%s", mode, plan)
		}
	}
}

func TestMailAccountsReplace(t *testing.T) {
	d, ctx := open(t), context.Background()
	rows := []MailAccountRow{{ID: "1", Email: "a@x", Data: json.RawMessage(`{"name":"A"}`), PasswordEnc: "enc"}, {ID: "2", Email: "b@x", Data: json.RawMessage(`{}`)}}
	if err := d.ReplaceMailAccounts(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceMailAccounts(ctx, rows[1:]); err != nil {
		t.Fatal(err)
	}
	got, err := d.MailAccounts(ctx)
	if err != nil || len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("accounts = %+v, %v", got, err)
	}
}

func TestInteractions(t *testing.T) {
	d, ctx := open(t), context.Background()
	ix := proto.Interaction{ID: "ix1", MachineID: "server", SessionID: "s", Kind: proto.IxQuestion, CreatedAt: time.Now()}
	_ = d.InsertInteraction(ctx, ix)
	_ = d.InsertInteraction(ctx, proto.Interaction{ID: "ix2", MachineID: "server", SessionID: "s", Kind: proto.IxPlan, CreatedAt: time.Now()})
	yes := true
	if err := d.FinishInteraction(ctx, "ix1", "resolved", &proto.Resolution{Approve: &yes}); err != nil {
		t.Fatal(err)
	}
	_ = d.FinishInteraction(ctx, "ix1", "expired", nil) // first outcome wins
	// A hook may still claim a resumable one after a restart, unless it expired.
	_ = d.InsertInteraction(ctx, proto.Interaction{ID: "ix3", MachineID: "server", SessionID: "s", Kind: proto.IxPermission,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Resumable: true})
	_ = d.InsertInteraction(ctx, proto.Interaction{ID: "ix4", MachineID: "server", SessionID: "s", Kind: proto.IxPermission,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(-time.Minute), Resumable: true})
	// The manager's card about a turn stays pending: it puts it back up from session_work.
	_, _ = d.pool.Exec(ctx, `TRUNCATE session_work`)
	_ = d.InsertInteraction(ctx, proto.Interaction{ID: "work_1", MachineID: "server", SessionID: "s", Kind: proto.IxQuestion, CreatedAt: time.Now()})
	if err := d.PutSessionWork(ctx, SessionWork{MachineID: "server", SessionID: "s", TurnAt: time.Now().UTC(), InteractionID: "work_1",
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, InteractionID: "work_1"}}); err != nil {
		t.Fatal(err)
	}
	kept, err := d.ExpireStaleInteractions(ctx)
	if err != nil || len(kept) != 1 || kept[0].ID != "ix3" || !kept[0].Resumable {
		t.Fatalf("kept %+v, %v", kept, err)
	}
	recs, err := d.Interactions(ctx, "server", "s", 10)
	if err != nil || len(recs) != 5 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	status := map[string]string{}
	for _, r := range recs {
		status[r.Interaction.ID] = r.Status
	}
	if status["ix1"] != "resolved" || status["ix2"] != "cancelled" || status["ix3"] != "pending" || status["ix4"] != "cancelled" ||
		status["work_1"] != "pending" {
		t.Fatalf("status = %v", status)
	}
	if st, res, err := d.InteractionOutcome(ctx, "ix1"); err != nil || st != "resolved" || res == nil || res.Approve == nil || !*res.Approve {
		t.Fatalf("outcome ix1 %q %+v %v", st, res, err)
	}
	if st, res, err := d.InteractionOutcome(ctx, "nope"); err != nil || st != "" || res != nil {
		t.Fatalf("outcome of a missing one %q %+v %v", st, res, err)
	}
	if rec, err := d.InteractionRecord(ctx, "ix1"); err != nil || rec == nil || rec.Interaction.ID != "ix1" || rec.Status != "resolved" ||
		rec.Resolution == nil || rec.ResolvedAt == nil {
		t.Fatalf("record ix1 %+v %v", rec, err)
	}
	if rec, err := d.InteractionRecord(ctx, "ix3"); err != nil || rec == nil || rec.Status != "pending" || rec.Resolution != nil {
		t.Fatalf("record ix3 %+v %v", rec, err)
	}
	if rec, err := d.InteractionRecord(ctx, "nope"); err != nil || rec != nil {
		t.Fatalf("record of a missing one %+v %v", rec, err)
	}
}

func TestLoopInteractionRestart(t *testing.T) {
	d, ctx := open(t), context.Background()
	for _, id := range []string{"tool_loop_kept", "tool_loop_orphan", "tool_loop_expiring"} {
		if err := d.InsertInteraction(ctx, proto.Interaction{ID: id, MachineID: "box", SessionID: "session", Kind: proto.IxQuestion, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PutSetting(ctx, "agents.tool_loops", []any{map[string]any{"card": map[string]any{"id": "tool_loop_kept"}}, map[string]any{"expired_id": "tool_loop_expiring"}, map[string]any{"silenced": true}}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		var value any
		_, _ = d.TakeSetting(ctx, "agents.tool_loops", &value)
	}()
	if _, err := d.ExpireStaleInteractions(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"tool_loop_kept": "pending", "tool_loop_expiring": "pending", "tool_loop_orphan": "cancelled"} {
		if status, _, err := d.InteractionOutcome(ctx, id); err != nil || status != want {
			t.Fatalf("%s status = %s, %v; want %s", id, status, err, want)
		}
	}
	if err := d.FinishInteraction(ctx, "tool_loop_expiring", "expired", nil); err != nil {
		t.Fatal(err)
	}
	if status, _, err := d.InteractionOutcome(ctx, "tool_loop_expiring"); err != nil || status != "expired" {
		t.Fatalf("restored expiry status = %s, %v", status, err)
	}
}

// The startup lookup of pending interactions can use the partial index instead of scanning every past interaction.
func TestPendingInteractionsIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sql  string
		args []any
	}{
		{resumablePendingSQL, nil},
		{cancelPendingSQL, []any{[]string{}}},
	} {
		q := c.sql
		rows, err := tx.Query(ctx, `EXPLAIN `+q, c.args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "interactions_pending") {
			t.Fatalf("%s\nplan:\n%s", q, plan)
		}
	}
}

// Each combination of filters returns the same rows the single OR statement did: newest first, limited, and a
// session alone matches that session id on every machine.
func TestInteractionsFilters(t *testing.T) {
	d, ctx := open(t), context.Background()
	base := time.Now().Add(-time.Hour).UTC()
	type row struct{ id, machine, session string }
	var all []row // newest first
	for i := 11; i >= 0; i-- {
		r := row{fmt.Sprintf("ix%02d", i), []string{"m1", "m2"}[i%2], []string{"s1", "s2", "s3"}[i%3]}
		all = append(all, r)
		if err := d.InsertInteraction(ctx, proto.Interaction{ID: r.id, MachineID: r.machine, SessionID: r.session,
			Kind: proto.IxQuestion, CreatedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		machine, session string
		limit            int
	}{{"", "", 0}, {"", "", 3}, {"m1", "", 0}, {"m2", "", 2}, {"", "s1", 0}, {"m1", "s1", 0}, {"m2", "s1", 1}, {"m1", "nope", 0}} {
		var want []string
		for _, r := range all {
			if (c.machine == "" || r.machine == c.machine) && (c.session == "" || r.session == c.session) &&
				(c.limit == 0 || len(want) < c.limit) {
				want = append(want, r.id)
			}
		}
		// Past five runs, so a cached statement is on its generic plan by the last ones.
		for range 7 {
			recs, err := d.Interactions(ctx, c.machine, c.session, c.limit)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(recs))
			for i, r := range recs {
				got[i] = r.Interaction.ID
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("Interactions(%q, %q, %d) = %v, want %v", c.machine, c.session, c.limit, got, want)
			}
		}
	}
}

// The cached history statements stay on an index under a generic plan too, which Postgres switches a cached
// statement to: neither scans the table, and machine+session does not walk interactions_created past the rows it skips.
func TestInteractionsHistoryIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `INSERT INTO interactions (id, machine_id, session_id, kind, payload, created_at)
		SELECT 'ix' || g, 'm' || (g % 20), 's' || (g % 600), 'question', '{}', now() - g * interval '1 minute'
		FROM generate_series(1, 6000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `ANALYZE interactions`); err != nil {
		t.Fatal(err)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, sql, types, args, index string
	}{
		{"ix_history", interactionsSQL, "int", "100", "interactions_created"},
		{"ix_history_machine_session", interactionsMachineSessionSQL, "text, text, int", "'m7', 's7', 100", "interactions_session"},
	} {
		if _, err := tx.Exec(ctx, `PREPARE `+c.name+`(`+c.types+`) AS `+c.sql); err != nil {
			t.Fatal(err)
		}
		// A prepared statement outlives the transaction; the connection goes back to the pool.
		defer func() { _, _ = d.pool.Exec(ctx, `DEALLOCATE `+c.name) }()
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = `+mode); err != nil {
				t.Fatal(err)
			}
			// EXPLAIN EXECUTE takes no bind parameters, so the values are written in.
			rows, err := tx.Query(ctx, `EXPLAIN EXECUTE `+c.name+`(`+c.args+`)`)
			if err != nil {
				t.Fatal(err)
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			walks := strings.Contains(plan, "interactions_created") && c.index != "interactions_created"
			if !strings.Contains(plan, c.index) || strings.Contains(plan, "Seq Scan") || walks {
				t.Fatalf("%s %s: want %s\nplan:\n%s", c.name, mode, c.index, plan)
			}
		}
	}
}

// The machine-only and session-only history statements go as unnamed statements, planned for each call with their
// values: a cached one would settle on a generic plan that walks every row for a quiet machine or session.
func TestInteractionsNotCached(t *testing.T) {
	d, ctx := open(t), context.Background()
	cfg, err := pgxpool.ParseConfig(os.Getenv("PERSONA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1 // one connection, so pg_prepared_statements below sees the statements Interactions used
	one, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	d = &DB{pool: one}
	for range 7 {
		if _, err := d.Interactions(ctx, "m1", "", 10); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Interactions(ctx, "", "s1", 10); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Interactions(ctx, "m1", "s1", 10); err != nil {
			t.Fatal(err)
		}
	}
	var alone, both int
	if err := one.QueryRow(ctx, `SELECT count(*) FILTER (WHERE statement IN ($1, $2)), count(*) FILTER (WHERE statement = $3)
		FROM pg_prepared_statements`, interactionsMachineSQL, interactionsSessionSQL, interactionsMachineSessionSQL).Scan(&alone, &both); err != nil {
		t.Fatal(err)
	}
	// machine+session is cached as usual, which shows the lookup sees the statements of this connection.
	if alone != 0 || both != 1 {
		t.Fatalf("prepared: machine or session alone %d (want 0), machine+session %d (want 1)", alone, both)
	}
}

func TestPendingClientOpsIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN `+recoverClientOpsSQL)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "client_ops_pending") {
		t.Fatalf("%s\nplan:\n%s", recoverClientOpsSQL, plan)
	}
}

func TestSettings(t *testing.T) {
	d, ctx := open(t), context.Background()
	var v struct{ Launcher string }
	if ok, err := d.Setting(ctx, "assistant", &v); ok || err != nil {
		t.Fatalf("unset setting: %v %v", ok, err)
	}
	_ = d.PutSetting(ctx, "assistant", map[string]string{"Launcher": "cc_max"})
	if ok, err := d.Setting(ctx, "assistant", &v); !ok || err != nil || v.Launcher != "cc_max" {
		t.Fatalf("setting = %+v %v %v", v, ok, err)
	}
}

func TestSessionViews(t *testing.T) {
	d, ctx := open(t), context.Background()
	_, _ = d.pool.Exec(ctx, `TRUNCATE session_views`)
	t1 := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	_ = d.MarkSeen(ctx, "server", "s", t1)
	_ = d.MarkSeen(ctx, "server", "s", t1.Add(-time.Hour)) // older view never moves it back
	got, err := d.SeenTimes(ctx)
	if err != nil || !got[[2]string{"server", "s"}].Equal(t1) {
		t.Fatalf("seen = %v, %v", got, err)
	}
}

// The 30-day seen load uses session_views_seen instead of scanning every session ever viewed.
func TestSeenTimesIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN `+seenTimesSQL)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "session_views_seen") {
		t.Fatalf("SeenTimes does not use session_views_seen:\n%s", plan)
	}
}

func TestInboxItems(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	if err := d.PutInboxItem(ctx, "a", []byte(`{"id":"a"}`), now, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.PutInboxItem(ctx, "a", []byte(`{"id":"a","count":2}`), now, &now); err != nil { // update
		t.Fatal(err)
	}
	if err := d.PutInboxItem(ctx, "old", []byte(`{"id":"old"}`), old, &old); err != nil {
		t.Fatal(err)
	}
	rows, err := d.InboxItems(ctx, now.Add(-8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the old done item left out", len(rows))
	}
	var it struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rows[0], &it); err != nil || it.Count != 2 {
		t.Fatalf("row = %s", rows[0])
	}
	// Deleted for good: a source taken back.
	_ = d.PutInboxItem(ctx, "imessage:1", []byte(`{"id":"imessage:1","source":"imessage"}`), now, nil)
	if gone, err := d.DeleteInboxSource(ctx, "imessage", nil); err != nil || len(gone) != 1 || gone[0] != "imessage:1" {
		t.Fatalf("delete the source: %v %v", gone, err)
	}
	if rows, _ := d.InboxItems(ctx, now.Add(-8*24*time.Hour)); len(rows) != 1 {
		t.Fatalf("rows after delete = %d", len(rows))
	}
}

func TestDeviceTokens(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE device_tokens`); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateDeviceToken(ctx, DeviceToken{ID: "dt1", Name: "iPhone", Kind: "phone", Role: "full"}, "h1"); err != nil {
		t.Fatal(err)
	}
	_ = d.CreateDeviceToken(ctx, DeviceToken{ID: "dt2", Name: "Watch", Kind: "watch", Role: "answer"}, "h2")
	got, err := d.DeviceTokenByHash(ctx, "h2")
	if err != nil || got == nil || got.ID != "dt2" || got.Role != "answer" {
		t.Fatalf("%+v %v", got, err)
	}
	_ = d.TouchDeviceToken(ctx, "dt2")
	if ok, err := d.RevokeDeviceToken(ctx, "dt2"); !ok || err != nil {
		t.Fatalf("revoke %v %v", ok, err)
	}
	if ok, _ := d.RevokeDeviceToken(ctx, "dt2"); ok {
		t.Fatal("revoked twice")
	}
	if got, _ := d.DeviceTokenByHash(ctx, "h2"); got != nil {
		t.Fatal("a revoked token still works")
	}
	list, _ := d.DeviceTokens(ctx)
	if len(list) != 2 || list[0].ID != "dt1" || list[1].RevokedAt == nil || list[1].LastSeen == nil {
		t.Fatalf("%+v", list)
	}
}

func TestKBEntities(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	_ = d.PutKBEntity(ctx, "projects", "a", "A", "P2", 50, []string{"发布"}, "", now)
	_ = d.PutKBEntity(ctx, "projects", "b", "B", "P0", 10, nil, "", now)
	_ = d.PutKBEntity(ctx, "chats", "c", "C", "", 90, []string{"发布"}, "x", time.Time{})
	if err := d.PutKBEntity(ctx, "projects", "a", "A2", "P1", 60, []string{"发布", "后端"}, "y", now); err != nil {
		t.Fatal(err)
	}
	list, err := d.KBEntities(ctx, "projects", "", 10)
	if err != nil || len(list) != 2 || list[0].ID != "b" || list[1].Name != "A2" || list[1].Importance != "P1" {
		t.Fatalf("%+v %v", list, err)
	}
	if list, _ := d.KBEntities(ctx, "", "发布", 10); len(list) != 2 {
		t.Fatalf("tag filter: %+v", list)
	}
	// A source taken back: only its rows of that kind go.
	_ = d.PutKBEntity(ctx, "chats", "imessage-0123456789ab", "张三", "", 10, nil, "", now)
	_ = d.PutKBEntity(ctx, "people", "imessage-0123456789ab", "张三", "", 10, nil, "", now)
	if err := d.DeleteKBEntities(ctx, "chats", []string{"imessage-0123456789ab", "nope"}); err != nil {
		t.Fatal(err)
	}
	chats, _ := d.KBEntities(ctx, "chats", "", 10)
	people, _ := d.KBEntities(ctx, "people", "", 10)
	if len(chats) != 1 || chats[0].ID != "c" || len(people) == 0 {
		t.Fatalf("after delete: chats %+v people %+v", chats, people)
	}
	if err := d.DeleteKBEntities(ctx, "chats", nil); err != nil {
		t.Fatal(err)
	}
}

func TestMachineMetrics(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.PutMachineSystem(ctx, "m1", proto.System{OS: "darwin", Distro: "macos", Cores: 8}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutMachineSystem(ctx, "m1", proto.System{OS: "darwin", Distro: "macos", Cores: 10}); err != nil {
		t.Fatal(err)
	}
	if sys, err := d.MachineSystems(ctx); err != nil || sys["m1"].Cores != 10 {
		t.Fatalf("systems = %+v, %v", sys, err)
	}

	// Two hours of samples every 10 s, CPU = the minute's index (so a minute's average is exact), with a 10-minute
	// hole and a sample without CPU.
	base := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	for i := 0; i < 720; i++ {
		ts := base.Add(time.Duration(i) * 10 * time.Second)
		if ts.Sub(base) >= 30*time.Minute && ts.Sub(base) < 40*time.Minute {
			continue
		}
		s := proto.MetricSample{T: ts.Unix(), CPU: f(float64(i / 6)), Load: f(1), MemUsed: 100, MemTotal: 1000,
			NetRx: f(float64(10 * (i % 6))), DiskUsed: 5, DiskTotal: 50}
		if i == 6 {
			s.CPU = nil
		}
		if err := d.InsertMetric(ctx, "m1", s); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.InsertMetric(ctx, "m2", proto.MetricSample{T: base.Unix(), CPU: f(99), MemTotal: 1})

	// Roll up everything before 1:00:30 → only whole minutes before 1:00 move: 60 minutes minus the hole.
	n, err := d.RollupMetrics(ctx, base.Add(time.Hour+30*time.Second))
	if err != nil || n != 51 { // 50 minutes of m1, 1 of m2
		t.Fatalf("rolled %d, %v", n, err)
	}
	if n, _ := d.RollupMetrics(ctx, base.Add(time.Hour+30*time.Second)); n != 0 {
		t.Fatalf("rolled again: %d", n)
	}
	var raw, rolled int
	_ = d.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE step = 10), count(*) FILTER (WHERE step = 60) FROM machine_metrics WHERE machine_id = 'm1'`).Scan(&raw, &rolled)
	if raw != 360 || rolled != 50 {
		t.Fatalf("raw %d rolled %d", raw, rolled)
	}

	// By minute over the whole two hours: rolled-up and raw minutes read the same.
	series, err := d.MetricSeries(ctx, []string{"m1"}, base, base.Add(2*time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	m1 := series["m1"]
	if len(m1) != 110 || len(series) != 1 {
		t.Fatalf("%d points, %d machines", len(m1), len(series))
	}
	if *m1[0].CPU != 0 || *m1[5].CPU != 5 || *m1[100].CPU != 110 || *m1[0].Rx != 25 || *m1[0].Mem != 100 {
		t.Fatalf("points %+v %+v %+v", m1[0], m1[5], m1[100])
	}
	if m1[1].T-m1[0].T != 60 || m1[30].T != base.Add(40*time.Minute).Unix() {
		t.Fatalf("times %d %d (the hole has no points)", m1[0].T, m1[30].T)
	}
	if *m1[1].CPU != 1 { // minute 1 had one sample without CPU: the average of the other five
		t.Fatalf("minute 1 cpu = %v", *m1[1].CPU)
	}
	// nil means every machine; a list leaves the others out, and an empty one matches none.
	all, err := d.MetricSeries(ctx, nil, base, base.Add(2*time.Hour), 60)
	if err != nil || len(all) != 2 || len(all["m1"]) != 110 || len(all["m2"]) != 1 || *all["m2"][0].CPU != 99 {
		t.Fatalf("all machines: %d machines, m1 %d points, m2 %+v, %v", len(all), len(all["m1"]), all["m2"], err)
	}
	only, err := d.MetricSeries(ctx, []string{"m2", "m9"}, base, base.Add(2*time.Hour), 60)
	if err != nil || len(only) != 1 || len(only["m2"]) != 1 {
		t.Fatalf("m2 only: %+v, %v", only, err)
	}
	if none, err := d.MetricSeries(ctx, []string{}, base, base.Add(2*time.Hour), 60); err != nil || len(none) != 0 {
		t.Fatalf("no machines: %+v, %v", none, err)
	}
	// Raw over the last hour: one point a sample.
	series, _ = d.MetricSeries(ctx, nil, base.Add(time.Hour), base.Add(2*time.Hour), 10)
	if len(series["m1"]) != 360 {
		t.Fatalf("raw points %d", len(series["m1"]))
	}

	if n, err := d.PruneMetrics(ctx, base.Add(30*time.Minute)); err != nil || n != 31 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

// The per-minute rollup finds the raw rows through machine_metrics_raw instead of scanning all 24 hours of rows, both
// planned for its values and as the generic plan a cached statement may switch to.
func TestRollupMetricsIndexed(t *testing.T) {
	// The partial index and the SQL write the raw step out; they must match RawMetricStep.
	if RawMetricStep != 10 || !strings.Contains(rollupMetricsSQL, "step = 10 ") {
		t.Fatalf("RawMetricStep %d: update rollupMetricsSQL and the machine_metrics_raw index", RawMetricStep)
	}
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `PREPARE rollup_metrics(bigint, smallint) AS `+rollupMetricsSQL); err != nil {
		t.Fatal(err)
	}
	// A prepared statement outlives the transaction; the connection goes back to the pool.
	defer func() { _, _ = tx.Exec(ctx, `DEALLOCATE rollup_metrics`) }()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		// EXPLAIN EXECUTE takes no bind parameters, so the two integers are written in.
		rows, err := tx.Query(ctx, fmt.Sprintf(`EXPLAIN EXECUTE rollup_metrics(%d, %d)`, time.Now().Add(-time.Hour).Unix(), RolledMetricStep))
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "machine_metrics_raw") {
			t.Fatalf("%s: RollupMetrics does not use machine_metrics_raw:\n%s", mode, plan)
		}
	}
}

// A machine's chart seeks machine_metrics_pkey by machine and time, both planned for its values and as the generic
// plan a cached statement switches to, instead of reading every machine's rows in the range and filtering them.
func TestMetricSeriesIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	// Ten machines with two hours of raw samples each.
	if _, err := d.pool.Exec(ctx, `INSERT INTO machine_metrics (machine_id, ts, step, cpu, load, mem_used, mem_total,
			swap_used, swap_total, net_rx, net_tx, disk_used, disk_total)
		SELECT 'm' || m, now() - g * interval '10 seconds', 10, 1, 1, 1, 2, 0, 0, 1, 1, 1, 2
		FROM generate_series(1, 10) m, generate_series(0, 719) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `ANALYZE machine_metrics`); err != nil {
		t.Fatal(err)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `PREPARE metric_series_machines(bigint, bigint, int, text[]) AS `+metricSeriesMachinesSQL); err != nil {
		t.Fatal(err)
	}
	// A prepared statement outlives the transaction; the connection goes back to the pool.
	defer func() { _, _ = d.pool.Exec(ctx, `DEALLOCATE metric_series_machines`) }()
	now := time.Now()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		// EXPLAIN EXECUTE takes no bind parameters, so the values are written in.
		rows, err := tx.Query(ctx, fmt.Sprintf(`EXPLAIN EXECUTE metric_series_machines(%d, %d, 60, '{m3}')`,
			now.Add(-time.Hour).Unix(), now.Add(time.Minute).Unix()))
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		seeks := false
		for _, l := range lines {
			if strings.Contains(l, "Index Cond") && strings.Contains(l, "machine_id") {
				seeks = true
			}
		}
		if !seeks || !strings.Contains(plan, "machine_metrics_pkey") || strings.Contains(plan, "Seq Scan") {
			t.Fatalf("%s: MetricSeries does not seek machine_metrics_pkey by machine:\n%s", mode, plan)
		}
	}
}

func TestPhoneToolsDevicePersistence(t *testing.T) {
	d, ctx := open(t), context.Background()
	for _, id := range []string{"credential-ios", "credential-android"} {
		if err := d.CreateDeviceToken(ctx, DeviceToken{ID: id, Name: "Device", Kind: "phone", Role: "full"}, "hash-"+id); err != nil {
			t.Fatal(err)
		}
	}
	iphone := PhoneToolRegistration{DeviceID: "ios-device", Platform: "ios", CredentialID: "credential-ios", Tools: []proto.Tool{{Name: "phone_reminders_list"}, {Name: "phone_device_info"}}}
	android := PhoneToolRegistration{DeviceID: "android-device", Platform: "android", CredentialID: "credential-android", Tools: []proto.Tool{{Name: "phone_notifications_list"}}}
	for _, r := range []PhoneToolRegistration{iphone, android} {
		if err := d.ReplacePhoneTools(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	iphone.Tools = iphone.Tools[:1]
	if err := d.ReplacePhoneTools(ctx, iphone); err != nil {
		t.Fatal(err)
	}
	got, err := d.PhoneTools(ctx)
	if err != nil || len(got) != 2 || len(got[0].Tools) != 1 || len(got[1].Tools) != 1 || got[0].Platform != "android" || got[1].CredentialID != iphone.CredentialID {
		t.Fatalf("registrations = %+v, %v", got, err)
	}
	if err := d.UpsertDevice(ctx, Device{DeviceID: "push-ios", CredentialID: iphone.CredentialID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.RevokeDeviceToken(ctx, iphone.CredentialID); err != nil || !ok {
		t.Fatalf("revoke = %v, %v", ok, err)
	}
	got, err = d.PhoneTools(ctx)
	if err != nil || len(got) != 1 || got[0].DeviceID != android.DeviceID {
		t.Fatalf("registrations after revoke = %+v, %v", got, err)
	}
	if devices, err := d.Devices(ctx); err != nil || len(devices) != 0 {
		t.Fatalf("push registrations after revoke = %+v, %v", devices, err)
	}
	if err := d.ReplacePhoneTools(ctx, iphone); err == nil {
		t.Fatal("revoked device registered tools again")
	}
	android.Tools = nil
	if err := d.ReplacePhoneTools(ctx, android); err != nil {
		t.Fatal(err)
	}
	if got, err := d.PhoneTools(ctx); err != nil || len(got) != 0 {
		t.Fatalf("registrations after empty update = %+v, %v", got, err)
	}
}

func TestSentMessages(t *testing.T) {
	d, ctx := open(t), context.Background()
	if st, err := d.ClaimSend(ctx, "m1", "box", "s1"); err != nil || st != "" {
		t.Fatalf("new id: %q %v", st, err)
	}
	if st, _ := d.ClaimSend(ctx, "m1", "box", "s1"); st != "sending" {
		t.Fatalf("claimed again while sending: %q", st)
	}
	if err := d.SettleSend(ctx, "m1", "delivered"); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.ClaimSend(ctx, "m1", "box", "s1"); st != "delivered" {
		t.Fatalf("after delivery: %q", st)
	}
	_ = d.SettleSend(ctx, "m1", "") // refused: forgotten, the retry sends
	if st, _ := d.ClaimSend(ctx, "m1", "box", "s1"); st != "" {
		t.Fatalf("after forgetting: %q", st)
	}
	_, _ = d.pool.Exec(ctx, `UPDATE sent_messages SET created_at = now() - interval '2 days'`)
	if st, _ := d.ClaimSend(ctx, "m1", "box", "s1"); st != "sending" {
		t.Fatalf("an old unacknowledged operation must not execute again: %q", st)
	}
}

// Where a sent message landed (node/echo.go): kept with the send, a found line not replaced by a late "expected"
// record, handed back per machine for a day.
func TestSentEcho(t *testing.T) {
	d, ctx := open(t), context.Background()
	for _, id := range []string{"m1", "m2", "m3"} {
		if _, err := d.ClaimSend(ctx, id, "box", "s1"); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = d.ClaimSend(ctx, "o1", "other", "s9")
	expected := json.RawMessage(`{"session_id":"s1","client_msg_id":"m1","text":"hi","after_seq":3}`)
	found := json.RawMessage(`{"session_id":"s1","client_msg_id":"m1","text":"hi","after_seq":3,"line":"u1"}`)
	for _, e := range []json.RawMessage{expected, found, expected} {
		if err := d.RecordEcho(ctx, "m1", "box", e); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.RecordEcho(ctx, "m2", "box", json.RawMessage(`{"client_msg_id":"m2"}`))
	_ = d.RecordEcho(ctx, "o1", "box", json.RawMessage(`{"client_msg_id":"o1"}`)) // another machine's send
	_ = d.RecordEcho(ctx, "nope", "box", json.RawMessage(`{"client_msg_id":"nope"}`))
	got, err := d.Echoes(ctx, "box")
	if err != nil || len(got) != 2 {
		t.Fatalf("echoes %s %v", got, err)
	}
	var first struct {
		Line string `json:"line"`
	}
	_ = json.Unmarshal(got[0], &first)
	if first.Line != "u1" {
		t.Fatalf("the found line was replaced: %s", got[0])
	}
	_, _ = d.pool.Exec(ctx, `UPDATE sent_messages SET created_at = now() - interval '2 days' WHERE client_msg_id = 'm2'`)
	if got, _ := d.Echoes(ctx, "box"); len(got) != 1 {
		t.Fatalf("a send older than a day was handed back: %s", got)
	}
}

// A connector's secret: stored sealed by the hub, replaced, read back, deleted; another connector has none.
func TestConnectorSecrets(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, ok, err := d.ConnectorSecret(ctx, "example"); ok || err != nil {
		t.Fatalf("none yet: %v %v", ok, err)
	}
	for _, v := range []string{"sealed-1", "sealed-2"} {
		if err := d.PutConnectorSecret(ctx, "example", v); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok, err := d.ConnectorSecret(ctx, "example"); !ok || err != nil || v != "sealed-2" {
		t.Fatalf("got %q %v %v", v, ok, err)
	}
	if _, ok, _ := d.ConnectorSecret(ctx, "other"); ok {
		t.Fatal("another connector has it")
	}
	if err := d.DeleteConnectorSecret(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.ConnectorSecret(ctx, "example"); ok {
		t.Fatal("still there after delete")
	}
}
