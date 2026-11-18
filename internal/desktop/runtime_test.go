package desktop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chengyaolee/blooper/internal/remote"
)

func TestDesktopChild(t *testing.T) {
	if os.Getenv("BLOOPER_TEST_DESKTOP") != "1" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	if err := Run(ctx, os.Getenv("BLOOPER_TEST_DIR")); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopLANPairingAndCredentialIsolation(t *testing.T) {
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	token, dir := hex.EncodeToString(tokenBytes), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDesktopChild$")
	cmd.Env = append(os.Environ(), "BLOOPER_TEST_DESKTOP=1", "BLOOPER_TEST_DIR="+dir, "BLOOPER_DESKTOP_TOKEN="+token)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("desktop shutdown: %v: %s", err, stderr.String())
		}
	}()
	var ready struct {
		Ready bool   `json:"ready"`
		URL   string `json:"url"`
	}
	scan := bufio.NewScanner(stdout)
	for scan.Scan() {
		_ = json.Unmarshal(scan.Bytes(), &ready)
		if ready.Ready {
			break
		}
	}
	if !ready.Ready {
		t.Fatalf("no ready handshake: %s", stderr.String())
	}
	call := func(method, path string, body any, auth bool) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, method, ready.URL+path, bytes.NewReader(data))
		if auth {
			req.Header.Set("X-Blooper-Desktop", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	if code, _ := call("GET", "/desktop/status", nil, false); code != 401 {
		t.Fatalf("unauthenticated settings: %d", code)
	}
	// Applying in-memory credentials must never write them to any runtime file.
	secret := "desktop-provider-test-secret"
	if code, _ := call("POST", "/desktop/configure", map[string]string{"provider": "openai", "openaiKey": secret}, true); code != 200 {
		t.Fatal(code)
	}
	code, data := call("GET", "/desktop/status", nil, true)
	if code != 200 || bytes.Contains(data, []byte(secret)) {
		t.Fatalf("settings leaked credentials: status %d", code)
	}
	var status struct {
		Addresses []string        `json:"addresses"`
		Devices   []remote.Device `json:"devices"`
	}
	_ = json.Unmarshal(data, &status)
	if len(status.Devices) != 1 || status.Devices[0].Kind != remote.KindDesktop {
		t.Fatal("local runner was not enrolled automatically")
	}
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			content, _ := os.ReadFile(path)
			if bytes.Contains(content, []byte(secret)) {
				t.Errorf("credential persisted in %s", path)
			}
		}
		return nil
	})
	if len(status.Addresses) == 0 {
		t.Log("No LAN interface: verified private runtime; skipping the network pairing portion")
		return
	}
	code, data = call("POST", "/desktop/pair", map[string]string{"address": status.Addresses[0]}, true)
	if code != 200 {
		t.Fatalf("pair QR: %d %s", code, data)
	}
	var pairing struct {
		Payload string `json:"payload"`
		QR      []byte `json:"qr"`
	}
	_ = json.Unmarshal(data, &pairing)
	uri, _ := url.Parse(pairing.Payload)
	pin := uri.Query().Get("pin")
	if len(pin) != 64 || len(pairing.QR) == 0 {
		t.Fatal("QR missing a certificate identity")
	}
	base := uri.Query().Get("url")
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		digest := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if hex.EncodeToString(digest[:]) != pin {
			return context.Canceled
		}
		return nil
	}}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signed := func(method, path string, body any) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, err := remote.SignedRequest(method, base, path, data, priv)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	if code, data := signed("POST", "/v1/pair", remote.PairRequest{Code: uri.Query().Get("code"), Name: "Test iPhone", PublicKey: pub}); code != 201 {
		t.Fatalf("pair: %d %s", code, data)
	}
	if code, _ := signed("POST", "/v1/pair", remote.PairRequest{Code: uri.Query().Get("code"), Name: "Test iPhone", PublicKey: pub}); code != 403 {
		t.Fatal("pair code was reused")
	}
	if code, data := signed("GET", "/v1/providers", nil); code != 200 || bytes.Contains(data, []byte(secret)) || !bytes.Contains(data, []byte(`"connected":true`)) {
		t.Fatalf("phone provider status: %d %s", code, data)
	}
	if code, _ := signed("GET", "/desktop/status", nil); code != 404 {
		t.Fatal("desktop settings exposed on LAN")
	}
	if code, _ := signed("GET", "/admin/devices", nil); code != 404 {
		t.Fatal("admin API exposed on LAN")
	}
	if code, _ := signed("POST", "/v1/heartbeat", nil); code != 200 {
		t.Fatal(code)
	}
	_, data = call("GET", "/desktop/status", nil, true)
	_ = json.Unmarshal(data, &status)
	phoneOnline := false
	for _, device := range status.Devices {
		if device.ID == remote.DeviceID(pub) {
			phoneOnline = device.Reachable
		}
	}
	if !phoneOnline {
		t.Fatal("phone presence did not update")
	}
	if code, _ := call("POST", "/desktop/revoke", map[string]string{"id": remote.DeviceID(pub)}, true); code != 200 {
		t.Fatal(code)
	}
	if code, _ := signed("GET", "/v1/providers", nil); code != 401 {
		t.Fatal("revoked phone still has access")
	}
}

func TestDesktopAcceptsOnlyLocalNetworkClients(t *testing.T) {
	h := localNetworkOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, c := range []struct {
		address string
		want    int
	}{{"127.0.0.1:1234", 200}, {"192.168.1.2:1234", 200}, {"10.0.0.3:1234", 200}, {"203.0.113.1:1234", 403}} {
		request := httptest.NewRequest("GET", "https://local/v1/devices", nil)
		request.RemoteAddr = c.address
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != c.want {
			t.Fatalf("%s: %d, want %d", c.address, response.Code, c.want)
		}
	}
}
