package remote

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"time"
)

type apiError struct {
	Error string `json:"error"`
}

// handler runs with c.mu held; its result is marshalled before the lock is released.
type handler func(r *http.Request, dev *Device, body []byte) (int, any)

func writeJSON(w http.ResponseWriter, code int, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(data)
}

func fail(w http.ResponseWriter, code int, msg string) {
	data, _ := json.Marshal(apiError{msg})
	writeJSON(w, code, data)
}

// authenticate reads the body and verifies the device signature. kind is
// KindPhone, or "runner" for desktop and cloud runners.
func (c *Coordinator) authenticate(w http.ResponseWriter, r *http.Request, kind string, limit int64) (id string, body []byte, ok bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "request body too large")
		return "", nil, false
	}
	id = r.Header.Get(HeaderDevice)
	c.mu.Lock()
	d := c.devices[id]
	known := d != nil && !d.Revoked
	var pub []byte
	var devKind string
	if known {
		pub, devKind = d.PublicKey, d.Kind
	}
	c.mu.Unlock()
	if !known || verifyRequest(r, body, pub) != nil {
		fail(w, http.StatusUnauthorized, "unknown or revoked device, or bad signature")
		return "", nil, false
	}
	if (devKind == KindPhone) != (kind == KindPhone) {
		fail(w, http.StatusForbidden, "this endpoint is not for "+devKind+" devices")
		return "", nil, false
	}
	return id, body, true
}

func (c *Coordinator) signed(kind string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, body, ok := c.authenticate(w, r, kind, 1<<20)
		if !ok {
			return
		}
		c.mu.Lock()
		dev := c.devices[id]
		if dev == nil || dev.Revoked { // revoked between the check and now
			c.mu.Unlock()
			fail(w, http.StatusUnauthorized, "unknown or revoked device")
			return
		}
		if dev.Kind == KindPhone {
			dev.LastSeen = time.Now().Unix()
		}
		code, v := h(r, dev, body)
		data, _ := json.Marshal(v)
		c.mu.Unlock()
		writeJSON(w, code, data)
	}
}

// Routes returns the /v1 phone and runner API plus the /admin API for the local CLI.
func (c *Coordinator) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", c.handlePair)
	mux.HandleFunc("POST /v1/enroll", c.handleEnrollStart)

	mux.HandleFunc("GET /v1/enroll/{code}", c.signed(KindPhone, c.handleEnrollGet))
	mux.HandleFunc("POST /v1/enroll/{code}/approve", c.signed(KindPhone, c.handleEnrollApprove))
	mux.HandleFunc("GET /v1/providers", c.signed(KindPhone, func(*http.Request, *Device, []byte) (int, any) {
		if c.cfg.ProviderStatus == nil {
			return 200, ProviderStatus{Deepgram: c.cfg.DeepgramKey != ""}
		}
		return 200, c.cfg.ProviderStatus()
	}))
	mux.HandleFunc("POST /v1/heartbeat", c.signed(KindPhone, func(*http.Request, *Device, []byte) (int, any) { return 200, map[string]bool{"connected": true} }))
	mux.HandleFunc("GET /v1/devices", c.signed(KindPhone, func(*http.Request, *Device, []byte) (int, any) { return 200, c.deviceList() }))
	mux.HandleFunc("DELETE /v1/devices/{id}", c.signed(KindPhone, func(r *http.Request, _ *Device, _ []byte) (int, any) {
		if err := c.revoke(r.PathValue("id")); err != nil {
			return 404, apiError{err.Error()}
		}
		return 200, c.deviceList()
	}))
	mux.HandleFunc("POST /v1/push-token", c.signed(KindPhone, c.handlePushToken))
	mux.HandleFunc("DELETE /v1/push-token", c.signed(KindPhone, func(_ *http.Request, d *Device, _ []byte) (int, any) {
		if err := c.removePushToken(d.ID); err != nil {
			return 500, apiError{"could not remove push token"}
		}
		return 200, map[string]string{"status": "unregistered"}
	}))
	mux.HandleFunc("POST /v1/transcribe", c.handleTranscribe)
	mux.HandleFunc("POST /v1/jobs", c.signed(KindPhone, func(_ *http.Request, dev *Device, body []byte) (int, any) {
		var req JobRequest
		if json.Unmarshal(body, &req) != nil {
			return 400, apiError{"invalid JSON"}
		}
		return c.submit(dev.ID, req)
	}))
	mux.HandleFunc("GET /v1/jobs", c.signed(KindPhone, c.handleJobList))
	mux.HandleFunc("GET /v1/jobs/{id}", c.handleJobGet)
	mux.HandleFunc("POST /v1/jobs/{id}/clarify", c.signed(KindPhone, c.handleClarify))
	mux.HandleFunc("POST /v1/jobs/{id}/approval", c.signed(KindPhone, c.handleApproval))
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", c.signed(KindPhone, c.handleCancel))

	mux.HandleFunc("POST /v1/runner/poll", c.handlePoll)
	mux.HandleFunc("POST /v1/runner/result", c.signed("runner", func(_ *http.Request, dev *Device, body []byte) (int, any) {
		var res RunnerResult
		if json.Unmarshal(body, &res) != nil {
			return 400, apiError{"invalid JSON"}
		}
		return c.result(dev, res)
	}))

	mux.HandleFunc("POST /admin/pair", c.admin(func(*http.Request) (int, any) {
		c.pairCode, c.pairExpires = newCode(), time.Now().Add(c.cfg.EnrollTTL)
		return 200, EnrollChallenge{Code: c.pairCode, ExpiresAt: c.pairExpires.Unix()}
	}))
	mux.HandleFunc("GET /admin/devices", c.admin(func(*http.Request) (int, any) { return 200, c.deviceList() }))
	mux.HandleFunc("POST /admin/revoke/{id}", c.admin(func(r *http.Request) (int, any) {
		if err := c.revoke(r.PathValue("id")); err != nil {
			return 404, apiError{err.Error()}
		}
		return 200, c.deviceList()
	}))
	return mux
}

