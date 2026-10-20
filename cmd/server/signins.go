package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/valid"
)

// A sign-in to someone's dashboard account from a browser it never used: the AI calls
// them (a text when they do not answer, or in their quiet hours), says where from and
// with what, and signs that browser out if it was not them.

// browserOf names a user agent's browser and system, like "Chrome on Windows".
func browserOf(ua string) string {
	b := ""
	for _, x := range [][2]string{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"SamsungBrowser/", "Samsung Internet"}, {"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"}, {"CriOS/", "Chrome"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}} {
		if strings.Contains(ua, x[0]) {
			b = x[1]
			break
		}
	}
	o := ""
	for _, x := range [][2]string{{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"}, {"Windows", "Windows"}, {"Mac OS X", "Mac"}, {"Macintosh", "Mac"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, x[0]) {
			o = x[1]
			break
		}
	}
	switch {
	case b == "" && o == "":
		return ""
	case b == "":
		return "a browser on " + o
	case o == "":
		return b
	}
	return b + " on " + o
}

// newLogin is the last unknown-browser sign-in of a person, to sign out if it was not them.
type newLogin struct {
	session, browser, country, ip string
	at                            time.Time
}

var newLogins = struct {
	sync.Mutex
	m map[string]newLogin
}{m: map[string]newLogin{}} // shortcut: kept in memory, a restart forgets it (Settings → Sessions still signs it out)

// signedIn runs after every completed sign-in.
func (d *callDeps) signedIn(member, sessionID string, c auth.Client) {
	person := member
	if person == "" {
		person = oncall.Owner
	}
	b := browserOf(c.UserAgent)
	if b == "" {
		return
	}
	now := time.Now().UnixMilli()
	known := map[string]bool{}
	if rows, err := d.db.Query(`SELECT browser FROM known_browsers WHERE person = ?`, person); err == nil {
		for rows.Next() {
			var x string
			if rows.Scan(&x) == nil {
				known[x] = true
			}
		}
		rows.Close()
	}
	remember := func(x string) {
		d.db.Exec(`INSERT INTO known_browsers (person, browser, first_at, last_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (person, browser) DO UPDATE SET last_at = excluded.last_at`, person, x, now, now)
	}
	if len(known) == 0 { // nothing learned yet (a new account, or this version is new): the browsers in use now
		for _, s := range d.auth.SessionsOf(member) {
			if x := browserOf(s.UserAgent); x != "" && s.ID != sessionID {
				known[x] = true
				remember(x)
			}
		}
		if len(known) == 0 {
			remember(b) // the very first sign-in: nothing to compare with
			return
		}
	}
	remember(b)
	if known[b] {
		return
	}
	country := ""
	if d.security != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		country = d.security.CountryOf(ctx, c.IP)
		cancel()
	}
	newLogins.Lock()
	newLogins.m[person] = newLogin{sessionID, b, country, c.IP, time.Now()}
	newLogins.Unlock()
	d.warnSignIn(person, b, country, c.IP)
}

// warnSignIn calls (or texts) someone about a sign-in from a new browser.
func (d *callDeps) warnSignIn(person, browser, country, ip string) {
	where := "an unknown place"
	if country != "" && country != "LAN" {
		where = "the country with code " + country
	} else if country == "LAN" {
		where = "the local network"
	}
	text := fmt.Sprintf("🔐 New sign-in to your Fleetwatch account: %s, from %s (%s). Not you? Reply \"sign it out\", then change your password.", browser, strings.TrimPrefix(where, "the country with code "), ip)
	wp, ok := d.wa.PersonOf(person)
	if !ok || wp.Phone == "" || !d.wa.Linked() {
		return
	}
	talker := d.sessionOpening(person, "", false, fmt.Sprintf("The call just connected. You are calling %s because someone just signed in to their Fleetwatch dashboard account with a browser this account never used before: %s, from %s (address %s). Tell them in your own words (name the country, not its code), and ask if it was them. If it was not, offer to sign that browser out right now (sign_out_new_login) and tell them to change their password. If it was them, say all is fine.", d.nameOf(person), browser, where, ip))
	if talker == nil || wp.Quiet(time.Now()) {
		d.text(person, text)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		if answered, err := d.wa.Ring(ctx, wp.Phone, 45*time.Second, talker); err != nil || !answered {
			if err != nil {
				slog.Warn("whatsapp sign-in call", "err", err)
			}
			d.text(person, text)
		}
	}()
}

// signInTools let the AI sign out a new browser that was not them.
func (d callDeps) signInTools(person string) []ai.Tool {
	return []ai.Tool{{Name: "sign_out_new_login", Description: "Sign out the browser that just signed in to their dashboard account (the one you warned them about), when they say it was not them.",
		Run: func(context.Context, map[string]any) (any, error) {
			newLogins.Lock()
			n, ok := newLogins.m[person]
			newLogins.Unlock()
			if !ok || time.Since(n.at) > 24*time.Hour {
				return nil, valid.Error("there is no recent sign-in from a new browser to sign out")
			}
			if err := d.auth.RevokeSession(auth.Client{IP: "WhatsApp", UserAgent: "Fleetwatch AI"}, n.session); err != nil {
				return "it is already signed out", nil
			}
			newLogins.Lock()
			delete(newLogins.m, person)
			newLogins.Unlock()
			return "signed out: " + n.browser + "; they should change their password now (Settings → Account)", nil
		}}}
}
