package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

type fakeSender struct {
	mu   sync.Mutex
	sent [][]byte
	err  error
}

func (f *fakeSender) SendRaw(_ context.Context, _, _ string, _ []string, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, raw)
	return nil
}

type fakeDNS struct {
	mx  map[string][]*net.MX
	txt map[string][]string
}

func (f fakeDNS) LookupMX(_ context.Context, n string) ([]*net.MX, error) {
	if v, ok := f.mx[n]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: n, IsNotFound: true}
}
func (f fakeDNS) LookupTXT(_ context.Context, n string) ([]string, error) {
	if v, ok := f.txt[n]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: n, IsNotFound: true}
}

func newSvc(t *testing.T) (*Service, *fakeSender) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	s, err := New(d, box, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSender{}
	s.Mail = f
	s.SaveOptions(Options{UndoSeconds: 0})
	return s, f
}

func setup(t *testing.T, s *Service) (Domain, Address) {
	t.Helper()
	d, err := s.AddDomain("Nashop.store", "")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.UpdateDomain(d.ID, DomainUpdate{ForwardTo: []string{"me@gmail.com"}, SpamThreshold: 5})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAddress(AddressInput{DomainID: d.ID, Local: "contact", DisplayName: "Nashop", Receive: true, Send: true, Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	return d, a
}

func msg(from, to, subject, extra, body string) []byte {
	return []byte("From: Jane <" + from + ">\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nDate: Mon, 02 Nov 2026 10:00:00 +0000\r\n" +
		"Authentication-Results: mx.cloudflare.net; spf=pass; dkim=pass; dmarc=pass\r\n" + extra + "\r\n" + body)
}

func TestResolveAliasCatchAllAndForward(t *testing.T) {
	s, _ := newSvc(t)
	d, contact := setup(t, s)
	hello, err := s.CreateAddress(AddressInput{DomainID: d.ID, Local: "hello", InboxID: contact.ID, Receive: true, ForwardMode: "custom", ForwardTo: []string{"a@gmail.com", "b@gmail.com"}})
	if err != nil {
		t.Fatal(err)
	}
	noreply, _ := s.CreateAddress(AddressInput{DomainID: d.ID, Local: "noreply", Receive: true, Send: true, ForwardMode: "off"})

	res, err := s.Ingest(Inbound{Rcpt: "hello@nashop.store"}, msg("jane@example.com", "hello@nashop.store", "Hi", "Message-ID: <a1@example.com>\r\n", "body"))
	if err != nil || !res.Stored || strings.Join(res.Forward, ",") != "a@gmail.com,b@gmail.com" {
		t.Fatalf("alias: %+v %v", res, err)
	}
	page, _ := s.List(OwnerAccess, Query{Folder: "inbox", Mailbox: contact.ID})
	if len(page.Messages) != 1 || page.Messages[0].Rcpt != "hello@nashop.store" || page.Messages[0].MailboxID != contact.ID {
		t.Fatalf("alias should land in contact's inbox keeping its address: %+v", page.Messages)
	}
	if res, _ := s.Ingest(Inbound{Rcpt: "noreply@nashop.store"}, msg("x@example.com", "noreply@nashop.store", "x", "", "x")); len(res.Forward) != 0 {
		t.Fatalf("forwarding off still forwards: %v", res.Forward)
	}
	if res, _ := s.Ingest(Inbound{Rcpt: "contact+shop@nashop.store"}, msg("x@example.com", "contact@nashop.store", "plus", "", "x")); !res.Stored || res.Forward[0] != "me@gmail.com" {
		t.Fatalf("plus addressing: %+v", res)
	}
	if res, _ := s.Ingest(Inbound{Rcpt: "nobody@nashop.store"}, msg("x@example.com", "nobody@nashop.store", "x", "", "x")); res.Reject == "" {
		t.Fatal("unknown address accepted without catch-all")
	}
	if _, err := s.UpdateDomain(d.ID, DomainUpdate{CatchAll: true, CatchAllTo: noreply.ID, ForwardTo: d.ForwardTo, SpamThreshold: 5}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Ingest(Inbound{Rcpt: "nobody@nashop.store"}, msg("x@example.com", "nobody@nashop.store", "x2", "", "x")); !res.Stored {
		t.Fatalf("catch-all: %+v", res)
	}
	s.UpdateAddress(hello.ID, AddressInput{InboxID: contact.ID, Disabled: true, Receive: true})
	if res, _ := s.Ingest(Inbound{Rcpt: "hello@nashop.store"}, msg("x@example.com", "hello@nashop.store", "x3", "", "x")); res.Reject == "" {
		t.Fatal("a disabled address took mail")
	}
	if res, _ := s.Ingest(Inbound{Rcpt: "x@other.com"}, msg("x@example.com", "x@other.com", "x", "", "x")); res.Reject == "" {
		t.Fatal("a domain that is not ours took mail")
	}
	if err := s.DeleteAddress(contact.ID); err == nil {
		t.Fatal("deleted an address that still has mail")
	}
}

func TestDedupeSizeAndRawKept(t *testing.T) {
	s, _ := newSvc(t)
	_, a := setup(t, s)
	raw := msg("jane@example.com", "contact@nashop.store", "Order", "Message-ID: <o1@example.com>\r\n", "hello")
	r1, err := s.Ingest(Inbound{Rcpt: a.Address}, raw)
	r2, err2 := s.Ingest(Inbound{Rcpt: a.Address}, raw)
	if err != nil || err2 != nil || !r2.Duplicate {
		t.Fatalf("dedupe: %+v %+v %v %v", r1, r2, err, err2)
	}
	page, _ := s.List(OwnerAccess, Query{})
	if len(page.Messages) != 1 {
		t.Fatalf("stored twice: %d", len(page.Messages))
	}
	got, err := s.Raw(page.Messages[0].ID)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("the original is not kept as is: %v", err)
	}
	big := make([]byte, MaxMessage+1)
	if res, _ := s.Ingest(Inbound{Rcpt: a.Address}, big); res.Reject == "" {
		t.Fatal("a message over 25 MiB was accepted")
	}
}

func TestParseMultipart(t *testing.T) {
	raw := "From: =?utf-8?q?Ren=C3=A9?= <rene@example.com>\r\nTo: a@x.com, \"B\" <b@x.com>\r\nCc: c@x.com\r\nSubject: =?iso-8859-1?q?caf=E9?=\r\n" +
		"Message-ID: <m1@x>\r\nIn-Reply-To: <m0@x>\r\nReferences: <r1@x> <m0@x>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=OUT\r\n\r\n" +
		"--OUT\r\nContent-Type: multipart/alternative; boundary=ALT\r\n\r\n" +
		"--ALT\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nBonjour caf=E9\r\n" +
		"--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Bonjour <b>café</b></p>\r\n--ALT--\r\n" +
		"--OUT\r\nContent-Type: application/pdf; name=\"inv.pdf\"\r\nContent-Disposition: attachment; filename=\"inv.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nSGVs\r\nbG8=\r\n" +
		"--OUT\r\nContent-Type: image/png\r\nContent-ID: <logo1>\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw==\r\n--OUT--\r\n"
	p := Parse([]byte(raw), true)
	if p.From.Name != "René" || p.Subject != "café" || len(p.To) != 2 || p.To[1].Name != "B" || len(p.Cc) != 1 {
		t.Fatalf("headers: %+v", p)
	}
	if p.MessageID != "<m1@x>" || p.InReplyTo != "<m0@x>" || len(p.References) != 2 {
		t.Fatalf("ids: %+v", p)
	}
	if !strings.Contains(p.Text, "Bonjour café") || !strings.Contains(p.HTML, "<b>café</b>") {
		t.Fatalf("bodies: %q %q", p.Text, p.HTML)
	}
	if len(p.Attachments) != 2 || p.Attachments[0].Filename != "inv.pdf" || string(p.Attachments[0].Data) != "Hello" || !p.Attachments[1].Inline || p.Attachments[1].ContentID != "logo1" {
		t.Fatalf("attachments: %+v", p.Attachments)
	}
	if Parse([]byte("garbage without headers"), false).Subject == "" {
		t.Fatal("a broken message must still parse to something")
	}
}

func TestThreadingAndSearch(t *testing.T) {
	s, _ := newSvc(t)
	_, a := setup(t, s)
	s.Ingest(Inbound{Rcpt: a.Address}, msg("jane@example.com", a.Address, "Invoice 42", "Message-ID: <t1@ex>\r\n", "Please find the invoice for the blue kettle"))
	s.Ingest(Inbound{Rcpt: a.Address}, msg("jane@example.com", a.Address, "Re: Invoice 42", "Message-ID: <t2@ex>\r\nIn-Reply-To: <t1@ex>\r\n", "follow up"))
	s.Ingest(Inbound{Rcpt: a.Address}, msg("jane@example.com", a.Address, "RE: invoice 42", "Message-ID: <t3@ex>\r\n", "no headers, same subject"))
	s.Ingest(Inbound{Rcpt: a.Address}, msg("bob@example.com", a.Address, "Something else", "Message-ID: <t4@ex>\r\n", "kettles"))
	page, _ := s.List(OwnerAccess, Query{Folder: "inbox"})
	threads := map[int64]int{}
	for _, m := range page.Messages {
		threads[m.ThreadID]++
	}
	if len(threads) != 2 {
		t.Fatalf("want 2 conversations, got %v", threads)
	}
	for q, want := range map[string]int{"kettle": 2, "blue kettle": 1, "from:bob": 1, "subject:invoice": 3, `"follow up"`: 1, "is:unread": 4, "nothing-like-this": 0} {
		p, err := s.List(OwnerAccess, Query{Search: q})
		if err != nil || len(p.Messages) != want {
			t.Errorf("search %q: %d (want %d) %v", q, len(p.Messages), want, err)
		}
	}
	// Paging walks every message once.
	p1, _ := s.List(OwnerAccess, Query{Limit: 3})
	p2, _ := s.List(OwnerAccess, Query{Limit: 3, Cursor: p1.Next})
	if len(p1.Messages) != 3 || len(p2.Messages) != 1 || p2.Next != "" {
		t.Fatalf("paging: %d %d %q", len(p1.Messages), len(p2.Messages), p2.Next)
	}
}

func TestSpamScoringAndLists(t *testing.T) {
	mine := []string{"nashop.store"}
	clean := Parse(msg("jane@example.com", "c@nashop.store", "Hello", "", "hi"), false)
	if sc, _ := Score(clean, mine); sc >= 5 {
		t.Fatalf("clean mail scored %v", sc)
	}
	phish := Parse([]byte("From: \"nashop.store support\" <x@evil.io>\r\nSubject: Verify your account\r\nAuthentication-Results: mx.cloudflare.net; spf=fail; dkim=fail; dmarc=fail\r\nContent-Type: text/html\r\n\r\n"+
		`<a href="http://203.0.113.9/login">https://nashop.store/login</a>`), false)
	sc, why := Score(phish, mine)
	if sc < 10 || len(why) < 4 {
		t.Fatalf("phishing scored %v %v", sc, why)
	}
	missing := Parse([]byte("From: a@b.com\r\nSubject: x\r\n\r\nx"), false)
	if sc, _ := Score(missing, mine); sc >= 1 {
		t.Fatalf("missing authentication must count far less than failing: %v", sc)
	}
	forged := Parse([]byte("From: a@b.com\r\nAuthentication-Results: mx.cloudflare.net; spf=fail; dmarc=fail\r\nAuthentication-Results: fake; spf=pass; dmarc=pass\r\n\r\nx"), false)
	if forged.Auth.SPF != "fail" {
		t.Fatalf("only the topmost result counts: %+v", forged.Auth)
	}

	s, _ := newSvc(t)
	_, a := setup(t, s)
	res, _ := s.Ingest(Inbound{Rcpt: a.Address}, []byte("From: \"nashop.store\" <x@evil.io>\r\nTo: "+a.Address+"\r\nSubject: Verify your account\r\nAuthentication-Results: mx.cloudflare.net; spf=fail; dkim=fail; dmarc=fail\r\n\r\nhttp://203.0.113.9/"))
	if !res.Stored || len(res.Forward) != 0 {
		t.Fatalf("spam must be stored but not copied to Gmail: %+v", res)
	}
	spam, _ := s.List(OwnerAccess, Query{Folder: "spam"})
	if len(spam.Messages) != 1 {
		t.Fatal("spam did not land in Spam")
	}
	if _, err := s.Act(OwnerAccess, Action{IDs: []int64{spam.Messages[0].ID}, Action: "notspam"}); err != nil {
		t.Fatal(err)
	}
	if s.listed("x@evil.io", a) != "allow" {
		t.Fatal("not spam should trust the sender")
	}
	s.AddSpamRule(SpamRule{Pattern: "@blocked.example", Action: "block"})
	s.Ingest(Inbound{Rcpt: a.Address}, msg("y@blocked.example", a.Address, "hi", "", "hi"))
	if spam, _ := s.List(OwnerAccess, Query{Folder: "spam"}); len(spam.Messages) != 1 {
		t.Fatal("a blocked sender must go to Spam (kept, not deleted)")
	}
}

func TestPermissionsLimitWhatMembersSee(t *testing.T) {
	s, _ := newSvc(t)
	d, contact := setup(t, s)
	d2, _ := s.AddDomain("other.site", "")
	admin, _ := s.CreateAddress(AddressInput{DomainID: d2.ID, Local: "admin", Receive: true, Send: true})
	s.Ingest(Inbound{Rcpt: contact.Address}, msg("a@ex.com", contact.Address, "for contact", "", "x"))
	s.Ingest(Inbound{Rcpt: admin.Address}, msg("a@ex.com", admin.Address, "for admin", "", "x"))
	s.db.Exec(`INSERT INTO members (id, username, password_hash, role, created_at) VALUES ('m1', 'ops', 'x', 'operator', 0), ('m2', 'view', 'x', 'viewer', 0)`)
	if err := s.SetGrants("m2", "viewer", []Grant{{ScopeKind: "domain", ScopeID: d.ID, Level: "read"}}); err == nil {
		t.Fatal("a viewer was given mail")
	}
	if err := s.SetGrants("m1", "operator", []Grant{{ScopeKind: "domain", ScopeID: d.ID, Level: "read"}}); err != nil {
		t.Fatal(err)
	}
	acc := s.For("m1", false, true)
	page, _ := s.List(acc, Query{})
	if len(page.Messages) != 1 || page.Messages[0].Subject != "for contact" {
		t.Fatalf("member sees %+v", page.Messages)
	}
	all, _ := s.List(OwnerAccess, Query{})
	for _, m := range all.Messages {
		_, err := s.Get(acc, m.ID)
		if (m.MailboxID == admin.ID) != (err != nil) {
			t.Fatalf("Get by id leaks or hides wrongly: %s %v", m.Subject, err)
		}
		if m.MailboxID == admin.ID {
			if n, _ := s.Act(acc, Action{IDs: []int64{m.ID}, Action: "trash"}); n != 0 {
				t.Fatal("member changed mail they cannot see")
			}
		}
	}
	if s.For("m1", false, false).Any() {
		t.Fatal("a non-operator has mail access")
	}
	if acc.AddressCan(contact.ID, "send") {
		t.Fatal("read must not allow sending")
	}
	if _, err := s.SaveDraft(acc, Draft{FromID: contact.ID}, "ops"); err == nil {
		t.Fatal("read-only member saved a draft")
	}
	if _, err := s.SearchAs(acc, "for admin"); err != nil {
		t.Fatal(err)
	}
}

// SearchAs is List with a search, for the test above.
func (s *Service) SearchAs(a Access, q string) (int, error) {
	p, err := s.List(a, Query{Search: q})
	if err == nil && len(p.Messages) > 0 && !a.Owner {
		for _, m := range p.Messages {
			if !a.Mailbox(m.MailboxID, "read") {
				return 0, fmt.Errorf("search leaked %s", m.Subject)
			}
		}
	}
	return len(p.Messages), err
}

func TestSendUndoAndNoDoubleSend(t *testing.T) {
	s, f := newSvc(t)
	_, a := setup(t, s)
	s.Ingest(Inbound{Rcpt: a.Address}, msg("jane@example.com", a.Address, "Question", "Message-ID: <q1@ex>\r\n", "hi?"))
	in, _ := s.List(OwnerAccess, Query{Folder: "inbox"})
	up, err := s.Upload("note.txt", "text/plain", []byte("attached"))
	if err != nil {
		t.Fatal(err)
	}
	dr, err := s.SaveDraft(OwnerAccess, Draft{FromID: a.ID, To: []string{"Jane <jane@example.com>"}, Bcc: []string{"boss@example.com"}, Subject: "Re: Question", HTML: "<p>Yes <script>x</script></p>", ReplyToID: in.Messages[0].ID, Files: []File{up}}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Undo: with a delay it can be taken back.
	s.SaveOptions(Options{UndoSeconds: 30})
	j, err := s.Send(OwnerAccess, dr.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := s.Cancel(OwnerAccess, j.ID); err != nil || back != dr.ID {
		t.Fatalf("undo: %v %v", back, err)
	}
	if ds, _ := s.Drafts(OwnerAccess); len(ds) != 1 {
		t.Fatal("undone message is not back in Drafts")
	}
	s.SaveOptions(Options{UndoSeconds: 0})
	j, err = s.Send(OwnerAccess, dr.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.sendDue(context.Background())
	if len(f.sent) != 1 {
		t.Fatalf("sent %d", len(f.sent))
	}
	raw := string(f.sent[0])
	for _, want := range []string{"From: \"Nashop\" <contact@nashop.store>", "In-Reply-To: <q1@ex>", "References: <q1@ex>", "multipart/mixed", "note.txt"} {
		if !strings.Contains(raw, want) {
			t.Errorf("sent message lacks %q", want)
		}
	}
	if strings.Contains(raw, "boss@example.com") || strings.Contains(raw, "<script>") {
		t.Fatal("Bcc header or script leaked into the message")
	}
	if _, err := s.Cancel(OwnerAccess, j.ID); err == nil {
		t.Fatal("cancelled a message already handed over")
	}
	thread, _ := s.Thread(OwnerAccess, in.Messages[0].ThreadID, false)
	if len(thread) != 2 {
		t.Fatalf("the reply is not in the conversation: %d", len(thread))
	}
	if ds, _ := s.Drafts(OwnerAccess); len(ds) != 0 {
		t.Fatal("draft left after sending")
	}

	// Ambiguous failure: never sent again by itself.
	dr2, _ := s.SaveDraft(OwnerAccess, Draft{FromID: a.ID, To: []string{"x@example.com"}, Subject: "two", HTML: "x"}, "owner")
	f.err = fmt.Errorf("%w: connection reset", ErrMaybeSent)
	j2, _ := s.Send(OwnerAccess, dr2.ID, 0)
	s.sendDue(context.Background())
	got, _, _ := s.job(j2.ID)
	if got.State != "unknown" {
		t.Fatalf("state %s", got.State)
	}
	f.err = nil
	s.sendDue(context.Background())
	if len(f.sent) != 1 {
		t.Fatal("an unknown job was sent again")
	}
	if err := s.Retry(OwnerAccess, j2.ID, false); err == nil {
		t.Fatal("retry without force on an unknown job")
	}
	// Permanent refusal fails; a temporary one waits.
	if st, _ := classify(&textproto.Error{Code: 550, Msg: "no"}, 1); st != "failed" {
		t.Fatal("5xx must fail")
	}
	if st, wait := classify(errors.New("dial tcp: timeout"), 1); st != "scheduled" || wait <= 0 {
		t.Fatal("network errors must be retried later")
	}
	// A crash mid-send becomes unknown, not a resend.
	s.db.Exec(`UPDATE mail_outbox SET state = 'sending' WHERE id = ?`, j2.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	if got, _, _ := s.job(j2.ID); got.State != "unknown" {
		t.Fatalf("after restart: %s", got.State)
	}
}

func TestSendChecks(t *testing.T) {
	s, _ := newSvc(t)
	_, a := setup(t, s)
	for _, d := range []Draft{{FromID: a.ID}, {FromID: a.ID, To: []string{"not an address"}}} {
		dr, _ := s.SaveDraft(OwnerAccess, d, "")
		if _, err := s.Send(OwnerAccess, dr.ID, 0); err == nil {
			t.Fatalf("sent %+v", d)
		}
	}
	s.UpdateAddress(a.ID, AddressInput{Receive: true, Send: false})
	dr, _ := s.SaveDraft(OwnerAccess, Draft{FromID: a.ID, To: []string{"x@example.com"}}, "")
	if _, err := s.Send(OwnerAccess, dr.ID, 0); err == nil {
		t.Fatal("sent from an address with sending off")
	}
}

func TestPurgeOnlyFromTrashAndRetention(t *testing.T) {
	s, _ := newSvc(t)
	_, a := setup(t, s)
	s.Ingest(Inbound{Rcpt: a.Address}, msg("j@ex.com", a.Address, "old", "", "x"))
	p, _ := s.List(OwnerAccess, Query{})
	id := p.Messages[0].ID
	if _, err := s.Act(OwnerAccess, Action{IDs: []int64{id}, Action: "purge"}); err == nil {
		t.Fatal("purged mail outside Trash")
	}
	s.Act(OwnerAccess, Action{IDs: []int64{id}, Action: "trash"})
	s.Sweep() // no keep rule: nothing goes
	if _, err := s.Get(OwnerAccess, id); err != nil {
		t.Fatal("deleted without a keep rule")
	}
	s.SaveOptions(Options{TrashDays: 30})
	s.Now = func() time.Time { return time.Now().AddDate(0, 0, 31) }
	if u, _ := s.Usage(); u.Retain["trash"].Messages != 1 {
		t.Fatalf("cleanup preview: %+v", u.Retain)
	}
	s.Sweep()
	if _, err := s.Get(OwnerAccess, id); err == nil {
		t.Fatal("keep rule did not apply")
	}
}

func TestSanitize(t *testing.T) {
	in := `<p onclick="x()">Hi<script>alert(1)</script><img src="https://t.example/p.gif"><img src="cid:logo"><a href="javascript:x">j</a><a href="https://ok.example">ok</a>` +
		`<iframe src="https://x"></iframe><div style="background:url(https://t.example/b.png);width:expression(1)">d</div><svg><script>1</script></svg></p>`
	out := Sanitize(in, SanitizeOptions{CID: map[string]string{"logo": "data:image/png;base64,AA=="}})
	for _, bad := range []string{"onclick", "<script", "alert(1)", "javascript:", "<iframe", "expression(", ` src="https://t.example`, "url(https", "<svg"} {
		if strings.Contains(out, bad) {
			t.Errorf("kept %q: %s", bad, out)
		}
	}
	for _, good := range []string{`data-fw-src="https://t.example/p.gif"`, `src="data:image/png;base64,AA=="`, `href="https://ok.example"`, `target="_blank"`} {
		if !strings.Contains(out, good) {
			t.Errorf("lost %q: %s", good, out)
		}
	}
	if strings.Contains(Sanitize(`<img src="https://t.example/p.gif">`, SanitizeOptions{Images: true}), "data-fw-src") {
		t.Fatal("images allowed but still held back")
	}
	if got := HTMLToText(`<p>Hello <b>you</b></p><ul><li>one</li></ul><a href="https://x.io">site</a>`); !strings.Contains(got, "Hello you") || !strings.Contains(got, "- one") || !strings.Contains(got, "(https://x.io)") {
		t.Fatalf("text: %q", got)
	}
}

func TestMergeSPFAndChecks(t *testing.T) {
	cases := map[string]string{
		"v=spf1 include:_spf.google.com ~all":        "v=spf1 include:_spf.google.com include:_spf.mx.cloudflare.net ~all",
		"v=spf1 include:_spf.mx.cloudflare.net -all": "v=spf1 include:_spf.mx.cloudflare.net -all",
		"v=spf1 a mx": "v=spf1 a mx include:_spf.mx.cloudflare.net",
		"v=spf1 ip4:1.2.3.4 redirect=_spf.example.com": "v=spf1 ip4:1.2.3.4 include:_spf.mx.cloudflare.net redirect=_spf.example.com",
	}
	for in, want := range cases {
		if got := MergeSPF(in, "_spf.mx.cloudflare.net"); got != want {
			t.Errorf("MergeSPF(%q) = %q", in, got)
		}
	}
	s, _ := newSvc(t)
	d, _ := setup(t, s)
	s.Resolver = fakeDNS{
		mx:  map[string][]*net.MX{"nashop.store": {{Host: "route1.mx.cloudflare.net."}, {Host: "route2.mx.cloudflare.net."}}},
		txt: map[string][]string{"nashop.store": {"v=spf1 include:_spf.mx.cloudflare.net ~all", "google-site-verification=x"}},
	}
	got, err := s.CheckDomain(context.Background(), d.ID)
	if err != nil || got.Checks.MX.Status != "ok" || got.Checks.SPF.Status != "ok" || got.Checks.DMARC.Status != "missing" || got.Checks.Routing.Status != "unknown" {
		t.Fatalf("checks: %+v %v", got.Checks, err)
	}
	s.Resolver = fakeDNS{txt: map[string][]string{"nashop.store": {"v=spf1 a ~all", "v=spf1 mx ~all"}}}
	got, _ = s.CheckDomain(context.Background(), d.ID)
	if got.Checks.MX.Status != "missing" || got.Checks.SPF.Status != "wrong" {
		t.Fatalf("bad records: %+v", got.Checks)
	}
}

func TestEdgeRoutesOnlyExceptions(t *testing.T) {
	s, _ := newSvc(t)
	d, _ := setup(t, s)
	s.CreateAddress(AddressInput{DomainID: d.ID, Local: "noreply", Receive: true, ForwardMode: "off"})
	routes, dests, err := s.edgeRoutes()
	if err != nil {
		t.Fatal(err)
	}
	a := routes["a"].(map[string][]string)
	if len(a) != 1 || len(a["noreply@nashop.store"]) != 0 || routes["d"].(map[string][]string)["nashop.store"][0] != "me@gmail.com" || dests[0] != "me@gmail.com" {
		t.Fatalf("routes: %+v %v", routes, dests)
	}
}

func TestExportMbox(t *testing.T) {
	s, _ := newSvc(t)
	_, a := setup(t, s)
	s.Ingest(Inbound{Rcpt: a.Address}, msg("j@ex.com", a.Address, "one", "", "line\r\nFrom the start\r\n"))
	s.Ingest(Inbound{Rcpt: a.Address}, msg("k@ex.com", a.Address, "two", "", "x"))
	var b strings.Builder
	n, err := s.ExportMbox(OwnerAccess, "", "", &b)
	if err != nil || n != 2 || strings.Count(b.String(), "\nFrom ") != 1 || !strings.Contains(b.String(), "\n>From the start") {
		t.Fatalf("mbox (%d, %v): %q", n, err, b.String())
	}
}

// With the dashboard's Cloudflare sign-in, email needs nothing pasted: the connected
// account's token deploys the Worker and adds the Gmail destinations.
func TestUseSignInDeploysWorker(t *testing.T) {
	s, _ := newSvc(t)
	setup(t, s)
	var mu sync.Mutex
	seen := map[string]string{}
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer signed-in-token" {
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"message":"bad token"}]}`))
			return
		}
		key := r.Method + " " + r.URL.Path
		switch key {
		case "GET /zones":
			w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"nashop.store","account":{"id":"acc1","name":"Me"}}],"result_info":{"total_pages":1}}`))
			return
		case "GET /accounts/acc1/email/routing/addresses":
			w.Write([]byte(`{"success":true,"result":[],"result_info":{"total_pages":1}}`))
			return
		case "POST /accounts/acc1/email/routing/addresses":
			b, _ := io.ReadAll(r.Body)
			seen[key] = string(b)
		case "PUT /accounts/acc1/workers/scripts/fleetwatch-email":
			r.ParseMultipartForm(1 << 20)
			seen[key] = r.FormValue("metadata")
			if f, _, err := r.FormFile("worker.js"); err == nil {
				b, _ := io.ReadAll(f)
				seen["script"] = string(b)
			}
		default:
			t.Errorf("unexpected %s", key)
		}
		w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer cf.Close()
	s.CloudflareAPI = cf.URL
	s.SignIn = func(context.Context) (string, string, error) { return "signed-in-token", "nashop.store", nil }

	st, err := s.UseSignIn(context.Background(), "https://dash.example.com")
	if err != nil || !st.Connected || !st.SignIn || st.AccountID != "acc1" || st.DeployedAt == 0 {
		t.Fatalf("%+v %v", st, err)
	}
	meta := seen["PUT /accounts/acc1/workers/scripts/fleetwatch-email"]
	for _, want := range []string{`"FLEETWATCH_URL"`, `https://dash.example.com/api/emails/inbound`, `"INBOUND_SECRET"`, `"ROUTES"`, `me@gmail.com`} {
		if !strings.Contains(meta, want) {
			t.Errorf("worker metadata lacks %s: %s", want, meta)
		}
	}
	if !strings.Contains(seen["script"], "export default") || !strings.Contains(seen["POST /accounts/acc1/email/routing/addresses"], "me@gmail.com") {
		t.Fatalf("worker or destination missing: %v", seen)
	}
	if _, err := s.UseSignIn(context.Background(), "http://localhost:8090"); err == nil {
		t.Fatal("a dashboard the Worker cannot reach was accepted")
	}
	// The sign-in expires: deploying says so instead of failing quietly.
	s.SignIn = func(context.Context) (string, string, error) {
		return "", "", errors.New("the Cloudflare sign-in has expired")
	}
	if err := s.PushEdge(context.Background()); err == nil {
		t.Fatal("an expired sign-in deployed")
	}
	if st, _ := s.Cloudflare(context.Background()); !strings.Contains(st.Error, "expired") {
		t.Fatalf("error not shown: %+v", st)
	}
}

