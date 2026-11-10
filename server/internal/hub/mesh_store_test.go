package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// meshStore is an in-memory Store with the peers table (peerStore), for hubs in the mesh tests: everything a hub
// keeps, as maps.
type meshStore struct {
	mu         sync.Mutex
	settings   map[string][]byte
	tokens     map[string]db.DeviceToken // hash -> token
	revoked    map[string]bool
	applyFails int // ApplyRevokedTokens fails this many times more
	devices    map[string]db.Device
	ixs        map[string]*db.InteractionRecord
	seen       map[[2]string]time.Time
	nodeSess   map[string]map[string]db.NodeSession
	peers      map[string]db.Peer
	codes      map[string]time.Time // hash -> expiry
	usedCodes  map[string]bool
	group      *meshGroup
	connectors map[string]db.ConnectorCredential
}

func newMeshStore() *meshStore {
	return &meshStore{settings: map[string][]byte{}, tokens: map[string]db.DeviceToken{}, revoked: map[string]bool{},
		devices: map[string]db.Device{}, ixs: map[string]*db.InteractionRecord{}, seen: map[[2]string]time.Time{},
		nodeSess: map[string]map[string]db.NodeSession{}, peers: map[string]db.Peer{}, codes: map[string]time.Time{}, usedCodes: map[string]bool{}}
}

var _ Store = (*meshStore)(nil)
var _ peerStore = (*meshStore)(nil)

func (s *meshStore) CreateLockTest(context.Context, db.LockTest) error              { return nil }
func (s *meshStore) LockTest(context.Context, string, string) (*db.LockTest, error) { return nil, nil }
func (s *meshStore) DueLockTests(context.Context, time.Time) ([]db.LockTest, error) { return nil, nil }
func (s *meshStore) NextLockTestDue(context.Context) (*time.Time, error)            { return nil, nil }
func (s *meshStore) AttemptLockTest(context.Context, string, int64, time.Time) (bool, error) {
	return false, nil
}
func (s *meshStore) ReceiveLockTest(context.Context, string, string, *bool, time.Time) (*db.LockTest, error) {
	return nil, nil
}
func (s *meshStore) RecoverLockTests(context.Context, time.Time) error { return nil }
func (s *meshStore) PruneLockTests(context.Context, time.Time) error   { return nil }
func (s *meshStore) InsertDeviceSession(context.Context, db.DeviceSession) (int64, error) {
	return 0, nil
}
func (s *meshStore) UpdateDeviceSessionHeartbeat(context.Context, int64, int) error { return nil }
func (s *meshStore) FinishDeviceSession(context.Context, int64, time.Time, string, int) error {
	return nil
}
func (s *meshStore) StopDeviceSessions(context.Context, time.Time) error  { return nil }
func (s *meshStore) PruneDeviceSessions(context.Context, time.Time) error { return nil }
func (s *meshStore) DeviceSessions(context.Context, string, time.Time, time.Time) (db.DeviceSessionHistory, error) {
	return db.DeviceSessionHistory{}, nil
}
func (s *meshStore) BeginClientOp(_ context.Context, op db.ClientOp) (*db.ClientOp, bool, error) {
	return &op, true, nil
}
func (s *meshStore) ClientOp(context.Context, string) (*db.ClientOp, error) { return nil, nil }
func (s *meshStore) FinishClientOp(context.Context, string, string, json.RawMessage) error {
	return nil
}
func (s *meshStore) RecoverClientOps(context.Context) error                            { return nil }
func (s *meshStore) CleanupClientOps(context.Context, time.Time) error                 { return nil }
func (s *meshStore) ReplacePhoneTools(context.Context, db.PhoneToolRegistration) error { return nil }
func (s *meshStore) PhoneTools(context.Context) ([]db.PhoneToolRegistration, error)    { return nil, nil }
func (s *meshStore) ClaimSend(context.Context, string, string, string) (string, error) {
	return "new", nil
}
func (s *meshStore) SettleSend(context.Context, string, string) error { return nil }

func (s *meshStore) UpsertDevice(_ context.Context, d db.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[d.DeviceID] = d
	return nil
}

func (s *meshStore) Devices(context.Context) ([]db.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.Device
	for _, d := range s.devices {
		out = append(out, d)
	}
	return out, nil
}

func (s *meshStore) DeleteDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.devices, id)
	return nil
}

func (s *meshStore) SetDevicePrefs(_ context.Context, id, credential string, prefs json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[id]
	d.DeviceID, d.Prefs = id, prefs
	if d.Credential == "" {
		d.Credential = credential
	}
	s.devices[id] = d
	return nil
}