func (c *Coordinator) admin(h func(r *http.Request) (int, any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+c.adminToken)) != 1 {
			fail(w, http.StatusUnauthorized, "admin token required")
			return
		}
		c.mu.Lock()
		code, v := h(r)
		data, _ := json.Marshal(v)
		c.mu.Unlock()
		writeJSON(w, code, data)
	}
}

func (c *Coordinator) deviceList() []Device {
	list := []Device{}
	for _, d := range c.devices {
		v := *d
		v.Reachable = c.reachable(d)
		list = append(list, v)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Name < list[b].Name })
	return list
}

// handlePair enrolls a phone with the single-use code printed by the
// coordinator. The request is signed by the key being enrolled.
func (c *Coordinator) handlePair(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	var req PairRequest
	if err != nil || json.Unmarshal(body, &req) != nil || req.Name == "" || len(req.Name) > 64 {
		fail(w, 400, "invalid pairing request")
		return
	}
	if verifyRequest(r, body, req.PublicKey) != nil {
		fail(w, 401, "the request must be signed by the key being paired")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pairCode == "" || time.Now().After(c.pairExpires) ||
		subtle.ConstantTimeCompare([]byte(req.Code), []byte(c.pairCode)) != 1 {
		fail(w, 403, "pairing code is wrong, used, or expired")
		return
	}
	c.pairCode = ""
	d := &Device{ID: DeviceID(req.PublicKey), Name: req.Name, Kind: KindPhone, OS: "iOS", PublicKey: req.PublicKey, CreatedAt: time.Now().Unix()}
	c.devices[d.ID] = d
	if err := c.saveDevice(d); err != nil {
		fail(w, 500, err.Error())
		return
	}
	data, _ := json.Marshal(d)
	writeJSON(w, 201, data)
}

// handleEnrollStart is called by a runner that wants to join. It gets a
// short-lived code to show as a QR; nothing is enrolled until a phone approves it.
func (c *Coordinator) handleEnrollStart(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	var req EnrollRequest
	if err != nil || json.Unmarshal(body, &req) != nil || req.Name == "" || len(req.Name) > 64 ||
		(req.Kind != KindDesktop && req.Kind != KindCloud) ||
		len(req.Tools) > 16 || len(req.Workspaces) > 64 || len(req.Apps) > 64 {
		fail(w, 400, "invalid enrollment request")
		return
	}
	for _, t := range req.Tools {
		if _, ok := Catalog[t]; !ok {
			fail(w, 400, "unknown tool "+t)
			return
		}
	}
	if verifyRequest(r, body, req.PublicKey) != nil {
		fail(w, 401, "the request must be signed by the key being enrolled")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.devices[DeviceID(req.PublicKey)]; d != nil && !d.Revoked {
		fail(w, 409, "already enrolled")
		return
	}
	// ponytail: this endpoint is unauthenticated by design, so pending
	// enrollments are capped. A flood can evict a real one; retry the QR.
	for code := range c.enrolls {
		if len(c.enrolls) < 16 {
			break
		}
		delete(c.enrolls, code)
	}
	ch := EnrollChallenge{Code: newCode(), ExpiresAt: time.Now().Add(c.cfg.EnrollTTL).Unix()}
	c.enrolls[ch.Code] = &pendingEnroll{req: req, expires: time.Unix(ch.ExpiresAt, 0)}
	data, _ := json.Marshal(ch)
	writeJSON(w, 201, data)
}

func (c *Coordinator) handleEnrollGet(r *http.Request, _ *Device, _ []byte) (int, any) {
	p := c.enrolls[r.PathValue("code")]
	if p == nil || time.Now().After(p.expires) {
		return 404, apiError{"enrollment code is wrong, used, or expired"}
	}
	return 200, p.req
}

// handleEnrollApprove binds the runner's proven public key to the account
// with the capabilities the owner just reviewed.
func (c *Coordinator) handleEnrollApprove(r *http.Request, _ *Device, _ []byte) (int, any) {
	code := r.PathValue("code")
	p := c.enrolls[code]
	if p == nil || time.Now().After(p.expires) {
		return 404, apiError{"enrollment code is wrong, used, or expired"}
	}
	delete(c.enrolls, code)
	now := time.Now()
	d := &Device{
		ID: DeviceID(p.req.PublicKey), Name: p.req.Name, Kind: p.req.Kind, OS: p.req.OS, PublicKey: p.req.PublicKey,
		Tools: p.req.Tools, Workspaces: p.req.Workspaces, Apps: p.req.Apps,
		GrantExpires: now.Add(c.cfg.GrantTTL).Unix(), CreatedAt: now.Unix(),
	}
	c.devices[d.ID] = d
	if err := c.saveDevice(d); err != nil {
		return 500, apiError{err.Error()}
	}
	return 201, d
}

func (c *Coordinator) handleJobList(*http.Request, *Device, []byte) (int, any) {
	list := []Job{}
	for _, j := range c.jobs {
		v := *j
		v.Events = nil // the list is a summary; fetch one job for its feed
		list = append(list, v)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].CreatedAt > list[b].CreatedAt })
	return 200, list[:min(len(list), 50)]
}

