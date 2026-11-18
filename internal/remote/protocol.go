// Package remote is Blooper's coordinator for phone-driven jobs: enrolled
// devices, durable jobs, approvals, and the runner protocol. See docs/REMOTE.md.
//
// This file is the wire contract shared with internal/runner and the iOS app:
// JSON types, request signing, and the typed tool catalog.
package remote

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/skip2/go-qrcode"
)

// Job states (brief §4).
const (
	StateAccepted       = "accepted"
	StateClarification  = "waiting_for_clarification"
	StateWaitingTarget  = "waiting_for_target"
	StateQueued         = "queued"
	StateRunning        = "running"
	StateApproval       = "waiting_for_approval"
	StateCancelRequest  = "cancel_requested"
	StateSucceeded      = "succeeded"
	StateFailed         = "failed"
	StateCancelled      = "cancelled"
	StateExpired        = "expired"
	StateReconciliation = "needs_reconciliation"
)

// Action statuses. An action is one typed tool call in a job's journal.
const (
	ActionAwaiting   = "awaiting_approval"
	ActionReady      = "ready"      // approved or routine; waiting for the runner to pick it up
	ActionDispatched = "dispatched" // handed to the runner; an effect may exist from here on
	ActionDone       = "done"       // ran to completion; see ExitCode
	ActionFailed     = "failed"     // the runner could not run it
	ActionRejected   = "rejected"   // the owner said no, or the approval expired
	ActionCancelled  = "cancelled"
	ActionUnknown    = "unknown" // dispatched, then the runner was lost: the effect is uncertain
)

const (
	KindPhone   = "phone"
	KindDesktop = "desktop"
	KindCloud   = "cloud"
)

// All times on the wire are unix seconds; 0 means unset.

type Device struct {
	ID           string   `json:"id"` // DeviceID(PublicKey)
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	OS           string   `json:"os"`
	PublicKey    []byte   `json:"public_key"`
	Tools        []string `json:"tools"`      // capabilities the owner confirmed at enrollment
	Workspaces   []string `json:"workspaces"` // names only; paths and commands stay on the runner
	Apps         []string `json:"apps"`
	GrantExpires int64    `json:"grant_expires"` // standing grant for routine tools ends here
	LastSeen     int64    `json:"last_seen"`
	Reachable    bool     `json:"reachable"` // computed on read
	Revoked      bool     `json:"revoked"`
	CreatedAt    int64    `json:"created_at"`
}

type Option struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Action struct {
	ID              string          `json:"id"`
	Tool            string          `json:"tool"`
	Args            json.RawMessage `json:"args"`
	Summary         string          `json:"summary"` // what the owner reviews: action, scope, destination
	Status          string          `json:"status"`
	Challenge       string          `json:"challenge,omitempty"` // single-use, present while awaiting approval
	ApprovalExpires int64           `json:"approval_expires,omitempty"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	Output          string          `json:"output,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	FinishedAt      int64           `json:"finished_at,omitempty"`

	stop, stopSent bool // cancel requested for a dispatched action / delivered to the runner
}

type Event struct {
	Seq     int    `json:"seq"` // monotonic per job
	Type    string `json:"type"`
	Message string `json:"message"`
	At      int64  `json:"at"`
}

type Job struct {
	ID              string    `json:"id"`
	DeviceID        string    `json:"device_id"` // the phone that asked
	ClientRequestID string    `json:"client_request_id"`
	PayloadHash     string    `json:"payload_hash"`
	Transcript      string    `json:"transcript"`
	Target          string    `json:"target"` // "", "cloud", or a device ID
	Workspace       string    `json:"workspace,omitempty"`
	State           string    `json:"state"`
	Detail          string    `json:"detail"` // wait reason, failure reason, or acknowledgment
	RunnerID        string    `json:"runner_id,omitempty"`
	RunnerName      string    `json:"runner_name,omitempty"`
	Deadline        int64     `json:"deadline"`
	Result          string    `json:"result,omitempty"`
	Question        string    `json:"question,omitempty"`
	Options         []Option  `json:"options,omitempty"`
	Actions         []*Action `json:"actions"`
	Events          []Event   `json:"events"`
	CreatedAt       int64     `json:"created_at"`
	UpdatedAt       int64     `json:"updated_at"`

	// The execution lease. Not persisted: a coordinator restart drops all leases.
	lease        string
	leaseExpires time.Time
}

func terminalState(s string) bool {
	return slices.Contains([]string{StateSucceeded, StateFailed, StateCancelled, StateExpired, StateReconciliation}, s)
}

func terminalAction(s string) bool {
	return !slices.Contains([]string{ActionAwaiting, ActionReady, ActionDispatched}, s)
}

// Requests.

type PairRequest struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	PublicKey []byte `json:"public_key"`
}

