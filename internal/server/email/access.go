package email

import (
	"strings"

	"fleetwatch/internal/server/valid"
)

// Mail is the owner's unless the owner shares a domain or an address with a team
// member (operators only), at one of three levels, each including the ones before:
// read (and organise), send (compose, reply, forward), manage (the addresses, their
// forwarding and spam rules). Domains, Cloudflare and the grants stay the owner's.

var levels = map[string]int{"read": 1, "send": 2, "manage": 3}

// Access is what one signed-in person may do with mail.
type Access struct {
	Owner   bool
	domains map[string]int // domain id → level
	addrs   map[string]int // address id → level
	// mailboxes the person reaches, with the level: filled by For.
	boxes map[string]int
}

// OwnerAccess may do everything.
var OwnerAccess = Access{Owner: true}

// For is a member's access ("" member = the owner; a non-operator gets none).
func (s *Service) For(memberID string, owner, operator bool) Access {
	if owner {
		return OwnerAccess
	}
	a := Access{domains: map[string]int{}, addrs: map[string]int{}, boxes: map[string]int{}}
	if !operator || memberID == "" {
		return a
	}
	rows, err := s.db.Query(`SELECT scope_kind, scope_id, level FROM mail_grants WHERE member_id = ?`, memberID)
	if err != nil {
		return a
	}
	for rows.Next() {
		var kind, id, lvl string
		rows.Scan(&kind, &id, &lvl)
		if kind == "domain" {
			a.domains[id] = max(a.domains[id], levels[lvl])
		} else {
			a.addrs[id] = max(a.addrs[id], levels[lvl])
		}
	}
	rows.Close()
	if len(a.domains)+len(a.addrs) == 0 {
		return a
	}
	addrRows, err := s.db.Query(`SELECT id, domain_id, COALESCE(inbox_id, '') FROM mail_addresses`)
	if err != nil {
		return a
	}
	defer addrRows.Close()
	for addrRows.Next() {
		var id, dom, inbox string
		addrRows.Scan(&id, &dom, &inbox)
		lvl := max(a.domains[dom], a.addrs[id])
		if lvl > 0 {
			a.addrs[id] = lvl
			if inbox == "" {
				a.boxes[id] = max(a.boxes[id], lvl)
			}
		}
	}
	return a
}

// Any reports whether the person sees any mail at all.
func (a Access) Any() bool {
	return a.Owner || len(a.boxes) > 0 || len(a.domains) > 0 || len(a.addrs) > 0
}

// Mailbox: may the person do lvl with mail in this inbox?
func (a Access) Mailbox(id, lvl string) bool { return a.Owner || a.boxes[id] >= levels[lvl] }

// AddressCan: may the person do lvl with this address (send from it, manage it)?
func (a Access) AddressCan(id, lvl string) bool { return a.Owner || a.addrs[id] >= levels[lvl] }

// DomainCan: may the person do lvl across the whole domain (add addresses to it)?
func (a Access) DomainCan(id, lvl string) bool { return a.Owner || a.domains[id] >= levels[lvl] }

// mailboxFilter is an SQL condition on column col limiting rows to the person's
// inboxes, with its arguments; "1" for the owner.
func (a Access) mailboxFilter(col string) (string, []any) {
	if a.Owner {
		return "1", nil
	}
	if len(a.boxes) == 0 {
		return "0", nil
	}
	ph := make([]string, 0, len(a.boxes))
	args := make([]any, 0, len(a.boxes))
	for id := range a.boxes {
		ph = append(ph, "?")
		args = append(args, id)
	}
	return col + " IN (" + strings.Join(ph, ",") + ")", args
}

// ---------- grants (owner only) ----------

func (s *Service) Grants() ([]Grant, error) {
	rows, err := s.db.Query(`SELECT member_id, scope_kind, scope_id, level FROM mail_grants ORDER BY member_id, scope_kind, scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		rows.Scan(&g.MemberID, &g.ScopeKind, &g.ScopeID, &g.Level)
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGrants replaces one member's grants. Only operators can be given mail.
func (s *Service) SetGrants(memberID, role string, gs []Grant) error {
	if role != "operator" && len(gs) > 0 {
		return valid.Error("only operators can be given access to mail")
	}
	if len(gs) > 200 {
		return valid.Error("too many grants")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mail_grants WHERE member_id = ?`, memberID); err != nil {
		return err
	}
	for _, g := range gs {
		if levels[g.Level] == 0 {
			return valid.Error("level must be read, send or manage")
		}
		var n int
		switch g.ScopeKind {
		case "domain":
			tx.QueryRow(`SELECT COUNT(*) FROM mail_domains WHERE id = ?`, g.ScopeID).Scan(&n)
		case "address":
			tx.QueryRow(`SELECT COUNT(*) FROM mail_addresses WHERE id = ?`, g.ScopeID).Scan(&n)
		}
		if n == 0 {
			return valid.Error("a grant names a domain or address that does not exist")
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO mail_grants (member_id, scope_kind, scope_id, level) VALUES (?, ?, ?, ?)`, memberID, g.ScopeKind, g.ScopeID, g.Level); err != nil {
			return err
		}
	}
	return tx.Commit()
}
