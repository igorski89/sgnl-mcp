// Package audit: a durable record of every tool call on both connectors,
// in a separate SQLite file, append-only (rows are only ever inserted, and
// deleted when older than the retention). Per call: called → asked (the
// dialog) → answered → done (outcome, backend calls). Never tokens,
// headers or keys; at the meta level never content, only who, what, the
// targets and hashes.
//
// Every hook is a no-op without a Store in the context, so code and tests
// that don't audit need nothing.
package audit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure Go, BSD-3
)

// Level: how much a connector's calls record.
type Level int

const (
	Off Level = iota
	Meta
	Full
)

func (l Level) String() string { return [...]string{"off", "meta", "full"}[l] }

// ParseLevel: off, meta or full.
func ParseLevel(s string, def Level) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def, nil
	case "off":
		return Off, nil
	case "meta":
		return Meta, nil
	case "full":
		return Full, nil
	}
	return Off, fmt.Errorf("audit level %q: off, meta or full", s)
}

// ParseRetention: a duration like 365d, 90d, 12h.
func ParseRetention(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		d, err := strconv.Atoi(n)
		if err != nil || d < 1 || d > 36500 {
			return 0, fmt.Errorf("audit retention %q: a number of days like 90d", s)
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Hour {
		return 0, fmt.Errorf("audit retention %q: like 90d (at least 1h)", s)
	}
	return d, nil
}

// Connectors.
const (
	User  = "user"
	Admin = "admin"
)

// Config: where and how much.
type Config struct {
	Path        string
	Admin, User Level
	UserStrict  bool // user changes fail closed too
	RetainAdmin time.Duration
	RetainUser  time.Duration
	// Clear: per connector, the argument fields stored as they are at the
	// meta level (ids, paths, accounts, dates, roles…); every other field
	// is stored as SHA-256 + length.
	Clear map[string]map[string]bool
}

// Store: the audit file.
type Store struct {
	failOn   map[string]bool // tests: events whose write fails
	cfg      Config
	mu       sync.Mutex // the one writer
	db       *sql.DB
	failures atomic.Int64
	lastFail atomic.Int64
	refused  refusedLimit
}

const schema = `
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY,
	ts INTEGER NOT NULL,
	call TEXT NOT NULL,
	connector TEXT NOT NULL,
	event TEXT NOT NULL,
	username TEXT, sub TEXT, client TEXT, session TEXT, ip TEXT,
	tool TEXT, dialog TEXT,
	data TEXT,
	full TEXT
);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS events_user ON events(username, ts);
CREATE INDEX IF NOT EXISTS events_tool ON events(tool, ts);
CREATE INDEX IF NOT EXISTS events_call ON events(call);
`

// Open: the store. If the file can't be opened it is retried in the
// background and every write counts as a failure meanwhile (admin changes
// then fail closed); the gateway still starts.
func Open(cfg Config) *Store {
	s := &Store{cfg: cfg}
	if err := s.open(); err != nil {
		s.fail("open", err)
		go func() {
			for range time.Tick(30 * time.Second) {
				if s.ready() {
					return
				}
				if err := s.open(); err == nil {
					slog.Info("audit: file opened", "path", cfg.Path)
					return
				}
			}
		}()
	}
	go func() {
		s.Purge()
		for range time.Tick(24 * time.Hour) {
			s.Purge()
		}
	}()
	return s
}

func (s *Store) open() error {
	dsn := "file:" + s.cfg.Path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return err
	}
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
	return nil
}

func (s *Store) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db != nil
}

func (s *Store) fail(what string, err error) {
	n := s.failures.Add(1)
	s.lastFail.Store(time.Now().Unix())
	slog.Error("audit write failed", "what", what, "error", err.Error(), "failures", n)
}

// Level: a connector's level.
func (s *Store) Level(connector string) Level {
	if s == nil {
		return Off
	}
	if connector == Admin {
		return s.cfg.Admin
	}
	return s.cfg.User
}