func (s *meshStore) LiveActivities(context.Context) ([]db.LiveActivity, error) { return nil, nil }
func (s *meshStore) SaveLiveActivity(context.Context, db.LiveActivity) error   { return nil }
func (s *meshStore) ClaimLiveActivityStart(context.Context, string, string, time.Time, time.Duration, time.Duration) (time.Time, bool, error) {
	return time.Time{}, false, nil // no persisted activity in this non-push fixture
}

func (s *meshStore) InsertInteraction(_ context.Context, ix proto.Interaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ixs[ix.ID] == nil {
		s.ixs[ix.ID] = &db.InteractionRecord{Interaction: ix, Status: "pending"}
	}
	return nil
}

func (s *meshStore) FinishInteraction(_ context.Context, id, status string, res *proto.Resolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.ixs[id]; r != nil && r.Status == "pending" {
		r.Status, r.Resolution = status, res
	}
	return nil
}

func (s *meshStore) InteractionOutcome(_ context.Context, id string) (string, *proto.Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.ixs[id]; r != nil {
		return r.Status, r.Resolution, nil
	}
	return "", nil, nil
}

func (s *meshStore) Interactions(_ context.Context, machine, session string, _ int) ([]db.InteractionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []db.InteractionRecord{}
	for _, r := range s.ixs {
		if (machine == "" || r.Interaction.MachineID == machine) && (session == "" || r.Interaction.SessionID == session) {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *meshStore) AllowAlways(context.Context, string, string, []string) error { return nil }
func (s *meshStore) AlwaysAllowed(context.Context, string, []string) (bool, error) {
	return false, nil
}

func (s *meshStore) PutNodeSession(_ context.Context, machine, id string, ns db.NodeSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodeSess[machine] == nil {
		s.nodeSess[machine] = map[string]db.NodeSession{}
	}
	s.nodeSess[machine][id] = ns
	return nil
}

func (s *meshStore) NodeSessions(_ context.Context, machine string) (map[string]db.NodeSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]db.NodeSession{}
	for k, v := range s.nodeSess[machine] {
		out[k] = v
	}
	return out, nil
}

func (s *meshStore) MailAccounts(context.Context) ([]db.MailAccountRow, error)      { return nil, nil }
func (s *meshStore) ReplaceMailAccounts(context.Context, []db.MailAccountRow) error { return nil }

func (s *meshStore) MarkSeen(_ context.Context, machine, session string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[[2]string{machine, session}] = at
	return nil
}

func (s *meshStore) SeenTimes(context.Context) (map[[2]string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[[2]string]time.Time{}
	for k, v := range s.seen {
		out[k] = v
	}
	return out, nil
}

func (s *meshStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	b, ok := s.settings[key]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, out)
}

func (s *meshStore) TakeSetting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	b, ok := s.settings[key]
	delete(s.settings, key)
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, out)
}

func (s *meshStore) PutSetting(_ context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = b
	return nil
}

func (s *meshStore) CreateDeviceToken(_ context.Context, t db.DeviceToken, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.CreatedAt = time.Now()
	s.tokens[hash] = t
	return nil
}

func (s *meshStore) DeviceTokenByHash(_ context.Context, hash string) (*db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[hash]
	if !ok || s.revoked[t.ID] {
		return nil, nil
	}
	return &t, nil
}

func (s *meshStore) DeviceTokens(context.Context) ([]db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.DeviceToken
	for _, t := range s.tokens {
		out = append(out, t)
	}
	return out, nil
}

func (s *meshStore) RevokeDeviceToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.ID == id && !s.revoked[id] {
			s.revoked[id] = true
			return true, nil
		}
	}
	return false, nil
}