// handleJobGet returns a job. With ?after=N it is the event feed: it holds the
// request until the job has an event with seq > N, so a phone that reconnects
// with the last seq it saw catches up at once.
func (c *Coordinator) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := c.authenticate(w, r, KindPhone, 1<<10); !ok {
		return
	}
	after, err := strconv.Atoi(r.URL.Query().Get("after"))
	feed := err == nil
	timeout := time.After(c.cfg.PollWait)
	for {
		c.mu.Lock()
		j := c.jobs[r.PathValue("id")]
		if j == nil {
			c.mu.Unlock()
			fail(w, 404, "no such job")
			return
		}
		data, _ := json.Marshal(j)
		fresh, ch := !feed || len(j.Events) > after, c.changed
		c.mu.Unlock()
		if !fresh {
			select {
			case <-ch:
				continue
			case <-timeout:
			case <-r.Context().Done():
			}
		}
		writeJSON(w, 200, data)
		return
	}
}

func (c *Coordinator) handleClarify(r *http.Request, _ *Device, body []byte) (int, any) {
	var req struct {
		Answer string `json:"answer"`
	}
	j := c.jobs[r.PathValue("id")]
	if j == nil || json.Unmarshal(body, &req) != nil {
		return 404, apiError{"no such job"}
	}
	if j.State != StateClarification || !slices.ContainsFunc(j.Options, func(o Option) bool { return o.ID == req.Answer }) {
		return 409, apiError{"the job is not waiting for that answer"}
	}
	j.Target = req.Answer // resolved before the target and its permissions are
	c.emit(j, "state", "Clarified")
	c.saveJob(j)
	return 200, j
}