// Adopting a Cloudflare domain keeps what Cloudflare did: address → Gmail rules become
// addresses with those copies, a forwarding catch-all keeps catching, and the old rules
// are turned off (not deleted) once the Worker handles the domain.
func TestAdoptCloudflareDomain(t *testing.T) {
	s, _ := newSvc(t)
	s.Resolver = fakeDNS{}
	var mu sync.Mutex
	var calls []string
	var disabled map[string]any
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := r.Method + " " + r.URL.Path
		calls = append(calls, key)
		res := `{}`
		switch key {
		case "GET /zones":
			res = `[{"id":"z1","name":"nashop.store","account":{"id":"acc1","name":"Me"}},{"id":"z2","name":"other.site","account":{"id":"acc1","name":"Me"}}]`
		case "GET /zones/z1/email/routing", "GET /zones/z2/email/routing":
			res = `{"enabled":true,"status":"ready"}`
		case "GET /zones/z1/email/routing/rules":
			res = `[{"id":"r1","name":"contact","enabled":true,"priority":0,"matchers":[{"type":"literal","field":"to","value":"Contact@nashop.store"}],"actions":[{"type":"forward","value":["shop@gmail.com"]}]},
				{"id":"r2","name":"sales","enabled":true,"priority":1,"matchers":[{"type":"literal","field":"to","value":"sales@nashop.store"}],"actions":[{"type":"forward","value":["a@gmail.com","b@gmail.com"]}]}]`
		case "GET /zones/z2/email/routing/rules", "GET /accounts/acc1/email/routing/addresses", "GET /zones/z1/dns_records":
			res = `[]`
		case "GET /zones/z1/email/routing/rules/catch_all":
			res = `{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"forward","value":["me@gmail.com"]}]}`
		case "GET /zones/z2/email/routing/rules/catch_all":
			res = `{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"drop"}]}`
		case "PUT /zones/z1/email/routing/rules/r1":
			json.NewDecoder(r.Body).Decode(&disabled)
		}
		w.Write([]byte(`{"success":true,"result":` + res + `,"result_info":{"total_pages":1}}`))
	}))
	defer cf.Close()
	s.CloudflareAPI = cf.URL
	s.SignIn = func(context.Context) (string, string, error) { return "tok", "Me", nil }
	if _, err := s.UseSignIn(context.Background(), "https://dash.example.com"); err != nil {
		t.Fatal(err)
	}

	ds, err := s.CloudflareDomains(context.Background())
	if err != nil || len(ds) != 2 || len(ds[0].Rules) != 2 || ds[0].Rules[0].Address != "contact@nashop.store" || ds[0].CatchAll.To[0] != "me@gmail.com" || ds[0].Routing != "on" {
		t.Fatalf("domains: %+v %v", ds, err)
	}
	steps, err := s.Adopt(context.Background(), "z1")
	if err != nil {
		t.Fatal(steps, err)
	}
	contact, err := s.addressByEmail("contact@nashop.store")
	if err != nil || contact.ForwardMode != "custom" || contact.ForwardTo[0] != "shop@gmail.com" {
		t.Fatalf("contact: %+v %v", contact, err)
	}
	if sales, _ := s.addressByEmail("sales@nashop.store"); len(sales.ForwardTo) != 2 {
		t.Fatalf("sales: %+v", sales)
	}
	d, _ := s.domainByName("nashop.store")
	if !d.CatchAll || d.CatchAllTo != contact.ID || d.ForwardTo[0] != "me@gmail.com" || d.ZoneID != "z1" {
		t.Fatalf("domain: %+v", d)
	}
	if res, _ := s.Ingest(Inbound{Rcpt: "anything@nashop.store"}, msg("x@ex.com", "anything@nashop.store", "hi", "", "x")); !res.Stored || res.Forward[0] != "me@gmail.com" {
		t.Fatalf("mail to any address must keep arriving: %+v", res)
	}
	if disabled == nil || disabled["enabled"] != false || disabled["matchers"] == nil {
		t.Fatalf("the old contact rule was not turned off (kept whole): %v", disabled)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "PUT /zones/z1/email/routing/rules/catch_all") || strings.Contains(joined, "DELETE") {
		t.Fatalf("calls: %s", joined)
	}
	if _, err := s.Adopt(context.Background(), "nope"); err == nil {
		t.Fatal("adopted a zone that is not in the account")
	}
	// Adopting again changes nothing and adds nothing twice.
	if _, err := s.Adopt(context.Background(), "z1"); err != nil {
		t.Fatal(err)
	}
	if as, _ := s.Addresses(); len(as) != 2 {
		t.Fatalf("addresses after a second adopt: %d", len(as))
	}
}
