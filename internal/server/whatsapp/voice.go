package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Voice-note replies: the AI's answer as speech, encoded to Ogg Opus (what WhatsApp
// voice notes are) by ffmpeg, which Go has no encoder for.

// CanSpeak reports whether voice-note replies can be made here: ffmpeg is installed.
var CanSpeak = func() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func (s *Service) sendVoice(ctx context.Context, cl *whatsmeow.Client, to types.JID, text string) error {
	if s.Speak == nil {
		return errors.New("no speech")
	}
	if !CanSpeak() {
		return errors.New("ffmpeg is not installed")
	}
	pcm, rate, err := s.Speak(ctx, text)
	if err != nil {
		return err
	}
	ogg, err := toOpus(ctx, pcm, rate)
	if err != nil {
		return err
	}
	up, err := cl.Upload(ctx, ogg, whatsmeow.MediaAudio)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	seconds := uint32(len(pcm) / 2 / max(rate, 1))
	_, err = cl.SendMessage(ctx, to, &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		Mimetype: proto.String("audio/ogg; codecs=opus"), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
		FileLength: proto.Uint64(up.FileLength), Seconds: proto.Uint32(seconds), PTT: proto.Bool(true),
	}})
	return err
}

// toOpus encodes 16-bit mono PCM to an Ogg Opus voice note.
func toOpus(ctx context.Context, pcm []byte, rate int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "s16le", "-ar", strconv.Itoa(rate), "-ac", "1", "-i", "pipe:0",
		"-c:a", "libopus", "-b:a", "32k", "-application", "voip", "-f", "ogg", "pipe:1")
	cmd.Stdin = bytes.NewReader(pcm)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v %s", err, errOut.String())
	}
	return out.Bytes(), nil
}

// SendVoice sends someone a voice note reading text out (ffmpeg and the AI needed).
func (s *Service) SendVoice(ctx context.Context, phone, text string) error {
	digits, err := CleanPhone(phone)
	if err != nil {
		return err
	}
	cl, err := s.connected()
	if err != nil {
		return err
	}
	return s.sendVoice(ctx, cl, types.NewJID(digits, types.DefaultUserServer), text)
}
