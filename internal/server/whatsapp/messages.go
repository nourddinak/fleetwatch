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
	if s.OnMessage == nil || cl == nil || info.IsFromMe || info.Chat.Server == types.BroadcastServer ||
		time.Since(info.Timestamp) > 5*time.Minute { // the backlog after a reconnect is not answered
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	group := info.IsGroup
	if group { // only the team group, and only what is for the AI
		if g := s.Group(); g.JID == "" || !g.Answer || info.Chat.String() != g.JID || !forBot(cl, evt.Message) {
			return
		}
		ctx = WithGroup(ctx)
	}
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
	if group {
		text, audio = strings.TrimSpace(mentionRe.ReplaceAllString(text, "")), nil
		if text == "" {
			return
		}
		text = "(Asked in the team group, where everyone reads your answer.)\n" + text
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
	if group { // in the group: a reply quoting the question
		q := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(reply), ContextInfo: &waE2E.ContextInfo{
			StanzaID: proto.String(info.ID), Participant: proto.String(info.Sender.String()), QuotedMessage: evt.Message}}}
		if _, err := cl.SendMessage(ctx, info.Chat, q); err != nil {
			slog.Warn("whatsapp: group reply", "err", err)
		}
		return
	}
	if p, _ := s.PersonOf(person); p.Replies == "always" || (p.Replies == "voice" && audio != nil) {
		if err := s.sendVoice(ctx, cl, info.Chat, reply); err == nil {
			return
		} else {
			slog.Warn("whatsapp: voice reply, sent as text", "err", err)
		}
	}
	if _, err := cl.SendMessage(ctx, info.Chat, &waE2E.Message{Conversation: proto.String(reply)}); err != nil {
		slog.Warn("whatsapp: reply", "err", err)
	}
}
