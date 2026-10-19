package dns

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

// AnalyticsScope lets a sign-in read traffic stats. A client made before it existed may
// not list it; the sign-in then goes on without it (see RetryWithoutAnalytics).
const AnalyticsScope = "analytics.read"

// Traffic is a site's last 24 hours on Cloudflare, over its domains.
type Traffic struct {
	Requests  int64     `json:"requests"`
	Visits    int64     `json:"visits"`
	Bytes     int64     `json:"bytes"`
	Threats   *int64    `json:"threats,omitempty"` // nil: Cloudflare did not say
	Hours     []Hour    `json:"hours"`             // 24, oldest first
	Countries []Country `json:"countries"`         // the busiest few
}

type Hour struct {
	Time     string `json:"t"`
	Requests int64  `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

type Country struct {
	Code     string `json:"code"` // ISO 3166, as Cloudflare names it
	Requests int64  `json:"requests"`
}

// ErrNoZone: none of the domains is in a Cloudflare account of the person.
var ErrNoZone = valid.Error("none of this site's domains is in a Cloudflare account of whoever added it")

var errNoAnalytics = valid.Error("traffic stats need one more Cloudflare permission: sign in with Cloudflare again")

const trafficQuery = `query ($zone: string!, $hosts: [string!], $since: Time!, $until: Time!) {
  viewer {
    zones(filter: {zoneTag: $zone}) {
      hours: httpRequestsAdaptiveGroups(limit: 100, filter: {datetime_geq: $since, datetime_lt: $until, clientRequestHTTPHost_in: $hosts}, orderBy: [datetimeHour_ASC]) {
        count
        sum { edgeResponseBytes visits }
        dimensions { datetimeHour }
      }
      countries: httpRequestsAdaptiveGroups(limit: 6, filter: {datetime_geq: $since, datetime_lt: $until, clientRequestHTTPHost_in: $hosts}, orderBy: [count_DESC]) {
        count
        dimensions { clientCountryName }
      }
    }
  }
}`

const threatsQuery = `query ($zone: string!, $hosts: [string!], $since: Time!, $until: Time!) {
  viewer {
    zones(filter: {zoneTag: $zone}) {
      threats: firewallEventsAdaptiveGroups(limit: 1, filter: {datetime_geq: $since, datetime_lt: $until, clientRequestHTTPHost_in: $hosts, action_in: ["block", "challenge", "jschallenge", "managed_challenge"]}) {
        count
      }
    }
  }
}`

// graphql runs one query against Cloudflare's analytics API.
func (c cloudflare) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := doJSON(ctx, http.MethodPost, CloudflareAPI+"/graphql", http.Header{"Authorization": {"Bearer " + c.token}},
		map[string]any{"query": query, "variables": vars}, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return errors.New("Cloudflare: " + env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

// Traffic adds up the last 24 hours of a site's domains, through the person's accounts.
func (s *Service) Traffic(ctx context.Context, owner string, domains []string) (Traffic, error) {
	out := Traffic{Hours: []Hour{}, Countries: []Country{}}
	type zoneHosts struct {
		account string
		zone    Zone
		hosts   []string
	}
	var zones []*zoneHosts
	for _, d := range domains {
		a, z, ok := s.zoneFor(owner, d)
		if !ok {
			continue
		}
		i := slices.IndexFunc(zones, func(zh *zoneHosts) bool { return zh.zone.ID == z.ID })
		if i < 0 {
			zones = append(zones, &zoneHosts{account: a.ID, zone: z})
			i = len(zones) - 1
		}
		zones[i].hosts = append(zones[i].hosts, d)
	}
	if len(zones) == 0 {
		return out, ErrNoZone
	}
	until := s.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	since := until.Add(-24 * time.Hour)
	hours := map[int64]*Hour{}
	countries := map[string]int64{}
	for _, zh := range zones {
		a, p, err := s.account(owner, zh.account)
		if err != nil {
			return out, err
		}
		if len(a.Scopes) > 0 && !slices.Contains(a.Scopes, AnalyticsScope) {
			return out, errNoAnalytics
		}
		c := p.(cloudflare)
		vars := map[string]any{"zone": zh.zone.ID, "hosts": zh.hosts, "since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339)}
		var res struct {
			Viewer struct {
				Zones []struct {
					Hours []struct {
						Count int64
						Sum   struct {
							EdgeResponseBytes int64 `json:"edgeResponseBytes"`
							Visits            int64 `json:"visits"`
						} `json:"sum"`
						Dimensions struct {
							DatetimeHour string `json:"datetimeHour"`
						} `json:"dimensions"`
					} `json:"hours"`
					Countries []struct {
						Count      int64
						Dimensions struct {
							ClientCountryName string `json:"clientCountryName"`
						} `json:"dimensions"`
					} `json:"countries"`
				} `json:"zones"`
			} `json:"viewer"`
		}
		if err := c.graphql(ctx, trafficQuery, vars, &res); err != nil {
			if msg := strings.ToLower(err.Error()); strings.Contains(msg, "authz") || strings.Contains(msg, "not authorized") || strings.Contains(msg, "permission") {
				return out, errNoAnalytics
			}
			return out, err
		}
		for _, zr := range res.Viewer.Zones {
			for _, h := range zr.Hours {
				t, err := time.Parse(time.RFC3339, h.Dimensions.DatetimeHour)
				if err != nil {
					continue
				}
				slot := hours[t.Unix()]
				if slot == nil {
					slot = &Hour{}
					hours[t.Unix()] = slot
				}
				slot.Requests += h.Count
				slot.Bytes += h.Sum.EdgeResponseBytes
				out.Requests += h.Count
				out.Bytes += h.Sum.EdgeResponseBytes
				out.Visits += h.Sum.Visits
			}
			for _, ct := range zr.Countries {
				countries[ct.Dimensions.ClientCountryName] += ct.Count
			}
		}
		var th struct {
			Viewer struct {
				Zones []struct {
					Threats []struct{ Count int64 } `json:"threats"`
				} `json:"zones"`
			} `json:"viewer"`
		}
		if c.graphql(ctx, threatsQuery, vars, &th) == nil { // not every plan answers this: then it stays unknown
			if out.Threats == nil {
				out.Threats = new(int64)
			}
			for _, zr := range th.Viewer.Zones {
				for _, t := range zr.Threats {
					*out.Threats += t.Count
				}
			}
		}
	}
	for t := since; t.Before(until); t = t.Add(time.Hour) {
		h := Hour{}
		if got := hours[t.Unix()]; got != nil {
			h = *got
		}
		h.Time = t.Format(time.RFC3339)
		out.Hours = append(out.Hours, h)
	}
	for code, n := range countries {
		out.Countries = append(out.Countries, Country{code, n})
	}
	slices.SortFunc(out.Countries, func(a, b Country) int {
		return cmp.Or(cmp.Compare(b.Requests, a.Requests), strings.Compare(a.Code, b.Code))
	})
	out.Countries = out.Countries[:min(len(out.Countries), 6)]
	return out, nil
}
