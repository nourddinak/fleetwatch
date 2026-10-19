package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ffmpeg, for voice-note answers, is installed by the dashboard's root helper (the
// ops-server-update unit, which runs "ops-server self-update"): the dashboard asks
// through the same request file as updates and Caddy, and the helper reports back.

const (
	requestFile   = "update-request" // shared with release and statuspage
	ffmpegRequest = "ffmpeg"
	ffmpegResult  = "ffmpeg-status.json"
)

// VoiceSetup says where voice-note answers stand.
type VoiceSetup struct {
	State string `json:"state"` // ready | installing | failed | manual (no helper: install it by hand)
	Error string `json:"error,omitempty"`
}

type ffmpegStatus struct {
	TS    int64  `json:"ts"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Service) VoiceSetup() VoiceSetup {
	if CanSpeak() {
		return VoiceSetup{State: "ready"}
	}
	if s.DataDir == "" || s.HelperPath == "" {
		return VoiceSetup{State: "manual"}
	}
	if _, err := os.Stat(s.HelperPath); err != nil {
		return VoiceSetup{State: "manual"}
	}
	if FFmpegRequested(s.DataDir) {
		return VoiceSetup{State: "installing"}
	}
	var st ffmpegStatus
	if raw, err := os.ReadFile(filepath.Join(s.DataDir, ffmpegResult)); err == nil && json.Unmarshal(raw, &st) == nil && !st.OK {
		return VoiceSetup{State: "failed", Error: st.Error}
	}
	return VoiceSetup{State: "installing"} // asked for, the helper has not started yet
}

// wantsVoice reports whether someone asked for voice-note answers.
func (s *Service) wantsVoice() bool {
	for _, p := range s.People() {
		if p.Replies != "" {
			return true
		}
	}
	return false
}

// EnsureVoice asks the helper for ffmpeg when someone wants voice answers and it is
// missing. An update the helper is busy with goes first; it asks again after.
func (s *Service) EnsureVoice() {
	if CanSpeak() || !s.wantsVoice() || s.DataDir == "" || s.HelperPath == "" {
		return
	}
	if _, err := os.Stat(s.HelperPath); err != nil {
		return // no helper: the page says to install it by hand
	}
	var st ffmpegStatus
	if raw, err := os.ReadFile(filepath.Join(s.DataDir, ffmpegResult)); err == nil && json.Unmarshal(raw, &st) == nil &&
		!st.OK && time.Since(time.UnixMilli(st.TS)) < 6*time.Hour {
		return // it failed lately: no retry loop
	}
	go func() {
		req := filepath.Join(s.DataDir, requestFile)
		for range 60 {
			raw, err := os.ReadFile(req)
			if err == nil && strings.TrimSpace(string(raw)) == ffmpegRequest {
				return // already asked
			}
			if err != nil { // the helper is free
				tmp := req + ".tmp"
				if os.WriteFile(tmp, []byte(ffmpegRequest+"\n"), 0o644) == nil && os.Rename(tmp, req) == nil {
					return
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

// FFmpegRequested reports whether the helper was started to install ffmpeg.
func FFmpegRequested(dataDir string) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, requestFile))
	return err == nil && strings.TrimSpace(string(raw)) == ffmpegRequest
}

// installer is the package manager command for ffmpeg here; tests replace it.
var installer = func(ctx context.Context) ([]byte, error) {
	if _, err := exec.LookPath("apt-get"); err == nil {
		if out, err := exec.CommandContext(ctx, "apt-get", "update", "-qq").CombinedOutput(); err != nil {
			return out, err
		}
		cmd := exec.CommandContext(ctx, "apt-get", "install", "-y", "-qq", "--no-install-recommends", "ffmpeg")
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		return cmd.CombinedOutput()
	}
	for _, pm := range []string{"dnf", "yum", "apk", "pacman"} {
		if _, err := exec.LookPath(pm); err == nil {
			args := map[string][]string{"dnf": {"install", "-y", "ffmpeg"}, "yum": {"install", "-y", "ffmpeg"}, "apk": {"add", "ffmpeg"}, "pacman": {"-S", "--noconfirm", "ffmpeg"}}[pm]
			return exec.CommandContext(ctx, pm, args...).CombinedOutput()
		}
	}
	return nil, errors.New("no package manager found (apt, dnf, yum, apk, pacman): install ffmpeg yourself")
}

// InstallFFmpeg runs as root in the helper, and writes how it went for the dashboard.
func InstallFFmpeg(dataDir string) error {
	defer os.Remove(filepath.Join(dataDir, requestFile))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	st := ffmpegStatus{TS: time.Now().UnixMilli(), OK: true}
	if !CanSpeak() {
		if out, err := installer(ctx); err != nil {
			st.OK, st.Error = false, strings.TrimSpace(lastLines(string(out), 3)+" "+err.Error())
		}
	}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(dataDir, ffmpegResult), b, 0o644)
	if !st.OK {
		return errors.New("ffmpeg: " + st.Error)
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], " ")
}
