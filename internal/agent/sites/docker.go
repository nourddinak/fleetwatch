package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/agent/docker"
	"fleetwatch/internal/proto"
)

// Docker apps: images are built by GitHub Actions and pulled onto this server.
// Older images stay tagged so a rollback deploys the previous snapshot.

var composeRe = regexp.MustCompile(`^(docker-)?compose(\.[a-z0-9-]+)?\.ya?ml$`)
var imageBaseRe = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`)
var imageTagRe = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
var composeServiceRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// composeBuildServices returns the services with build sections in a compose file.
// Compose's normalized JSON is used so we don't try to parse YAML ourselves.
var composeBuildServices = func(ctx context.Context, dir, file string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", file, "config", "--format", "json")
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", "DOCKER_BUILDKIT=1"}, docker.ConfigEnv(ctx)...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose config failed: %w", err)
	}
	var config struct {
		Services map[string]struct {
			Build json.RawMessage `json:"build"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &config); err != nil {
		return nil, fmt.Errorf("docker compose returned invalid JSON: %w", err)
	}
	var services []string
	for name, service := range config.Services {
		if len(service.Build) > 0 && string(service.Build) != "null" {
			services = append(services, name)
		}
	}
	slices.Sort(services)
	return services, nil
}

// dockerCmd runs docker (tests replace it).
var dockerCmd = func(ctx context.Context, dir string, timeout time.Duration, log *bytes.Buffer, args ...string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker is not installed on this server: install it from the Docker page first")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", "DOCKER_BUILDKIT=1"}, docker.ConfigEnv(ctx)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := out.String()
	if len(text) > 64<<10 { // the end of a long build is what matters
		text = "…\n" + text[len(text)-64<<10:]
	}
	log.WriteString(text)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("docker %s timed out after %s", args[0], timeout)
	}
	if err != nil {
		return fmt.Errorf("docker %s failed: %s", args[0], lastLine([]byte(text), err))
	}
	return nil
}