// handleApproval consumes the single-use challenge of one pending action. The
// action's parameters were fixed when it was proposed, so what runs is exactly
// what the owner reviewed.
func (c *Coordinator) handleApproval(r *http.Request, _ *Device, body []byte) (int, any) {
	var req struct {
		Challenge string `json:"challenge"`
		Approve   bool   `json:"approve"`
	}
	j := c.jobs[r.PathValue("id")]
	if j == nil || json.Unmarshal(body, &req) != nil {
		return 404, apiError{"no such job"}
	}
	for _, a := range j.Actions {
		if a.Status == ActionAwaiting && req.Challenge != "" &&
			subtle.ConstantTimeCompare([]byte(a.Challenge), []byte(req.Challenge)) == 1 &&
			time.Now().Unix() <= a.ApprovalExpires {
			c.decide(j, a, req.Approve, "rejected by the owner")
			return 200, j
		}
	}
	return 409, apiError{"no pending approval matches that challenge; it may have expired or been answered"}
}

// handleCancel records the stop request durably. The job reports "cancelled"
// only once execution has actually stopped.
func (c *Coordinator) handleCancel(r *http.Request, _ *Device, _ []byte) (int, any) {
	j := c.jobs[r.PathValue("id")]
	if j == nil {
		return 404, apiError{"no such job"}
	}
	if !terminalState(j.State) && j.State != StateCancelRequest {
		c.setState(j, StateCancelRequest, "Stop requested; waiting for execution to stop")
		c.cancelJob(j, errCancelled)
	}
	return 200, j
}

// handlePoll is the runner's long poll: heartbeat, lease renewal, and work.
func (c *Coordinator) handlePoll(w http.ResponseWriter, r *http.Request) {
	id, _, ok := c.authenticate(w, r, "runner", 1<<10)
	if !ok {
		return
	}
	timeout := time.After(c.cfg.PollWait)
	for first := true; ; first = false {
		c.mu.Lock()
		dev := c.devices[id]
		if dev == nil || dev.Revoked {
			c.mu.Unlock()
			fail(w, http.StatusUnauthorized, "revoked")
			return
		}
		resp, reconnected := c.pollOnce(dev)
		ch := c.changed
		c.mu.Unlock()
		// A runner that just (re)connected is answered at once so it can confirm.
		if len(resp.Actions)+len(resp.Cancel) == 0 && !(first && reconnected) {
			select {
			case <-ch:
				continue
			case <-timeout:
			case <-r.Context().Done():
			}
		}
		data, _ := json.Marshal(resp)
		writeJSON(w, 200, data)
		return
	}
}

// handleTranscribe forwards the phone's audio to Deepgram so the API key stays
// on the coordinator. The audio is held in memory only and never stored.
func (c *Coordinator) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	_, audio, ok := c.authenticate(w, r, KindPhone, 25<<20)
	if !ok {
		return
	}
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg.DeepgramKey == "" {
		fail(w, http.StatusNotImplemented, "Deepgram is not configured on the coordinator (set DEEPGRAM_API_KEY). Use on-device transcription.")
		return
	}
	text, err := transcribe(r.Context(), cfg, r.Header.Get("Content-Type"), audio)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	data, _ := json.Marshal(map[string]string{"transcript": text})
	writeJSON(w, 200, data)
}

// transcribe sends pre-recorded audio to Deepgram's /v1/listen endpoint.
func transcribe(ctx context.Context, cfg Config, contentType string, audio []byte) (string, error) {
	q := url.Values{"model": {cfg.DeepgramModel}, "smart_format": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.DeepgramURL+"/v1/listen?"+q.Encode(), bytes.NewReader(audio))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Token "+cfg.DeepgramKey)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("deepgram: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("deepgram: status %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Results struct {
			Channels []struct {
				Alternatives []struct {
					Transcript string `json:"transcript"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("deepgram: %w", err)
	}
	if len(out.Results.Channels) == 0 || len(out.Results.Channels[0].Alternatives) == 0 {
		return "", nil
	}
	return out.Results.Channels[0].Alternatives[0].Transcript, nil
}
