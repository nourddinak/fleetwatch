package api

import (
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/server/db"
)

func TestTimelinePieces(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'web-1', 0)`)
	d.Exec(`INSERT INTO events (ts, server_id, app_id, kind, text) VALUES (100, 's1', 'app_1', 'deploy', 'shop deployed abc1234: fix cart'),
		(200, 's1', 'app_1', 'status', 'shop: online → crash-loop'), (300, 's1', NULL, 'agent', 'Agent connected')`)
	d.Exec(`INSERT INTO audit_events (ts, action, ok, target, detail, actor) VALUES (150, 'file.write', 1, 'app_1', 'shop: .env', 'Nour'),
		(160, 'login', 1, 'app_1', '', 'Nour'), (170, 'file.write', 0, 'app_1', 'shop: .env', 'Nour'),
		(180, 'site.redeploy', 1, 's1', 'blog (me/blog)', 'Sam'), (190, 'site.redeploy', 1, 's1', 'shop (me/shop)', 'Sam')`)
	s := &server{Deps{DB: d}}

	ev := s.momentsEvents(`app_id = ?`, 1000, "app_1")
	if len(ev) != 2 || ev[0].Kind != "status" || ev[0].Tone != "crit" || ev[1].Kind != "deploy" {
		t.Fatalf("%+v", ev)
	}
	// Who did what: failed actions and sign-ins stay out.
	au := s.momentsAudit(1000, []string{"app_1"}, "", "")
	if len(au) != 1 || au[0].Who != "Nour" || !strings.Contains(au[0].Text, ".env") {
		t.Fatalf("%+v", au)
	}
	// A site's actions: aimed at its server, its name first.
	if site := s.momentsAudit(1000, []string{"s1"}, "site.", "blog"); len(site) != 1 || site[0].Who != "Sam" {
		t.Fatalf("%+v", site)
	}
	// Paging back: only what came before.
	if older := s.momentsEvents(`server_id = ?`, 150, "s1"); len(older) != 1 || older[0].TS != 100 {
		t.Fatalf("%+v", older)
	}
}
