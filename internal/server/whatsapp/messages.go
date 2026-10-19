package whatsapp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A team member writes to the linked number, or sends a voice note; the dashboard
// answers (with the AI). Messages from numbers nobody saved are ignored: no reply.

const maxVoiceNote = 15 << 20

func (s *Service) onMessage(cl *whatsmeow.Client, evt *events.Message) {
	info := evt.Info
	if s.OnMessage == nil || cl == nil || info.IsFromMe || info.IsGroup || info.Chat.Server == types.BroadcastServer ||
		time.Since(info.Timestamp) > 5*time.Minute { // the backlog after a reconnect is not answered
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	person, _ := s.callerOf(ctx, cl, info.Sender)
	if person == "" && !info.SenderAlt.IsEmpty() {
		person, _ = s.callerOf(ctx, cl, info.SenderAlt)
	}
	if person == "" {
		slog.Info("whatsapp: ignored a message from a number nobody saved")
		return
	}
	msg := evt.Message
	text := strings.TrimSpace(msg.GetConversation())
	if text == "" {
		text = strings.TrimSpace(msg.GetExtendedTextMessage().GetText())
	}
	var audio []byte
	mime := ""
	if am := msg.GetAudioMessage(); am != nil && am.GetFileLength() < maxVoiceNote {
		b, err := cl.Download(ctx, am)
		if err != nil {
			slog.Warn("whatsapp: voice note", "err", err)
		} else {
			audio, mime = b, strings.TrimSpace(strings.Split(am.GetMimetype(), ";")[0])
		}
	}
	if text == "" && audio == nil {
		return // stickers, pictures…: nothing to answer
	}
	cl.MarkRead(ctx, []types.MessageID{info.ID}, info.Timestamp, info.Chat, info.Sender)
	cl.SendChatPresence(ctx, info.Chat, types.ChatPresenceComposing, "")
	reply := s.OnMessage(ctx, person, text, audio, mime)
	cl.SendChatPresence(ctx, info.Chat, types.ChatPresencePaused, "")
	if reply == "" {
		return
	}
	if _, err := cl.SendMessage(ctx, info.Chat, &waE2E.Message{Conversation: proto.String(reply)}); err != nil {
		slog.Warn("whatsapp: reply", "err", err)
	}
}