// runDocker builds and starts a release; it returns the address Caddy proxies to.
func runDocker(ctx context.Context, req proto.SiteDeploy, rel string, log *bytes.Buffer) (string, error) {
	d := req.Docker
	ctx, done := docker.WithAuth(ctx, d.Auths) // private images in FROM and image: lines
	defer done()
	project := "fw-" + req.Name
	if strings.TrimSpace(d.Env) != "" || d.Compose != "" {
		if err := os.WriteFile(filepath.Join(rel, ".env"), []byte(d.Env), 0o600); err != nil {
			return "", err
		}
	}
	if d.Compose != "" {
		if !composeRe.MatchString(d.Compose) {
			return "", errors.New("invalid compose file name")
		}
		if _, err := os.Stat(filepath.Join(rel, d.Compose)); err != nil {
			return "", fmt.Errorf("%s is not in the repository", d.Compose)
		}
		args := []string{"compose", "-p", project, "-f", d.Compose}
		if d.ImageBase != "" {
			if !imageBaseRe.MatchString(d.ImageBase) || !imageTagRe.MatchString(d.ImageTag) {
				return "", errors.New("invalid remote Docker image reference")
			}
			services, err := composeBuildServices(ctx, rel, d.Compose)
			if err != nil {
				return "", err
			}
			images := map[string]map[string]any{}
			for _, service := range services {
				if !composeServiceRe.MatchString(service) {
					return "", fmt.Errorf("invalid compose service name %q", service)
				}
				images[service] = map[string]any{
					"image": d.ImageBase + ":" + d.ImageTag + "-" + service,
					"build": nil,
				}
			}
			if len(images) > 0 {
				override, err := os.CreateTemp(rel, ".fleetwatch-compose-*.json")
				if err != nil {
					return "", err
				}
				overridePath := override.Name()
				defer os.Remove(overridePath)
				if err := json.NewEncoder(override).Encode(map[string]any{"services": images}); err != nil {
					override.Close()
					return "", err
				}
				if err := override.Close(); err != nil {
					return "", err
				}
				args = append(args, "-f", filepath.Base(overridePath))
			}
			fmt.Fprintf(log, "$ docker %s pull\n", strings.Join(args, " "))
			if err := dockerCmd(ctx, rel, 30*time.Minute, log, append(args, "pull")...); err != nil {
				return "", err
			}
			args = append(args, "up", "-d", "--remove-orphans", "--no-build")
		} else {
			args = append(args, "up", "-d", "--build", "--remove-orphans")
		}
		fmt.Fprintf(log, "$ docker %s\n", strings.Join(args, " "))
		if err := dockerCmd(ctx, rel, 30*time.Minute, log, args...); err != nil {
			return "", err
		}
	} else {
		if _, err := os.Stat(filepath.Join(rel, "Dockerfile")); err != nil {
			return "", errors.New("there is no Dockerfile at the top of the repository")
		}
		image := project + ":" + filepath.Base(rel)
		imageRepo := project
		if d.ImageBase != "" {
			if !imageBaseRe.MatchString(d.ImageBase) || !imageTagRe.MatchString(d.ImageTag) {
				return "", errors.New("invalid remote Docker image reference")
			}
			image = d.ImageBase + ":" + d.ImageTag
			imageRepo = d.ImageBase
			fmt.Fprintf(log, "$ docker pull %s\n", image)
			if err := dockerCmd(ctx, rel, 30*time.Minute, log, "pull", image); err != nil {
				return "", err
			}
		} else {
			fmt.Fprintf(log, "$ docker build -t %s .\n", image)
			if err := dockerCmd(ctx, rel, 30*time.Minute, log, "build", "-t", image, "--label", "fleetwatch.site="+req.Name, "."); err != nil {
				return "", err
			}
		}
		dockerCmd(ctx, rel, time.Minute, &bytes.Buffer{}, "rm", "-f", project) // the previous release's container
		args := []string{"run", "-d", "--name", project, "--restart", "unless-stopped", "--label", "fleetwatch.site=" + req.Name}
		if _, err := os.Stat(filepath.Join(rel, ".env")); err == nil {
			args = append(args, "--env-file", filepath.Join(rel, ".env"))
		}
		if d.Port > 0 && d.HostPort > 0 {
			args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", d.HostPort, d.Port))
		}
		fmt.Fprintf(log, "$ docker %s\n", strings.Join(append(args, image), " "))
		if err := dockerCmd(ctx, rel, 2*time.Minute, log, append(args, image)...); err != nil {
			return "", err
		}
		pruneImages(ctx, imageRepo, image, log)
	}
	if d.HostPort <= 0 {
		return "", nil
	}
	return "127.0.0.1:" + strconv.Itoa(d.HostPort), nil
}

// pruneImages keeps the running image and the newest older one (for a quick rollback).
func pruneImages(ctx context.Context, repo, keep string, log *bytes.Buffer) {
	out, err := exec.CommandContext(ctx, "docker", "images", repo, "--format", "{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		return
	}
	tags := strings.Fields(string(out)) // newest first
	tags = slices.DeleteFunc(tags, func(t string) bool { return t == keep })
	for i, t := range tags {
		if i >= 1 {
			dockerCmd(ctx, "", time.Minute, &bytes.Buffer{}, "rmi", t)
		}
	}
}

// removeDocker takes a Docker app's containers and images away (Remove).
func removeDocker(name, imageBase string) {
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	project := "fw-" + name
	var log bytes.Buffer
	dockerCmd(ctx, "", 5*time.Minute, &log, "compose", "-p", project, "down", "--remove-orphans")
	dockerCmd(ctx, "", time.Minute, &log, "rm", "-f", project)
	if out, err := exec.CommandContext(ctx, "docker", "images", project, "--format", "{{.Repository}}:{{.Tag}}").Output(); err == nil {
		for _, t := range strings.Fields(string(out)) {
			dockerCmd(ctx, "", time.Minute, &log, "rmi", t)
		}
	}
	if imageBaseRe.MatchString(imageBase) {
		if out, err := exec.CommandContext(ctx, "docker", "images", imageBase, "--format", "{{.Repository}}:{{.Tag}}").Output(); err == nil {
			for _, t := range strings.Fields(string(out)) {
				dockerCmd(ctx, "", time.Minute, &log, "rmi", t)
			}
		}
	}
}
