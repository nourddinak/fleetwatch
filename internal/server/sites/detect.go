package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"fleetwatch/internal/server/github"
)

// Plan is how a repository becomes a site: published as is, or built on GitHub Actions.
type Plan struct {
	Kind      string `json:"kind"`      // static | build
	Framework string `json:"framework"` // what was recognised ("Vite", "Plain HTML"…)
	Dir       string `json:"dir"`       // static: folder of the repository to publish ("" = all)
	Install   string `json:"install"`   // build: install command
	Build     string `json:"build"`     // build: build command
	Output    string `json:"output"`    // build: folder the build writes
	Node      string `json:"node"`      // build: .nvmrc | .node-version | "" (latest LTS)
	SPA       bool   `json:"spa"`       // unknown paths fall back to /index.html
	Note      string `json:"note,omitempty"`
}

// framework: package → name, output folder, single-page app. Earlier rows win.
var frameworks = []struct {
	pkg, name, out string
	spa            bool
	note           string
}{
	{"next", "Next.js (static export)", "out", false, "Needs output: 'export' in next.config.js: Next.js sites with a server cannot be static."},
	{"astro", "Astro", "dist", false, ""},
	{"@sveltejs/kit", "SvelteKit (static)", "build", false, "Needs @sveltejs/adapter-static."},
	{"gatsby", "Gatsby", "public", false, ""},
	{"@docusaurus/core", "Docusaurus", "build", false, ""},
	{"vitepress", "VitePress", "docs/.vitepress/dist", false, "Check the output folder: it follows your VitePress folder."},
	{"@11ty/eleventy", "Eleventy", "_site", false, ""},
	{"nuxt", "Nuxt (generate)", ".output/public", false, "Needs `nuxt generate` as the build script."},
	{"react-scripts", "Create React App", "build", true, ""},
	{"@angular/core", "Angular", "dist", true, "Angular writes to dist/<project>/browser: set the output folder to that."},
	{"@vue/cli-service", "Vue CLI", "dist", true, ""},
	{"vite", "Vite", "dist", true, ""},
	{"parcel", "Parcel", "dist", true, ""},
}

// Detect looks at the top of a repository (and its package.json) to choose the plan.
func (s *Service) Detect(ctx context.Context, repo, branch string) (Plan, error) {
	names, err := s.GitHub.List(ctx, repo, "", branch)
	if err != nil {
		return Plan{}, err
	}
	has := func(n string) bool { return slices.Contains(names, n) }
	if has("package.json") {
		raw, _, _, err := s.GitHub.File(ctx, repo, "package.json", branch)
		if err != nil {
			return Plan{}, err
		}
		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(raw, &pkg) != nil {
			return Plan{}, fmt.Errorf("package.json is not valid JSON")
		}
		if pkg.Scripts["build"] != "" {
			p := Plan{Kind: "build", Framework: "Node.js build", Output: "dist", SPA: true}
			for _, f := range frameworks {
				_, a := pkg.Dependencies[f.pkg]
				_, b := pkg.DevDependencies[f.pkg]
				if a || b {
					p.Framework, p.Output, p.SPA, p.Note = f.name, f.out, f.spa, f.note
					break
				}
			}
			switch {
			case has("pnpm-lock.yaml"):
				p.Install, p.Build = "pnpm install --frozen-lockfile", "pnpm run build"
			case has("yarn.lock"):
				p.Install, p.Build = "yarn install --frozen-lockfile", "yarn build"
			case has("bun.lockb") || has("bun.lock"):
				p.Install, p.Build = "npm install", "npm run build"
				p.Note = strings.TrimSpace(p.Note + " Built with npm (Bun is not set up on the runner).")
			case has("package-lock.json"):
				p.Install, p.Build = "npm ci", "npm run build"
			default:
				p.Install, p.Build = "npm install", "npm run build"
			}
			for _, f := range []string{".nvmrc", ".node-version"} {
				if has(f) {
					p.Node = f
					break
				}
			}
			return p, nil
		}
	}
	// Published as is: the folder holding index.html.
	for _, dir := range []string{"", "public", "site", "docs", "www", "dist", "build"} {
		if dir == "" && has("index.html") || dir != "" && has(dir+"/") && s.hasIndex(ctx, repo, dir, branch) {
			name := "Plain HTML"
			if dir != "" {
				name += " (" + dir + "/)"
			}
			return Plan{Kind: "static", Framework: name, Dir: dir}, nil
		}
	}
	return Plan{}, fmt.Errorf("no index.html and no package.json with a build script at the top of %s (branch %s)", repo, branch)
}

func (s *Service) hasIndex(ctx context.Context, repo, dir, branch string) bool {
	names, err := s.GitHub.List(ctx, repo, dir, branch)
	return err == nil && slices.Contains(names, "index.html")
}

// workflowFile is where the build workflow lives in the repository.
const workflowFile = "fleetwatch.yml"

// BuildBranch receives each build's files; servers deploy from it.
const BuildBranch = github.BuildBranchName

// workflow is the GitHub Actions workflow that builds the site on every push to branch
// and publishes the result (a single commit, force-pushed) to BuildBranch.
func workflow(p Plan, branch string) string {
	node := "          node-version: lts/*\n"
	if p.Node != "" {
		node = "          node-version-file: " + p.Node + "\n"
	}
	corepack := ""
	if strings.HasPrefix(p.Install, "pnpm") || strings.HasPrefix(p.Install, "yarn") {
		corepack = "      - run: corepack enable\n"
	}
	return `# Added by Fleetwatch: builds this site on every push to ` + branch + ` and publishes the
# result to the ` + BuildBranch + ` branch, which your server deploys from. Delete this file
# (and the site in Fleetwatch) to stop.
name: Fleetwatch build
on:
  push:
    branches: [` + yamlQuote(branch) + `]
  workflow_dispatch:
permissions:
  contents: write
concurrency:
  group: fleetwatch-build
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
` + node + corepack + `      - name: Write .env from the FLEETWATCH_ENV secret
        env:
          FLEETWATCH_ENV: ${{ secrets.FLEETWATCH_ENV }}
        run: if [ -n "$FLEETWATCH_ENV" ]; then printf '%s\n' "$FLEETWATCH_ENV" > .env; fi
      - run: ` + p.Install + `
      - run: ` + p.Build + `
      - name: Publish to ` + BuildBranch + `
        run: |
          out=` + shellQuote(p.Output) + `
          test -f "$out/index.html" || { echo "::error::no index.html in $out: check the output folder in Fleetwatch"; exit 1; }
          if [ -f Caddyfile ]; then cp Caddyfile "$out/.fleetwatch.Caddyfile"; fi
          cd "$out"
          git init -q -b ` + BuildBranch + `
          git config user.name "Fleetwatch"
          git config user.email "fleetwatch@users.noreply.github.com"
          git add -A
          git commit -qm "Build of ${GITHUB_SHA}"
          git push -qf "https://x-access-token:${{ github.token }}@github.com/${{ github.repository }}.git" ` + BuildBranch + `
`
}

func yamlQuote(s string) string  { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
