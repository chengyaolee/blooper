package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAPNS(t *testing.T, h http.HandlerFunc) (*APNSClient, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	path := filepath.Join(t.TempDir(), "key.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	apns, err := NewAPNSClient(path, "KEY123", "TEAM123", "com.blooper.remote", true)
	if err != nil {
		t.Fatal(err)
	}
	if apns.URL != "https://api.sandbox.push.apple.com" {
		t.Fatal(apns.URL)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	apns.URL, apns.HTTPClient = srv.URL, srv.Client()
	return apns, key
}

func TestAPNSWireAndJWT(t *testing.T) {
	var auth string
	var public *ecdsa.PrivateKey
	apns, key := testAPNS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 || r.URL.Path != "/3/device/aabb" || r.Header.Get("apns-topic") != "com.blooper.remote" || r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-collapse-id") != "job1" {
			t.Errorf("bad APNs request: %s %s %v", r.Proto, r.URL, r.Header)
		}
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"job_id":"job1"`) || !strings.Contains(string(data), "Approval needed") {
			t.Errorf("payload: %s", data)
		}
		token := strings.TrimPrefix(r.Header.Get("authorization"), "bearer ")
		if auth != "" && auth != token {
			t.Error("provider token was not cached")
		}
		auth = token
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Error("invalid JWT")
			return
		}
		header, _ := base64.RawURLEncoding.DecodeString(parts[0])
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if !strings.Contains(string(header), `"alg":"ES256"`) || !strings.Contains(string(header), `"kid":"KEY123"`) || !strings.Contains(string(claims), `"iss":"TEAM123"`) || len(sig) != 64 || !ecdsa.Verify(&public.PublicKey, hash[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			t.Error("invalid JWT signature or claims")
		}
	})
	public = key
	for i := 0; i < 2; i++ {
		if invalid, err := apns.send(context.Background(), "aabb", "job1", StateApproval); invalid || err != nil {
			t.Fatalf("send: %v %v", invalid, err)
		}
	}
}

func TestPushRegistrationDeliveryAndRevocation(t *testing.T) {
	delivered := make(chan string, 10)
	apns, _ := testAPNS(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		delivered <- string(data)
		if strings.HasSuffix(r.URL.Path, "dead") {
			w.WriteHeader(410)
			io.WriteString(w, `{"reason":"Unregistered"}`)
		}
	})
	cfg := Config{DBPath: filepath.Join(t.TempDir(), "remote.db"), APNS: apns}
	c, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close() }()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	phone := &Device{ID: DeviceID(pub), Kind: KindPhone, PublicKey: pub}
	c.mu.Lock()
	c.devices[phone.ID] = phone
	err = c.saveDevice(phone)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) int {
		req, _ := SignedRequest(method, "http://localhost", "/v1/push-token", []byte(body), priv)
		w := httptest.NewRecorder()
		c.Routes().ServeHTTP(w, req)
		return w.Code
	}
	if status := call("POST", `{"token":"../bad"}`); status != 400 {
		t.Fatal(status)
	}
	if status := call("POST", `{"token":"aabb"}`); status != 200 {
		t.Fatal(status)
	}
	// Tokens persist separately from the public device list.
	data, _ := json.Marshal(c.deviceList())
	if strings.Contains(string(data), "aabb") {
		t.Fatal("push token leaked in device list")
	}
	c.Close()
	c, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.pushTokens[phone.ID] != "aabb" {
		t.Fatal("push registration lost on restart")
	}
	state := func(s string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		j := c.jobs["job1"]
		if j == nil {
			j = &Job{ID: "job1", DeviceID: phone.ID, State: StateRunning}
			c.jobs[j.ID] = j
		}
		c.setState(j, s, "private transcript must stay local")
	}
	state(StateApproval)
	select {
	case payload := <-delivered:
		if strings.Contains(payload, "private transcript") {
			t.Fatal("private job detail sent to APNs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification not delivered")
	}
	state(StateApproval)
	select {
	case <-delivered:
		t.Fatal("duplicate state notification")
	case <-time.After(30 * time.Millisecond):
	}
	if status := call("POST", `{"token":"dead"}`); status != 200 {
		t.Fatal(status)
	}
	state(StateSucceeded)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal notification not delivered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		token := c.pushTokens[phone.ID]
		c.mu.Unlock()
		if token == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("invalid device token was not removed")
		}
		time.Sleep(time.Millisecond)
	}
	if status := call("POST", `{"token":"ccdd"}`); status != 200 {
		t.Fatal(status)
	}
	if status := call("DELETE", ""); status != 200 {
		t.Fatal(status)
	}
	if c.pushTokens[phone.ID] != "" {
		t.Fatal("unregister failed")
	}
	if status := call("POST", `{"token":"ccdd"}`); status != 200 {
		t.Fatal(status)
	}
	c.mu.Lock()
	err = c.revoke(phone.ID)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if status := call("POST", `{"token":"ccdd"}`); status != 401 {
		t.Fatal(status)
	}
	c.mu.Lock()
	token := c.pushTokens[phone.ID]
	c.mu.Unlock()
	if token != "" {
		t.Fatal("revocation retained push token")
	}
}
