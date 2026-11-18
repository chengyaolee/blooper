package remote

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"time"
)

// ProviderStatus deliberately contains no credential material.
type ProviderStatus struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Connected bool   `json:"connected"`
	Deepgram  bool   `json:"deepgram_connected"`
}

func (c *Coordinator) SetTranscriptionKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.DeepgramKey = key
}
func (c *Coordinator) Devices() []Device {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deviceList()
}
func (c *Coordinator) Revoke(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revoke(id)
}
func (c *Coordinator) HasActiveJobs() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, j := range c.jobs {
		if !terminalState(j.State) {
			return true
		}
	}
	return false
}

// RegisterLocalRunner is called only by the owning desktop runtime, never by
// network clients. Other computers still need the phone's enrollment approval.
func (c *Coordinator) RegisterLocalRunner(req EnrollRequest) error {
	if len(req.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid runner identity")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &Device{AutoApproveRoutine: req.AutoApproveRoutine, ID: DeviceID(req.PublicKey), Name: req.Name, Kind: KindDesktop, OS: req.OS, PublicKey: req.PublicKey,
		Tools: req.Tools, Workspaces: req.Workspaces, Apps: req.Apps, GrantExpires: time.Now().Add(c.cfg.GrantTTL).Unix(), CreatedAt: time.Now().Unix()}
	c.devices[d.ID] = d
	return c.saveDevice(d)
}

// ValidateDeepgramKey transcribes one second of generated silence. No user
// audio is uploaded; a valid key must have permission to use transcription.
func ValidateDeepgramKey(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("Enter a Deepgram API key")
	}
	audio := make([]byte, 44+16000*2)
	copy(audio, "RIFF")
	binary.LittleEndian.PutUint32(audio[4:8], uint32(len(audio)-8))
	copy(audio[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(audio[16:20], 16)
	binary.LittleEndian.PutUint16(audio[20:22], 1)
	binary.LittleEndian.PutUint16(audio[22:24], 1)
	binary.LittleEndian.PutUint32(audio[24:28], 16000)
	binary.LittleEndian.PutUint32(audio[28:32], 32000)
	binary.LittleEndian.PutUint16(audio[32:34], 2)
	binary.LittleEndian.PutUint16(audio[34:36], 16)
	copy(audio[36:], "data")
	binary.LittleEndian.PutUint32(audio[40:44], uint32(len(audio)-44))
	cfg := Config{DeepgramKey: key}
	cfg.defaults()
	_, err := transcribe(ctx, cfg, "audio/wav", audio)
	return err
}
