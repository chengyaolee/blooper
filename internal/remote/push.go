package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// APNSClient sends alert notifications using an Apple .p8 authentication key.
// A single connection pool and a cached ES256 token are shared across sends.
type APNSClient struct {
	URL           string
	Topic         string
	HTTPClient    *http.Client
	key           *ecdsa.PrivateKey
	keyID, teamID string
	mu            sync.Mutex
	token         string
	issued        time.Time
}

func NewAPNSClient(keyPath, keyID, teamID, topic string, sandbox bool) (*APNSClient, error) {
	if keyID == "" || teamID == "" || topic == "" {
		return nil, fmt.Errorf("APNs requires a key ID, team ID and bundle ID (topic)")
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("APNs key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("APNs key must be a PEM-encoded .p8 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("APNs key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("APNs requires an ECDSA P-256 private key")
	}
	endpoint := "https://api.push.apple.com"
	if sandbox {
		endpoint = "https://api.sandbox.push.apple.com"
	}
	return &APNSClient{URL: endpoint, Topic: topic, HTTPClient: &http.Client{Timeout: 10 * time.Second}, key: key, keyID: keyID, teamID: teamID}, nil
}

func (a *APNSClient) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Since(a.issued) < 50*time.Minute {
		return a.token, nil
	}
	encode := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	now := time.Now()
	unsigned := encode(map[string]string{"alg": "ES256", "kid": a.keyID}) + "." + encode(map[string]any{"iss": a.teamID, "iat": now.Unix()})
	hash := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, hash[:])
	if err != nil {
		return "", err
	}
	// JWT ES256 uses fixed-width R || S, not ASN.1 DER.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.token, a.issued = unsigned+"."+base64.RawURLEncoding.EncodeToString(sig), now
	return a.token, nil
}

// send returns invalid=true only for device tokens Apple says must be discarded.
func (a *APNSClient) send(ctx context.Context, token, jobID, state string) (invalid bool, err error) {
	jwt, err := a.providerToken()
	if err != nil {
		return false, err
	}
	title, body := "Blooper job updated", "Open Blooper to see the result."
	switch state {
	case StateApproval:
		title, body = "Approval needed", "Open Blooper to review an action."
	case StateClarification:
		title, body = "Your input is needed", "Open Blooper to choose a computer."
	case StateSucceeded:
		title = "Blooper job completed"
	case StateFailed:
		title = "Blooper job failed"
	case StateCancelled:
		title = "Blooper job cancelled"
	case StateExpired:
		title = "Blooper job expired"
	case StateReconciliation:
		title, body = "Check your computer", "An action's outcome is uncertain. Open Blooper for details."
	}
	payload, _ := json.Marshal(map[string]any{
		"aps":    map[string]any{"alert": map[string]string{"title": title, "body": body}, "sound": "default", "thread-id": jobID},
		"job_id": jobID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL+"/3/device/"+token, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	req.Header.Set("apns-collapse-id", jobID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false, err
	}
	if resp.StatusCode == http.StatusOK {
		return false, nil
	}
	var failure struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(data, &failure)
	invalid = resp.StatusCode == http.StatusGone || failure.Reason == "BadDeviceToken" || failure.Reason == "DeviceTokenNotForTopic"
	return invalid, fmt.Errorf("APNs status %d: %s", resp.StatusCode, failure.Reason)
}

type pushNotice struct{ phoneID, jobID, state string }

func (c *Coordinator) handlePushToken(_ *http.Request, dev *Device, body []byte) (int, any) {
	if c.cfg.APNS == nil {
		return http.StatusNotImplemented, apiError{"Push notifications are not configured on this server"}
	}
	var req struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &req) != nil {
		return 400, apiError{"invalid JSON"}
	}
	token := strings.ToLower(req.Token)
	raw, err := hex.DecodeString(token)
	// APNs token length is not fixed. Bound the input without assuming 32 bytes.
	if err != nil || len(raw) == 0 || len(raw) > 256 {
		return 400, apiError{"invalid APNs device token"}
	}
	if err := c.put("push", dev.ID, token); err != nil {
		return 500, apiError{"could not save push token"}
	}
	// One installation may have re-paired with a new device identity.
	for id, old := range c.pushTokens {
		if id != dev.ID && old == token {
			_ = c.removePushToken(id)
		}
	}
	c.pushTokens[dev.ID] = token
	return 200, map[string]string{"status": "registered"}
}

func (c *Coordinator) removePushToken(id string) error {
	if _, err := c.db.Exec(`DELETE FROM records WHERE kind = 'push' AND id = ?`, id); err != nil {
		return err
	}
	delete(c.pushTokens, id)
	return nil
}

// queuePush is called with c.mu held. Delivery is best effort, outside the job
// lock; the event feed remains authoritative when APNs is unavailable.
// ponytail: a bounded, non-durable queue without retries; use a persistent outbox
// if notification delivery needs guarantees across restarts or APNs outages.
func (c *Coordinator) queuePush(j *Job) {
	if c.cfg.APNS == nil || c.pushTokens[j.DeviceID] == "" {
		return
	}
	if !terminalState(j.State) && j.State != StateApproval && j.State != StateClarification {
		return
	}
	select {
	case c.pushQueue <- pushNotice{j.DeviceID, j.ID, j.State}:
	default:
		log.Printf("remote: push queue full; notification dropped for job %s", j.ID)
	}
}

func (c *Coordinator) pushLoop() {
	defer close(c.pushDone)
	for {
		select {
		case <-c.closed:
			return
		case n := <-c.pushQueue:
			c.mu.Lock()
			d, token := c.devices[n.phoneID], c.pushTokens[n.phoneID]
			allowed := d != nil && !d.Revoked && d.Kind == KindPhone && token != ""
			c.mu.Unlock()
			if !allowed {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			invalid, err := c.cfg.APNS.send(ctx, token, n.jobID, n.state)
			cancel()
			if invalid {
				c.mu.Lock()
				if c.pushTokens[n.phoneID] == token {
					if e := c.removePushToken(n.phoneID); e != nil {
						log.Printf("remote: remove invalid push token: %v", e)
					}
				}
				c.mu.Unlock()
			}
			if err != nil {
				log.Printf("remote: push for job %s: %v", n.jobID, err)
			}
		}
	}
}
