// Package docker reports a host's containers, compose stacks, images and volumes through
// the docker CLI, and runs the actions the dashboard offers on them.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// cli is the docker binary, or "" when Docker is not installed (looked up each time:
// Docker can be installed or removed from the dashboard).
func cli() string {
	p, _ := exec.LookPath("docker")
	return p
}

var errUnknown = errors.New("unknown docker action")

// Available reports whether the docker CLI is installed.
func Available() bool { return cli() != "" }

var (
	targetRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/@-]{0,254}$`) // container, image, volume
	projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
)

func run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	o := sysexec.Opts{Timeout: timeout, MaxOut: 8 << 20}
	if env := ConfigEnv(ctx); env != nil {
		o.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "HOME=/root"}, env...)
	}
	out, err := sysexec.Run(ctx, o, cli(), args...)
	return string(out), err
}

// lines decodes `--format '{{json .}}'` output: one JSON object per line.
func lines[T any](out string) []T {
	var list []T
	for _, l := range strings.Split(out, "\n") {
		var v T
		if l = strings.TrimSpace(l); l != "" && json.Unmarshal([]byte(l), &v) == nil {
			list = append(list, v)
		}
	}
	return list
}

// size parses docker's human sizes: "12.3MB" (powers of 1000) and "1.5GiB" (powers of 1024).
func size(s string) int64 {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		v, _ := strconv.ParseFloat(s, 64)
		return int64(v)
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40}[strings.ToLower(s[i:])]
	return int64(v * mult)
}

func pctOf(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return v
}

// labels parses "a=b,c=d".
func labels(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

type psRow struct {
	ID, Names, Image, State, Status, Ports, CreatedAt, Labels, Mounts string
}
type statsRow struct {
	ID, CPUPerc, MemUsage, MemPerc string
}
type imageRow struct {
	ID, Repository, Tag, Size, CreatedSince string
}
type volumeRow struct {
	Name, Driver string
}
type projectRow struct {
	Name, Status, ConfigFiles string
}

func projects(ctx context.Context) ([]projectRow, error) {
	out, err := run(ctx, 20*time.Second, "compose", "ls", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	var list []projectRow
	return list, json.Unmarshal([]byte(strings.TrimSpace(out)), &list)
}

// Report lists everything; Available false when Docker is not installed.
func Report(ctx context.Context) proto.DockerReport {
	rep := proto.DockerReport{At: time.Now().UnixMilli(), Containers: []proto.DockerContainer{}, Images: []proto.DockerImage{},
		Volumes: []proto.DockerVolume{}, Projects: []proto.DockerProject{}}
	if !Available() {
		return rep
	}
	rep.Available = true
	if v, err := run(ctx, 10*time.Second, "version", "--format", "{{.Server.Version}}"); err == nil {
		rep.Version = strings.TrimSpace(v)
	} else {
		rep.Error = "Docker is installed but its service does not answer: " + err.Error()
		return rep
	}
	out, err := run(ctx, 30*time.Second, "ps", "-a", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	stats := map[string]statsRow{}
	if s, err := run(ctx, 30*time.Second, "stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"); err == nil {
		for _, r := range lines[statsRow](s) {
			stats[r.ID] = r
		}
	}
	usedImages, usedVolumes := map[string]bool{}, map[string]bool{}
	for _, r := range lines[psRow](out) {
		l := labels(r.Labels)
		c := proto.DockerContainer{ID: r.ID, Name: r.Names, Image: r.Image, State: r.State, Status: r.Status, Ports: r.Ports, Created: r.CreatedAt,
			Project: l["com.docker.compose.project"], Service: l["com.docker.compose.service"]}
		if st, ok := stats[r.ID]; ok {
			c.CPU, c.MemPct = pctOf(st.CPUPerc), pctOf(st.MemPerc)
			if used, limit, ok := strings.Cut(st.MemUsage, "/"); ok {
				c.MemUsed, c.MemLimit = size(used), size(limit)
			}
		}
		for _, m := range strings.Split(r.Mounts, ",") {
			if m = strings.TrimSpace(m); m != "" {
				usedVolumes[m] = true
			}
		}
		usedImages[r.Image] = true
		if !strings.Contains(r.Image, ":") && !strings.HasPrefix(r.Image, "sha256") {
			usedImages[r.Image+":latest"] = true
		}
		rep.Containers = append(rep.Containers, c)
	}
	// The registry digests of the image each container runs: the dashboard compares them
	// with what the tag points to now.
	if len(rep.Containers) > 0 {
		ids := make([]string, 0, len(rep.Containers))
		for _, c := range rep.Containers {
			ids = append(ids, c.ID)
		}
		if s, err := run(ctx, 30*time.Second, append([]string{"inspect", "--format", "{{.Id}} {{.Image}}"}, ids...)...); err == nil {
			imageOf, imgs := map[string]string{}, []string{}
			for _, l := range strings.Split(s, "\n") {
				if id, img, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
					imageOf[id] = img
					imgs = append(imgs, img)
				}
			}
			slices.Sort(imgs)
			if d, err := run(ctx, 30*time.Second, append([]string{"image", "inspect", "--format", "{{.Id}} {{json .RepoDigests}}"}, slices.Compact(imgs)...)...); err == nil {
				digests := repoDigests(d)
				for i := range rep.Containers {
					rep.Containers[i].Digests = digests[imageOf[rep.Containers[i].ID]]
				}
			}
		}
	}
	if s, err := run(ctx, 30*time.Second, "images", "--no-trunc", "--format", "{{json .}}"); err == nil {
		for _, r := range lines[imageRow](s) {
			img := proto.DockerImage{ID: r.ID, Repo: r.Repository, Tag: r.Tag, Size: size(r.Size), Created: r.CreatedSince, Dangling: r.Repository == "<none>"}
			img.InUse = usedImages[r.Repository+":"+r.Tag] || usedImages[r.ID] || usedImages[strings.TrimPrefix(r.ID, "sha256:")]
			rep.Images = append(rep.Images, img)
		}
	}
	if s, err := run(ctx, 30*time.Second, "volume", "ls", "--format", "{{json .}}"); err == nil {
		for _, r := range lines[volumeRow](s) {
			rep.Volumes = append(rep.Volumes, proto.DockerVolume{Name: r.Name, Driver: r.Driver, InUse: usedVolumes[r.Name]})
		}
	}
	if list, err := projects(ctx); err == nil {
		for _, p := range list {
			rep.Projects = append(rep.Projects, proto.DockerProject{Name: p.Name, Status: p.Status, ConfigFiles: p.ConfigFiles})
		}
	}
	return rep
}

// compose runs a docker compose command for a project the host itself knows (its
// config files come from `docker compose ls`, never from the request).
func compose(ctx context.Context, project string, timeout time.Duration, args ...string) (string, error) {
	list, err := projects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range list {
		if p.Name != project {
			continue
		}
		cmd := []string{"compose", "-p", p.Name}
		for _, f := range strings.Split(p.ConfigFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				cmd = append(cmd, "-f", f)
			}
		}
		return run(ctx, timeout, append(cmd, args...)...)
	}
	return "", fmt.Errorf("compose project %q not found", project)
}

// containerInfo returns a container's image and compose labels.
func containerInfo(ctx context.Context, id string) (image, project, service string, err error) {
	out, err := run(ctx, 15*time.Second, "inspect", "--format", `{{.Config.Image}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}`, id)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 3)
	if len(parts) != 3 {
		return "", "", "", errors.New("unexpected docker inspect output")
	}
	return parts[0], parts[1], parts[2], nil
}

// Act runs one action and says what happened.
func Act(ctx context.Context, a proto.DockerAction) proto.DockerResult {
	res := proto.DockerResult{RunID: a.RunID, Kind: a.Kind, Target: a.Target, Action: a.Action}
	out, err := act(ctx, a)
	if len(out) > 256<<10 {
		out = out[len(out)-256<<10:]
	}
	res.Output, res.OK = out, err == nil
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func act(ctx context.Context, a proto.DockerAction) (string, error) {
	ctx, done := WithAuth(ctx, a.Auths)
	defer done()
	if a.Kind == "engine" {
		return engine(ctx, a.Action)
	}
	if !Available() {
		return "", errors.New("Docker is not installed on this server")
	}
	const short, long = 2 * time.Minute, 15 * time.Minute
	switch a.Kind {
	case "container":
		if !targetRe.MatchString(a.Target) {
			return "", errors.New("invalid container")
		}
		switch a.Action {
		case "start", "restart":
			return run(ctx, short, a.Action, a.Target)
		case "stop":
			return run(ctx, short, "stop", "-t", "30", a.Target)
		case "remove":
			return run(ctx, short, "rm", a.Target) // refuses a running container
		case "delete":
			return run(ctx, short, "rm", "-f", a.Target) // stops it first
		case "logs":
			return logs(ctx, a.Target)
		case "update":
			image, project, service, err := containerInfo(ctx, a.Target)
			if err != nil {
				return "", err
			}
			if project != "" && service != "" && projectRe.MatchString(project) {
				out, err := compose(ctx, project, long, "pull", service)
				if err != nil {
					return out, err
				}
				more, err := compose(ctx, project, long, "up", "-d", service)
				return out + more, err
			}
			out, err := run(ctx, long, "pull", image)
			return out + "\nPulled " + image + ". This container was not started by docker compose, so it keeps running the old image until it is recreated with the same options.", err
		}
	case "project":
		if !projectRe.MatchString(a.Target) {
			return "", errors.New("invalid compose project")
		}
		switch a.Action {
		case "start":
			return compose(ctx, a.Target, long, "up", "-d")
		case "stop", "restart":
			return compose(ctx, a.Target, short, a.Action)
		case "down": // its containers and networks go; named volumes stay
			return compose(ctx, a.Target, long, "down", "--remove-orphans")
		case "update":
			out, err := compose(ctx, a.Target, long, "pull")
			if err != nil {
				return out, err
			}
			more, err := compose(ctx, a.Target, long, "up", "-d")
			return out + more, err
		}
	case "image":
		if a.Action == "prune" {
			return run(ctx, short, "image", "prune", "-f") // dangling images only
		}
		if a.Action == "remove" && targetRe.MatchString(a.Target) {
			return run(ctx, short, "rmi", a.Target) // refuses an image a container uses
		}
	case "volume":
		if a.Action == "remove" && targetRe.MatchString(a.Target) {
			return run(ctx, short, "volume", "rm", a.Target) // refuses a volume a container uses
		}
	}
	return "", fmt.Errorf("unknown docker action %s %s", a.Kind, a.Action)
}

// logs returns the last lines a container wrote to stdout and stderr, interleaved.
func logs(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, cli(), "logs", "--tail", "300", "--timestamps", id)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// repoDigests reads `docker image inspect --format '{{.Id}} {{json .RepoDigests}}'`:
// image ID → the digests it was pulled as.
func repoDigests(out string) map[string][]string {
	m := map[string][]string{}
	for _, l := range strings.Split(out, "\n") {
		id, js, ok := strings.Cut(strings.TrimSpace(l), " ")
		var list []string
		if !ok || json.Unmarshal([]byte(js), &list) != nil {
			continue
		}
		for _, rd := range list {
			if _, d, ok := strings.Cut(rd, "@"); ok {
				m[id] = append(m[id], d)
			}
		}
	}
	return m
}