// row: one event.
type row struct {
	call, connector, event                   string
	username, sub, client, session, ip, tool string
	dialog                                   string
	data, full                               any
}

func (s *Store) insert(ctx context.Context, rows ...row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("the audit file isn't open")
	}
	for _, r := range rows {
		if s.failOn[r.event] {
			return fmt.Errorf("test: %s fails", r.event)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, r := range rows {
		var data, full any
		if r.data != nil {
			b, _ := json.Marshal(r.data)
			data = string(b)
		}
		if r.full != nil {
			b, _ := json.Marshal(r.full)
			full = string(b)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (ts, call, connector, event, username, sub, client, session, ip, tool, dialog, data, full)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			now, r.call, r.connector, r.event, r.username, r.sub, r.client, r.session, r.ip, r.tool, nullable(r.dialog), data, full); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Purge: rows older than each connector's retention.
func (s *Store) Purge() {
	if s == nil || !s.ready() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for c, d := range map[string]time.Duration{Admin: s.cfg.RetainAdmin, User: s.cfg.RetainUser} {
		if d <= 0 {
			continue
		}
		if r, err := s.db.Exec(`DELETE FROM events WHERE connector = ? AND ts < ?`, c, now.Add(-d).UnixMilli()); err != nil {
			slog.Error("audit purge failed", "error", err.Error())
		} else if n, _ := r.RowsAffected(); n > 0 {
			slog.Info("audit purged", "connector", c, "rows", n)
		}
	}
}

// OK: whether the last write succeeded (no failure in the last 5 minutes).
func (s *Store) OK() bool {
	return s.ready() && time.Since(time.Unix(s.lastFail.Load(), 0)) > 5*time.Minute
}

// Metrics: Prometheus text, for an internal listener only.
func (s *Store) Metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	ok := 0
	if s.OK() {
		ok = 1
	}
	fmt.Fprintf(w, "# HELP sgnl_mcp_audit_ok 1 when the audit file is open and no write failed in the last 5 minutes.\n# TYPE sgnl_mcp_audit_ok gauge\nsgnl_mcp_audit_ok %d\n", ok)
	fmt.Fprintf(w, "# HELP sgnl_mcp_audit_write_failures_total Audit writes that failed.\n# TYPE sgnl_mcp_audit_write_failures_total counter\nsgnl_mcp_audit_write_failures_total %d\n", s.failures.Load())
	fmt.Fprintf(w, "# HELP sgnl_mcp_audit_last_failure_seconds Unix time of the last failed audit write (0: none).\n# TYPE sgnl_mcp_audit_last_failure_seconds gauge\nsgnl_mcp_audit_last_failure_seconds %d\n", s.lastFail.Load())
}

// ---- one call

// Who: the caller.
type Who struct {
	Username, Sub, Client, Session, IP string
}

// IPFrom: the address the proxy saw: the last X-Forwarded-For entry (the
// one the proxy appended; earlier ones come from the client).
func IPFrom(h http.Header) string {
	if h == nil {
		return ""
	}
	v := h.Values("X-Forwarded-For")
	if len(v) == 0 {
		return ""
	}
	parts := strings.Split(v[len(v)-1], ",")
	ip := strings.TrimSpace(parts[len(parts)-1])
	if len(ip) > 64 {
		ip = ip[:64]
	}
	return ip
}

// Call: the record of one tool call, carried in its context.
type Call struct {
	s         *Store
	id        string
	connector string
	level     Level
	who       Who
	tool      string
	readOnly  bool
	start     time.Time
	mu        sync.Mutex
	backend   []map[string]any
}

type ctxKey struct{}

// From: the call recorded in ctx (nil: none).
func From(ctx context.Context) *Call {
	c, _ := ctx.Value(ctxKey{}).(*Call)
	return c
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mustClose: a failed write stops this call (admin changes; user changes
// in strict mode).
func (c *Call) mustClose() bool {
	return !c.readOnly && (c.connector == Admin || c.s.cfg.UserStrict)
}

func (c *Call) row(event string) row {
	return row{call: c.id, connector: c.connector, event: event, username: c.who.Username, sub: c.who.Sub, client: c.who.Client,
		session: c.who.Session, ip: c.who.IP, tool: c.tool}
}

// Begin: the called event, written before the tool runs. An error means
// the call must not go on (a change that couldn't be recorded).
func (s *Store) Begin(ctx context.Context, connector, tool string, readOnly bool, who Who, args any) (context.Context, *Call, error) {
	lvl := s.Level(connector)
	if lvl == Off {
		return ctx, nil, nil
	}
	c := &Call{s: s, id: newID(), connector: connector, level: lvl, who: who, tool: tool, readOnly: readOnly, start: time.Now()}
	r := c.row("called")
	meta, full := c.encodeArgs(args)
	r.data = map[string]any{"args": meta, "read_only": readOnly}
	if lvl == Full && full != nil {
		r.full = map[string]any{"args": full}
	}
	ctx = context.WithValue(ctx, ctxKey{}, c)
	if err := s.insert(context.WithoutCancel(ctx), r); err != nil {
		s.fail("called", err)
		if c.mustClose() {
			return ctx, c, fmt.Errorf("this change can't be recorded in the audit trail right now, so it wasn't made: try again later, or ask whoever runs the instance")
		}
	}
	return ctx, c, nil
}

// Refused: a call refused before it ran (not an admin, no access, rate
// limited), at most a few rows per person per hour.
func (s *Store) Refused(ctx context.Context, connector, tool string, who Who, reason string) {
	if s.Level(connector) == Off || who.Username == "" || !s.refused.allow(connector+"|"+who.Username) {
		return
	}
	c := &Call{s: s, id: newID(), connector: connector, who: who, tool: tool}
	r := c.row("refused")
	r.data = map[string]any{"reason": reason}
	if err := s.insert(context.WithoutCancel(ctx), r); err != nil {
		s.fail("refused", err)
	}
}

// DialogID: how asked and answered (two invocations) are linked: a hash
// of the confirmation's state, never the state itself.
func DialogID(state string) string {
	if state == "" {
		return ""
	}
	h := sha256.Sum256([]byte("dialog\x00" + state))
	return hex.EncodeToString(h[:12])
}

// Asked: the question put to the person. Admin and user-full store the
// exact text; user-meta its hash and length (it can quote mail).
func Asked(ctx context.Context, dialog, text string) {
	c := From(ctx)
	if c == nil {
		return
	}
	r := c.row("asked")
	r.dialog = dialog
	if c.connector == Admin || c.level == Full {
		r.data = map[string]any{"text": text}
	} else {
		r.data = map[string]any{"text": hashed(text)}
	}
	if err := c.s.insert(context.WithoutCancel(ctx), r); err != nil {
		c.s.fail("asked", err)
	}
}

// Answered: what came back: accept, decline, cancel, expired, mismatch
// (state), too-fast, typed-mismatch, untrusted-client, no-elicitation.
// An error means an accepted change must not go on.
func Answered(ctx context.Context, dialog, outcome string, ms int64) error {
	c := From(ctx)
	if c == nil {
		return nil
	}
	r := c.row("answered")
	r.dialog = dialog
	d := map[string]any{"outcome": outcome}
	if ms >= 0 {
		d["ms"] = ms
	}
	r.data = d
	if err := c.s.insert(context.WithoutCancel(ctx), r); err != nil {
		c.s.fail("answered", err)
		if outcome == "accept" && c.mustClose() {
			return fmt.Errorf("the answer can't be recorded in the audit trail right now, so nothing was done: try again later")
		}
	}
	return nil
}

// Backend: one call to a backend (method or JMAP methods, the path
// without its query, the status), kept until Done.
func Backend(ctx context.Context, system, method, path string, status int) {
	c := From(ctx)
	if c == nil {
		return
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if len(path) > 400 {
		path = path[:400]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.backend) < 100 {
		c.backend = append(c.backend, map[string]any{"system": system, "method": method, "path": path, "status": status})
	}
}

// Done: the outcome and the backend calls, in one write.
func (c *Call) Done(ctx context.Context, outcome, errText string, out any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	backend := c.backend
	c.mu.Unlock()
	r := c.row("done")
	d := map[string]any{"outcome": outcome, "ms": time.Since(c.start).Milliseconds()}
	if errText != "" {
		if len(errText) > 1000 {
			errText = errText[:1000]
		}
		d["error"] = errText
	}
	if out != nil {
		d["result"] = c.encodeResult(out)
	}
	r.data = d
	rows := []row{r}
	if len(backend) > 0 {
		b := c.row("backend")
		b.data = map[string]any{"calls": backend}
		rows = []row{b, r}
	}
	if err := c.s.insert(context.WithoutCancel(ctx), rows...); err != nil {
		c.s.fail("done", err)
	}
}

// ---- encoding

const maxField = 64 << 10

func hashed(s string) map[string]any {
	h := sha256.Sum256([]byte(s))
	return map[string]any{"sha256": hex.EncodeToString(h[:]), "len": len(s)}
}

func asMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// encodeArgs: meta (clear fields as they are, the rest hashed) and, for
// full, every field (capped: past 64 KB a hash and length).
func (c *Call) encodeArgs(args any) (meta, full map[string]any) {
	m := asMap(args)
	if m == nil {
		return nil, nil
	}
	clear := c.s.cfg.Clear[c.connector]
	meta = map[string]any{}
	for k, v := range m {
		if isEmpty(v) {
			continue
		}
		b, _ := json.Marshal(v)
		if (clear[k] || clear[c.tool+"."+k]) && len(b) <= 2048 {
			meta[k] = v
		} else {
			meta[k] = hashed(string(b))
		}
	}
	if c.level == Full {
		full = map[string]any{}
		for k, v := range m {
			b, _ := json.Marshal(v)
			if len(b) > maxField {
				full[k] = hashed(string(b))
			} else {
				full[k] = v
			}
		}
	}
	return meta, full
}

// encodeResult: for a change, the result as meta (ids, paths, counts in
// clear where allowed); for a read, its size, a hash and how many items.
func (c *Call) encodeResult(out any) any {
	b, err := json.Marshal(out)
	if err != nil || string(b) == "null" {
		return nil
	}
	res := hashed(string(b))
	m := asMap(out)
	for k, v := range m {
		if a, ok := v.([]any); ok {
			res["count_"+k] = len(a)
		}
	}
	if !c.readOnly && m != nil {
		clear := c.s.cfg.Clear[c.connector]
		kept := map[string]any{}
		for k, v := range m {
			if vb, _ := json.Marshal(v); (clear[k] || clear[c.tool+"."+k]) && len(vb) <= 2048 {
				kept[k] = v
			}
		}
		if len(kept) > 0 {
			res["fields"] = kept
		}
	}
	return res
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	case bool:
		return !x
	case float64:
		return x == 0
	}
	return false
}

// ---- refused rows: at most 20 per person per hour

type refusedLimit struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func (l *refusedLimit) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil || len(l.m) > 10000 {
		l.m = map[string][]time.Time{}
	}
	now := time.Now()
	ts := l.m[key][:0]
	for _, t := range l.m[key] {
		if now.Sub(t) < time.Hour {
			ts = append(ts, t)
		}
	}
	if len(ts) >= 20 {
		l.m[key] = ts
		return false
	}
	l.m[key] = append(ts, now)
	return true
}

// ---- backend calls, recorded from the HTTP transport

// Transport: next, recording each request made for an audited call:
// method, host and path (no query, no headers, no body), status. Token,
// introspection and discovery endpoints aren't recorded.
func Transport(next http.RoundTripper) http.RoundTripper {
	return transport{next}
}

type transport struct{ next http.RoundTripper }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	c := From(r.Context())
	if c == nil || skipPath(r.URL.Path) {
		return resp, err
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	Backend(r.Context(), r.URL.Host, r.Method, r.URL.Path, status)
	return resp, err
}

func skipPath(p string) bool {
	p = strings.ToLower(p)
	for _, s := range []string{"/token", "/introspect", "/userinfo", "/.well-known/", "/jwks"} {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// Methods: the JMAP (or management JMAP) methods of one request, which
// the transport can't see.
func Methods(ctx context.Context, system string, methods []string) {
	if From(ctx) == nil || len(methods) == 0 {
		return
	}
	Backend(ctx, system, "JMAP", strings.Join(methods, ","), 0)
}

// ---- reading

// Filter: what Search looks for.
type Filter struct {
	Username, Tool, Connector, Call, Target string
	Since, Until                            time.Time
	Limit                                   int
}

// Event: one row as Search returns it. For the user connector only the
// meta record (data): never full arguments.
type Event struct {
	Time      string `json:"time"`
	Call      string `json:"call"`
	Connector string `json:"connector"`
	Event     string `json:"event"`
	Username  string `json:"username,omitempty"`
	Client    string `json:"client,omitempty"`
	IP        string `json:"ip,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Dialog    string `json:"dialog,omitempty"`
	Data      any    `json:"data,omitempty"`
	Full      any    `json:"full,omitempty" jsonschema:"admin calls only: the full arguments"`
}

// Search: newest first. Target matches the meta record only (for people's
// own calls, never their full arguments).
func (s *Store) Search(ctx context.Context, f Filter) ([]Event, error) {
	if s == nil || !s.ready() {
		return nil, fmt.Errorf("the audit file isn't open")
	}
	q := `SELECT ts, call, connector, event, COALESCE(username,''), COALESCE(client,''), COALESCE(ip,''), COALESCE(tool,''), COALESCE(dialog,''), COALESCE(data,''), COALESCE(full,'') FROM events WHERE 1=1`
	var args []any
	add := func(cond string, v any) { q += " AND " + cond; args = append(args, v) }
	if f.Username != "" {
		add("username = ?", f.Username)
	}
	if f.Tool != "" {
		add("tool = ?", f.Tool)
	}
	if f.Connector != "" {
		add("connector = ?", f.Connector)
	}
	if f.Call != "" {
		add("call = ?", f.Call)
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		add("ts < ?", f.Until.UnixMilli())
	}
	if f.Target != "" {
		add("instr(data, ?) > 0", f.Target)
	}
	if f.Limit < 1 || f.Limit > 500 {
		f.Limit = 100
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var ts int64
		var e Event
		var data, full string
		if err := rows.Scan(&ts, &e.Call, &e.Connector, &e.Event, &e.Username, &e.Client, &e.IP, &e.Tool, &e.Dialog, &data, &full); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(ts).UTC().Format(time.RFC3339)
		if data != "" {
			_ = json.Unmarshal([]byte(data), &e.Data)
		}
		if full != "" && e.Connector == Admin {
			_ = json.Unmarshal([]byte(full), &e.Full)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FailForTest: from now on writes of these events fail (tests only).
func FailForTest(s *Store, events ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failOn = map[string]bool{}
	for _, e := range events {
		s.failOn[e] = true
	}
}

// OpenForTest: a store that is ready now (no background retry), for tests.
func OpenForTest(cfg Config) (*Store, error) {
	s := &Store{cfg: cfg}
	return s, s.open()
}

var jsonMarshal = json.Marshal

// PathForTest: the store's file.
func PathForTest(s *Store) string { return s.cfg.Path }