func (s *meshStore) RevokeMachineCredentials(_ context.Context, machine string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, t := range s.tokens {
		if t.MachineID == machine && t.Role == roleLink && !s.revoked[t.ID] {
			s.revoked[t.ID] = true
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

func (s *meshStore) TouchDeviceToken(context.Context, string) error { return nil }

func (s *meshStore) CreatePairToken(context.Context, string, time.Time, string) error { return nil }
func (s *meshStore) PairTokenByHash(context.Context, string) (*db.PairToken, error)   { return nil, nil }
func (s *meshStore) RedeemPairToken(context.Context, string, db.DeviceToken, string) (bool, error) {
	return false, nil
}
func (s *meshStore) PutInboxItem(context.Context, string, []byte, time.Time, *time.Time) error {
	return nil
}
func (s *meshStore) InboxItems(context.Context, time.Time, func([]byte) error) error { return nil }

func (s *meshStore) Peers(context.Context) ([]db.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.Peer
	for _, p := range s.peers {
		out = append(out, p)
	}
	return out, nil
}

func (s *meshStore) SavePeer(_ context.Context, p db.Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[p.HubID] = p
	return nil
}

func (s *meshStore) UpdatePeerSeen(_ context.Context, hubID, name string, endpoints []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[hubID]; ok {
		p.Name, p.Endpoints = name, endpoints
		s.peers[hubID] = p
	}
	return nil
}

func (s *meshStore) DeletePeer(_ context.Context, hubID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.peers[hubID]
	delete(s.peers, hubID)
	return ok, nil
}

func (s *meshStore) CreatePeerCode(_ context.Context, hash string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[hash] = expires
	return nil
}

func (s *meshStore) PeerCodeValid(_ context.Context, hash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.codes[hash]
	return ok && !s.usedCodes[hash] && time.Now().Before(exp), nil
}

func (s *meshStore) RedeemPeerCode(_ context.Context, codeHash string, p db.Peer, cred db.DeviceToken, credHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.codes[codeHash]
	if !ok || s.usedCodes[codeHash] || !time.Now().Before(exp) {
		return false, nil
	}
	s.usedCodes[codeHash] = true
	for _, t := range s.tokens {
		if t.Kind == "peer" && t.MachineID == p.HubID {
			s.revoked[t.ID] = true
		}
	}
	cred.CreatedAt = time.Now()
	s.tokens[credHash] = cred
	s.peers[p.HubID] = p
	return true, nil
}

func (s *meshStore) PrunePeerCodes(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.codes {
		if exp.Before(before) {
			delete(s.codes, k)
		}
	}
	return nil
}

var _ centerStore = (*meshStore)(nil)

func (s *meshStore) MeshGroup(context.Context, string) (string, int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.group == nil {
		return "", 0, false, nil
	}
	return s.group.Center, s.group.Term, true, nil
}

func (s *meshStore) SaveMeshGroup(_ context.Context, _, center string, term int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.group = &meshGroup{Center: center, Term: term}
	return nil
}

func (s *meshStore) AppTokens(context.Context) ([]db.ExportedToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.ExportedToken
	for hash, t := range s.tokens {
		if !s.revoked[t.ID] && (t.Role == roleFull || t.Role == roleAnswer) {
			out = append(out, db.ExportedToken{DeviceToken: t, Hash: hash})
		}
	}
	return out, nil
}

func (s *meshStore) RevokedAppTokens(context.Context) ([]db.ExportedToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.ExportedToken
	for hash, t := range s.tokens {
		if s.revoked[t.ID] && (t.Role == roleFull || t.Role == roleAnswer) {
			out = append(out, db.ExportedToken{DeviceToken: t, Hash: hash})
		}
	}
	return out, nil
}

func (s *meshStore) ApplyRevokedTokens(_ context.Context, list []db.ExportedToken) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applyFails > 0 {
		s.applyFails--
		return nil, errors.New("the database is having a moment")
	}
	var ids []string
	for _, e := range list {
		t, ok := s.tokens[e.Hash]
		switch {
		case !ok:
			s.tokens[e.Hash] = e.DeviceToken
			s.revoked[e.ID] = true
		case !s.revoked[t.ID] && (t.Role == roleFull || t.Role == roleAnswer):
			s.revoked[t.ID] = true
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

func (s *meshStore) ImportDeviceTokens(_ context.Context, list []db.ExportedToken) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range list {
		if _, ok := s.tokens[t.Hash]; ok {
			continue
		}
		dup := false
		for _, x := range s.tokens {
			if x.ID == t.ID {
				dup = true
			}
		}
		if !dup {
			s.tokens[t.Hash] = t.DeviceToken
			n++
		}
	}
	return n, nil
}

func (s *meshStore) ConnectorCredential(_ context.Context, machine string) (*db.ConnectorCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.connectors[machine]; ok {
		return &c, nil
	}
	return nil, nil
}

func (s *meshStore) SaveConnectorCredential(_ context.Context, c db.ConnectorCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connectors == nil {
		s.connectors = map[string]db.ConnectorCredential{}
	}
	s.connectors[c.MachineID] = c
	return nil
}

func (s *meshStore) DeleteConnectorCredential(_ context.Context, machine string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.connectors[machine]
	delete(s.connectors, machine)
	return ok, nil
}

// Connectors' secrets and todos: not used by the mesh tests.
func (s *meshStore) ConnectorSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (s *meshStore) PutConnectorSecret(context.Context, string, string) error     { return nil }
func (s *meshStore) DeleteConnectorSecret(context.Context, string) error          { return nil }
func (s *meshStore) ListTodos(context.Context) ([]db.TodoRow, error)              { return nil, nil }
func (s *meshStore) ListAllTodoSpaces(context.Context) ([]db.TodoSpaceRow, error) { return nil, nil }
func (s *meshStore) ListAllTodoAgents(context.Context) ([]db.TodoAgentRow, error) { return nil, nil }
func (s *meshStore) GetTodo(context.Context, string) (*db.TodoRow, error)         { return nil, nil }
func (s *meshStore) CreateTodo(context.Context, db.TodoRow) error                 { return nil }
func (s *meshStore) UpdateTodo(context.Context, string, *string, *string, *bool, time.Time) (*db.TodoRow, error) {
	return nil, nil
}
func (s *meshStore) FailTodo(context.Context, string, string, time.Time) (*db.TodoRow, error) {
	return nil, db.ErrTodoNotFound
}
func (s *meshStore) AutoDoneTodo(context.Context, string, string, time.Time, time.Time) (*db.TodoRow, bool, error) {
	return nil, false, db.ErrTodoNotFound
}
func (s *meshStore) RetryTodo(context.Context, string, time.Time) (*db.TodoRow, bool, error) {
	return nil, false, db.ErrTodoNotFound
}
func (s *meshStore) DeleteTodo(context.Context, string) error             { return nil }
func (s *meshStore) CreateTaskPlan(context.Context, db.TaskPlanRow) error { return nil }
func (s *meshStore) TaskPlan(context.Context, string) (*db.TaskPlanRow, error) {
	return nil, db.ErrTaskPlanNotFound
}
func (s *meshStore) TaskPlansByID(context.Context, []string) ([]db.TaskPlanRow, error) {
	return nil, nil
}
func (s *meshStore) TaskPlans(context.Context, string) ([]db.TaskPlanRow, error) { return nil, nil }
func (s *meshStore) UpdateTaskPlan(context.Context, string, string, string, json.RawMessage, time.Time) (bool, error) {
	return false, nil
}
func (s *meshStore) AppendFeed(context.Context, db.FeedRow) error { return nil }
func (s *meshStore) Feed(context.Context, string, time.Time, int) ([]db.FeedRow, error) {
	return nil, nil
}
func (s *meshStore) FeedItem(context.Context, string) (*db.FeedRow, error) { return nil, nil }
func (s *meshStore) MarkFeedSent(context.Context, string, string, time.Time, json.RawMessage) ([]db.FeedRow, error) {
	return nil, nil
}
func (s *meshStore) LinkTodoSpace(context.Context, string, string, string, time.Time) (string, error) {
	return "", nil
}
func (s *meshStore) UnlinkTodoSpace(context.Context, string, string, string) error { return nil }
func (s *meshStore) RenameTodoSpace(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (s *meshStore) LinkTodoAgent(context.Context, string, string, string, string, string, string, string, time.Time) ([]string, error) {
	return nil, nil
}
func (s *meshStore) LinkTodoAgentWithSource(context.Context, string, string, string, string, string, string, string, string, string, time.Time) ([]string, error) {
	return nil, nil
}
func (s *meshStore) LinkTodoAgentOfDoneTask(context.Context, string, string, string, string, string, string, string, string, string, time.Time) ([]string, error) {
	return nil, nil
}
func (s *meshStore) AutoLinkTodoAgent(context.Context, string, string, string, string, string, string, string, string, string, time.Time) (bool, []string, error) {
	return true, nil, nil
}
func (s *meshStore) UnlinkTodoAgent(context.Context, string, string, string) error { return nil }
func (s *meshStore) UpdateTodoAgentSnapshot(context.Context, string, string, string, string) error {
	return nil
}

func (s *meshStore) ListTodoDecisions(context.Context, string, bool) ([]db.TodoDecisionRow, error) {
	return nil, nil
}
func (s *meshStore) InsertTodoDecision(context.Context, db.TodoDecisionRow) (bool, error) {
	return false, db.ErrTodoNotFound
}
func (s *meshStore) SetTodoDecisionSupersedes(context.Context, string, string, string, time.Time) (*db.TodoDecisionRow, error) {
	return nil, db.ErrDecisionNotFound
}

// Queue tests use Postgres; this unrelated fixture has no durable phone delivery store.
func (s *meshStore) EnqueuePhoneWrite(context.Context, string, string, json.RawMessage) (*db.PhoneWrite, error) {
	return nil, errors.New("phone write queue unavailable")
}
func (s *meshStore) PhoneWrites(context.Context) ([]db.PhoneWrite, error)  { return nil, nil }
func (s *meshStore) ClaimPhoneWrite(context.Context, string) (bool, error) { return false, nil }
func (s *meshStore) FinishPhoneWrite(context.Context, string, string, json.RawMessage) error {
	return nil
}
func (s *meshStore) CancelPhoneWrite(context.Context, string) (*db.PhoneWrite, error) {
	return nil, nil
}
func (s *meshStore) RecoverPhoneWrites(context.Context) error { return nil }

func (s *meshStore) FindQueuedPhoneWrite(context.Context, string, json.RawMessage) (*db.PhoneWrite, error) {
	return nil, nil
}

func (s *meshStore) QueuedPhoneWrites(context.Context) ([]db.PhoneWrite, error) { return nil, nil }
