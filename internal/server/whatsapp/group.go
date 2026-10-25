package whatsapp

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// The team group: a WhatsApp group the dashboard made with the team. Incidents are
// posted there, and the AI answers whoever mentions the linked number (or replies to
// one of its messages) in it.

type Group struct {
	JID       string `json:"jid,omitempty"`
	Name      string `json:"name,omitempty"`
	Incidents string `json:"incidents"` // "" none | critical | all: which incidents are posted
	Answer    bool   `json:"answer"`    // the AI answers mentions
	Invite    string `json:"invite,omitempty"`
}

func (s *Service) Group() Group {
	var g Group
	settings.Load(s.db, "whatsapp.group", &g)
	return g
}

func (s *Service) SetGroupOptions(incidents string, answer bool) (Group, error) {
	if incidents != "" && incidents != "critical" && incidents != "all" {
		return Group{}, valid.Error("post no incidents, critical ones, or all")
	}
	g := s.Group()
	if g.JID == "" {
		return g, valid.Error("there is no team group yet")
	}
	g.Incidents, g.Answer = incidents, answer
	return g, settings.Save(s.db, "whatsapp.group", g)
}

// CreateGroup makes the team group with these phone numbers. Those WhatsApp did not
// let in directly (their privacy settings) are listed; the invite link lets them join.
func (s *Service) CreateGroup(ctx context.Context, name string, phones []string) (Group, []string, error) {
	cl, err := s.connected()
	if err != nil {
		return Group{}, nil, err
	}
	if g := s.Group(); g.JID != "" {
		return g, nil, valid.Error("there is a team group already")
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 25 {
		return Group{}, nil, valid.Error("a group name of 1 to 25 characters")
	}
	info, err := cl.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: name, Participants: s.jids(cl, phones)})
	if err != nil {
		return Group{}, nil, err
	}
	g := Group{JID: info.JID.String(), Name: name, Incidents: "critical", Answer: true}
	g.Invite, _ = cl.GetGroupInviteLink(ctx, info.JID, false)
	var missing []string
	in := map[string]bool{}
	for _, p := range info.Participants {
		if p.Error == 0 {
			in[p.JID.User] = true
			if !p.PhoneNumber.IsEmpty() {
				in[p.PhoneNumber.User] = true
			}
		}
	}
	for _, ph := range phones {
		if d, err := CleanPhone(ph); err == nil && !in[d] && d != cl.Store.ID.User {
			missing = append(missing, "+"+d)
		}
	}
	return g, missing, settings.Save(s.db, "whatsapp.group", g)
}

// AddToGroup adds phone numbers (new teammates) to the team group.
func (s *Service) AddToGroup(ctx context.Context, phones []string) error {
	cl, err := s.connected()
	if err != nil {
		return err
	}
	g := s.Group()
	jid, err := types.ParseJID(g.JID)
	if g.JID == "" || err != nil {
		return valid.Error("there is no team group yet")
	}
	_, err = cl.UpdateGroupParticipants(ctx, jid, s.jids(cl, phones), whatsmeow.ParticipantChangeAdd)
	return err
}

// ForgetGroup stops using the team group, and leaves it.
func (s *Service) ForgetGroup(ctx context.Context) error {
	g := s.Group()
	if jid, err := types.ParseJID(g.JID); err == nil && g.JID != "" {
		if cl, err := s.connected(); err == nil {
			cl.LeaveGroup(ctx, jid)
		}
	}
	return settings.Save(s.db, "whatsapp.group", Group{})
}

// PostToGroup writes in the team group (nothing when there is none).
func (s *Service) PostToGroup(ctx context.Context, text string) error {
	g := s.Group()
	jid, err := types.ParseJID(g.JID)
	if g.JID == "" || err != nil {
		return nil
	}
	cl, err := s.connected()
	if err != nil {
		return err
	}
	_, err = cl.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	return err
}

func (s *Service) connected() (*whatsmeow.Client, error) {
	if s.Paused() {
		return nil, ErrPaused
	}
	s.mu.Lock()
	cl := s.client
	s.mu.Unlock()
	if cl == nil || cl.Store.ID == nil {
		return nil, ErrNotLinked
	}
	if !cl.IsConnected() {
		return nil, errors.New("WhatsApp is not connected right now")
	}
	return cl, nil
}

func (s *Service) jids(cl *whatsmeow.Client, phones []string) []types.JID {
	var out []types.JID
	for _, ph := range phones {
		if d, err := CleanPhone(ph); err == nil && d != cl.Store.ID.User {
			out = append(out, types.NewJID(d, types.DefaultUserServer))
		}
	}
	return out
}

// ---------- mentions in the group ----------

type groupKey struct{}

// InGroup reports whether a message being answered came from the team group: no
// PINs there (everyone would read them).
func InGroup(ctx context.Context) bool { v, _ := ctx.Value(groupKey{}).(bool); return v }

// WithGroup marks a message as coming from the team group.
func WithGroup(ctx context.Context) context.Context { return context.WithValue(ctx, groupKey{}, true) }

var mentionRe = regexp.MustCompile(`@\d{5,}`)

// forBot reports whether a group message is for the AI: it mentions the linked number
// or replies to one of its messages.
func forBot(cl *whatsmeow.Client, msg *waE2E.Message) bool {
	ci := msg.GetExtendedTextMessage().GetContextInfo()
	if ci == nil {
		return false
	}
	me := map[string]bool{cl.Store.ID.User: true}
	if !cl.Store.LID.IsEmpty() {
		me[cl.Store.LID.User] = true
	}
	for _, m := range ci.GetMentionedJID() {
		if j, err := types.ParseJID(m); err == nil && me[j.User] {
			return true
		}
	}
	if j, err := types.ParseJID(ci.GetParticipant()); err == nil && me[j.User] && ci.GetQuotedMessage() != nil {
		return true
	}
	return false
}
