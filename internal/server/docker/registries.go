package docker

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Logins for private registries (GHCR, Docker Hub, GitLab…): agents get them for pulls
// and builds, the update check for asking about private images.

const registriesKey = "docker.registries"

var (
	ErrNoRegistry = errors.New("registry login not found")
	hostRe        = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(:\d{1,5})?$`)
)

// Registry is one login; Password is write-only.
type Registry struct {
	ID          string `json:"id"`
	Host        string `json:"host"` // ghcr.io, docker.io, registry.gitlab.com…
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"hasPassword"`
	CreatedAt   int64  `json:"createdAt"`
}

type storedRegistry struct {
	Registry
	Sealed []byte `json:"sealed"`
}

type Registries struct {
	db  *sql.DB
	box *secret.Box
	mu  sync.Mutex
}

func NewRegistries(d *sql.DB, box *secret.Box) *Registries { return &Registries{db: d, box: box} }

// normHost turns what people paste (https://ghcr.io/, index.docker.io) into a host name.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	switch h {
	case "", "docker.io", "index.docker.io", "registry-1.docker.io", "hub.docker.com":
		return "docker.io"
	}
	return h
}

func (r *Registries) stored() []storedRegistry {
	list := []storedRegistry{}
	settings.Load(r.db, registriesKey, &list)
	return list
}

func (r *Registries) List() []Registry {
	out := []Registry{}
	for _, s := range r.stored() {
		x := s.Registry
		x.Password, x.HasPassword = "", len(s.Sealed) > 0
		out = append(out, x)
	}
	return out
}

// Save adds a login, or replaces the one for the same host and user.
func (r *Registries) Save(in Registry) (Registry, error) {
	in.Host, in.Username = normHost(in.Host), strings.TrimSpace(in.Username)
	switch {
	case !hostRe.MatchString(in.Host):
		return Registry{}, valid.Error("enter the registry's address, like ghcr.io or docker.io")
	case in.Username == "" || len(in.Username) > 200:
		return Registry{}, valid.Error("enter the user name")
	case in.Password == "" || len(in.Password) > 4096:
		return Registry{}, valid.Error("enter the password or access token")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	list := slices.DeleteFunc(r.stored(), func(s storedRegistry) bool { return s.Host == in.Host && s.Username == in.Username })
	if len(list) >= 50 {
		return Registry{}, valid.Error("at most 50 registry logins")
	}
	b := make([]byte, 6)
	rand.Read(b)
	s := storedRegistry{Registry: Registry{ID: "reg_" + hex.EncodeToString(b), Host: in.Host, Username: in.Username, CreatedAt: time.Now().UnixMilli()},
		Sealed: r.box.Seal([]byte(in.Password), "docker.registry")}
	if err := settings.Save(r.db, registriesKey, append(list, s)); err != nil {
		return Registry{}, err
	}
	out := s.Registry
	out.HasPassword = true
	return out, nil
}

func (r *Registries) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.stored()
	n := len(list)
	if list = slices.DeleteFunc(list, func(s storedRegistry) bool { return s.ID == id }); len(list) == n {
		return ErrNoRegistry
	}
	return settings.Save(r.db, registriesKey, list)
}

// Auths are all the logins, for an agent's pull or build.
func (r *Registries) Auths() []proto.RegistryAuth {
	if r == nil {
		return nil
	}
	var out []proto.RegistryAuth
	for _, s := range r.stored() {
		if pw, err := r.box.Open(s.Sealed, "docker.registry"); err == nil {
			out = append(out, proto.RegistryAuth{Host: s.Host, Username: s.Username, Password: string(pw)})
		}
	}
	return out
}

// cred is the login for a registry host as the update check names it.
func (r *Registries) cred(registry string) (user, pass string) {
	if r == nil {
		return "", ""
	}
	host := normHost(registry)
	for _, a := range r.Auths() {
		if a.Host == host {
			return a.Username, a.Password
		}
	}
	return "", ""
}