type EnrollRequest struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	OS         string   `json:"os"`
	PublicKey  []byte   `json:"public_key"`
	Tools      []string `json:"tools"`
	Workspaces []string `json:"workspaces"`
	Apps       []string `json:"apps"`
}

type EnrollChallenge struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

type JobRequest struct {
	Transcript      string `json:"transcript"`
	Target          string `json:"target"`
	Workspace       string `json:"workspace"`
	ClientRequestID string `json:"client_request_id"`
}

// Runner protocol.

type PollResponse struct {
	Actions []RunnerAction `json:"actions"`
	Cancel  []string       `json:"cancel"` // action IDs to stop
}

type RunnerAction struct {
	ID    string          `json:"id"`
	JobID string          `json:"job_id"`
	Lease string          `json:"lease"` // must be echoed with the result
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
}

type RunnerResult struct {
	ActionID string `json:"action_id"`
	JobID    string `json:"job_id"`
	Lease    string `json:"lease"`
	Status   string `json:"status"` // ActionDone, ActionFailed, or ActionCancelled
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output"`
}

// Tool catalog. Names use underscores because OpenAI-compatible APIs reject dots.

type ToolSpec struct {
	Description string
	Param       string // every v0.1 tool takes exactly one string parameter
	ParamDesc   string
	Confirm     bool // always needs a phone confirmation, even under a standing grant
}

var Catalog = map[string]ToolSpec{
	"repo_run_tests": {
		Description: "Run the test command configured for an enrolled workspace on the target device. Returns the exit code and the tail of the log.",
		Param:       "workspace", ParamDesc: "Name of an enrolled workspace",
	},
	"app_open": {
		Description: "Open an allow-listed application on the target device's desktop.",
		Param:       "app", ParamDesc: "Name of an allow-listed app",
		Confirm: true, // a visible action on the owner's desktop
	},
	"web_search": {
		Description: "Search the web from the target runner and return a short summary.",
		Param:       "query", ParamDesc: "The search query",
		// Web pages can steer the planner into putting job data in a query, which
		// then leaves the runner. Ask the owner each time, and show the query.
		Confirm: true,
	},
}

// ParseArg validates args against the catalog and returns the tool's single parameter.
func ParseArg(tool string, args json.RawMessage) (string, error) {
	spec, ok := Catalog[tool]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", tool)
	}
	var m map[string]string
	if err := json.Unmarshal(args, &m); err != nil || len(m) != 1 || m[spec.Param] == "" {
		return "", fmt.Errorf("%s takes exactly one string argument %q", tool, spec.Param)
	}
	return m[spec.Param], nil
}

// Request signing. Every request is signed with the device's Ed25519 key over
// method, request URI, timestamp, and body hash.

const (
	HeaderDevice = "X-Blooper-Device"
	HeaderTime   = "X-Blooper-Time"
	HeaderSig    = "X-Blooper-Sig"
)

func DeviceID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

func signingInput(method, uri, ts string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(method + "\n" + uri + "\n" + ts + "\n" + hex.EncodeToString(sum[:]))
}

// SignedRequest builds a request to base+uri signed with priv.
func SignedRequest(method, base, uri string, body []byte, priv ed25519.PrivateKey) (*http.Request, error) {
	req, err := http.NewRequest(method, base+uri, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(HeaderDevice, DeviceID(priv.Public().(ed25519.PublicKey)))
	req.Header.Set(HeaderTime, ts)
	req.Header.Set(HeaderSig, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signingInput(method, uri, ts, body))))
	return req, nil
}

// maxClockSkew bounds how old a signed request may be.
// ponytail: no nonce cache, so a captured request can be replayed inside this
// window. TLS hides requests, job submission is idempotent, and approvals are
// single-use. Add a nonce cache if this is ever served without TLS on a hostile network.
const maxClockSkew = 5 * time.Minute

func verifyRequest(r *http.Request, body []byte, pub ed25519.PublicKey) error {
	ts, err := strconv.ParseInt(r.Header.Get(HeaderTime), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)).Abs() > maxClockSkew {
		return fmt.Errorf("stale or missing timestamp")
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(HeaderSig))
	if err != nil || len(pub) != ed25519.PublicKeySize ||
		!ed25519.Verify(pub, signingInput(r.Method, r.URL.RequestURI(), r.Header.Get(HeaderTime), body), sig) {
		return fmt.Errorf("bad signature")
	}
	return nil
}

// newCode returns a short pairing code (80 bits) that is still typeable by hand.
func newCode() string {
	b := make([]byte, 10)
	rand.Read(b)
	return base32.StdEncoding.EncodeToString(b)
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// PrintQR renders content as a terminal QR code. Dark terminal themes may need
// the phone's camera a little closer; the plain code is always printed too.
func PrintQR(w io.Writer, content string) {
	if q, err := qrcode.New(content, qrcode.Medium); err == nil {
		fmt.Fprint(w, q.ToSmallString(false))
	}
}
