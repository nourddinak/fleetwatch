package projects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Runtime labels are what the dashboard shows: a language ("Go"), a framework
// ("Django", "React"), or a backend and a JavaScript frontend joined by " + "
// ("Rust + React", "Laravel + Vue", "Express + React").

// Markers are files whose presence makes a directory a project.
var Markers = []string{
	// JavaScript
	"package.json", "pnpm-lock.yaml", "yarn.lock", "package-lock.json", "bun.lockb", "bun.lock",
	"next.config.js", "next.config.mjs", "next.config.ts", "deno.json", "deno.jsonc",
	"ecosystem.config.js", "ecosystem.config.cjs",
	// other languages
	"go.mod", "Cargo.toml",
	"requirements.txt", "pyproject.toml", "Pipfile", "manage.py",
	"composer.json", "artisan", "Gemfile",
	"pom.xml", "build.gradle", "build.gradle.kts",
	"mix.exs", "pubspec.yaml", "Package.swift", "build.zig", "shard.yml", "stack.yaml",
	// general
	".git", "Dockerfile", "docker-compose.yml", "compose.yaml", "index.html",
}

// markerSuffixes mark projects whose key file is named after the project.
var markerSuffixes = []string{".csproj", ".fsproj", ".sln", ".cabal", ".nimble"}

// notSubMarkers say nothing about a subfolder's language (public/index.html, tooling).
var notSubMarkers = map[string]bool{".git": true, "Dockerfile": true, "docker-compose.yml": true, "compose.yaml": true, "index.html": true,
	"pnpm-lock.yaml": true, "yarn.lock": true, "package-lock.json": true, "ecosystem.config.js": true, "ecosystem.config.cjs": true}

// skipSubdirs never hold the app itself.
var skipSubdirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "venv": true, ".venv": true, "__pycache__": true, "bin": true, "obj": true}

func isMarker(name string) bool {
	if slices.Contains(Markers, name) {
		return true
	}
	for _, s := range markerSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func isSubMarker(name string) bool { return isMarker(name) && !notSubMarkers[name] }

// langOf maps every label to the language it runs on, so a process ("Python") can
// be matched with what its project says ("Django + React").
var langOf = map[string]string{
	"Node.js": "JavaScript", "Bun": "JavaScript", "Deno": "JavaScript",
	"Next.js": "JavaScript", "Nuxt": "JavaScript", "SvelteKit": "JavaScript", "Remix": "JavaScript", "Astro": "JavaScript",
	"NestJS": "JavaScript", "Fastify": "JavaScript", "Hono": "JavaScript", "Express": "JavaScript", "Koa": "JavaScript",
	"Angular": "JavaScript", "Svelte": "JavaScript", "Vue": "JavaScript", "Solid": "JavaScript", "Preact": "JavaScript", "React": "JavaScript",
	"Go": "Go", "Rust": "Rust", "Zig": "Zig", "Crystal": "Crystal", "Haskell": "Haskell", "Nim": "Nim", "C/C++": "C/C++",
	"Python": "Python", "Django": "Python", "FastAPI": "Python", "Flask": "Python",
	"PHP": "PHP", "Laravel": "PHP", "Symfony": "PHP",
	"Ruby": "Ruby", "Rails": "Ruby",
	"Java": "Java", "Kotlin": "Java", "Spring Boot": "Java",
	".NET": ".NET", "ASP.NET": ".NET",
	"Elixir": "Elixir", "Phoenix": "Elixir",
	"Dart": "Dart", "Flutter": "Dart",
	"Swift": "Swift", "Vapor": "Swift",
	"Perl": "Perl", "Lua": "Lua",
}

// compiled languages produce native binaries the agent may only see as "Binary".
var compiled = map[string]bool{"Go": true, "Rust": true, "Zig": true, "Crystal": true, "Haskell": true, "Nim": true, "C/C++": true, "Swift": true, "Dart": true}

// frontends are client-side frameworks a backend can serve as a static build.
var frontends = map[string]bool{"Angular": true, "Svelte": true, "Vue": true, "Solid": true, "Preact": true, "React": true}

// Combine merges what a process runs with what its project folder says. The process
// wins; the project only adds detail it agrees with.
func Combine(process, project string) string {
	switch {
	case project == "":
		return process
	case process == "":
		return project
	}
	parts := strings.Split(project, " + ")
	lang := langOf[process]
	switch {
	case lang != "" && langOf[parts[0]] == lang:
		return project // Python + "Django + React", Node.js + "Next.js", Go + "Go + React"
	case process == "Binary" && compiled[langOf[parts[0]]]:
		return project // an unrecognized native binary built from this project
	case lang != "" && lang != "JavaScript" && allOf(parts, frontends):
		return process + " + " + project // Go binary serving a React build
	}
	return process
}

func allOf(parts []string, set map[string]bool) bool {
	for _, p := range parts {
		if !set[p] {
			return false
		}
	}
	return true
}

// Runtime names what a project in dir is built with, from its marker files (which may
// sit one folder down, like "backend/go.mod") and, for frameworks, their contents.
func Runtime(dir string, markers []string) string {
	files := map[string][]string{} // marker name → paths relative to dir
	for _, m := range markers {
		name := m[strings.LastIndex(m, "/")+1:]
		files[name] = append(files[name], m)
	}
	has := func(names ...string) bool {
		for _, n := range names {
			if len(files[n]) > 0 {
				return true
			}
		}
		return false
	}
	hasSuffix := func(suffix string) []string {
		var out []string
		for name, paths := range files {
			if strings.HasSuffix(name, suffix) {
				out = append(out, paths...)
			}
		}
		return out
	}
	// contents of the named files, lower-cased, for framework checks
	read := func(paths ...string) string {
		var b strings.Builder
		for _, p := range paths {
			if raw, err := os.ReadFile(filepath.Join(dir, p)); err == nil && len(raw) < 1<<20 {
				b.Write(raw)
				b.WriteByte('\n')
			}
		}
		return strings.ToLower(b.String())
	}
	all := func(names ...string) []string {
		var out []string
		for _, n := range names {
			out = append(out, files[n]...)
		}
		return out
	}
	pick := func(text string, fallback string, pairs ...string) string {
		for i := 0; i+1 < len(pairs); i += 2 {
			if strings.Contains(text, pairs[i]) {
				return pairs[i+1]
			}
		}
		return fallback
	}

	backend := ""
	switch {
	case has("go.mod"):
		backend = "Go"
	case has("Cargo.toml"):
		backend = "Rust"
	case has("requirements.txt", "pyproject.toml", "Pipfile", "manage.py"):
		backend = pick(read(all("requirements.txt", "pyproject.toml", "Pipfile")...), "Python", "django", "Django", "fastapi", "FastAPI", "flask", "Flask")
		if has("manage.py") {
			backend = "Django"
		}
	case has("composer.json", "artisan"):
		backend = pick(read(all("composer.json")...), "PHP", "laravel/framework", "Laravel", "symfony/framework-bundle", "Symfony")
		if has("artisan") {
			backend = "Laravel"
		}
	case has("Gemfile"):
		backend = pick(read(all("Gemfile")...), "Ruby", `"rails"`, "Rails", `'rails'`, "Rails")
	case has("pom.xml", "build.gradle", "build.gradle.kts"):
		backend = pick(read(all("pom.xml", "build.gradle", "build.gradle.kts")...), "Java", "spring-boot", "Spring Boot", "kotlin", "Kotlin")
	case len(hasSuffix(".csproj")) > 0 || len(hasSuffix(".fsproj")) > 0 || len(hasSuffix(".sln")) > 0:
		backend = pick(read(append(hasSuffix(".csproj"), hasSuffix(".fsproj")...)...), ".NET", "microsoft.net.sdk.web", "ASP.NET")
	case has("mix.exs"):
		backend = pick(read(all("mix.exs")...), "Elixir", ":phoenix", "Phoenix")
	case has("pubspec.yaml"):
		backend = pick(read(all("pubspec.yaml")...), "Dart", "flutter:", "Flutter")
	case has("Package.swift"):
		backend = pick(read(all("Package.swift")...), "Swift", "vapor", "Vapor")
	case has("build.zig"):
		backend = "Zig"
	case has("shard.yml"):
		backend = "Crystal"
	case has("stack.yaml") || len(hasSuffix(".cabal")) > 0:
		backend = "Haskell"
	case len(hasSuffix(".nimble")) > 0:
		backend = "Nim"
	}

	full, server, front := jsFrameworks(dir, files["package.json"])
	if has("next.config.js", "next.config.mjs", "next.config.ts") {
		full = "Next.js"
	}
	jsRuntime := ""
	switch {
	case has("bun.lockb", "bun.lock"):
		jsRuntime = "Bun"
	case has("package.json"):
		jsRuntime = "Node.js"
	case has("deno.json", "deno.jsonc"):
		jsRuntime = "Deno"
	}

	switch {
	case backend != "" && full != "":
		return backend + " + " + full
	case backend != "" && front != "":
		return backend + " + " + front
	case backend != "":
		return backend // a package.json without a framework is build tooling here
	case full != "":
		return full
	case server != "" && front != "":
		return server + " + " + front
	case server != "":
		return server
	case front != "":
		return front
	case jsRuntime != "":
		return jsRuntime
	case has("index.html"):
		return "Static"
	}
	return ""
}

// jsFrameworks reads package.json dependencies: a full-stack framework, a server
// framework and a frontend framework, most specific first.
func jsFrameworks(dir string, pkgs []string) (full, server, front string) {
	fullList := [][2]string{{"next", "Next.js"}, {"nuxt", "Nuxt"}, {"@sveltejs/kit", "SvelteKit"}, {"@remix-run/node", "Remix"}, {"@remix-run/react", "Remix"}, {"astro", "Astro"}}
	serverList := [][2]string{{"@nestjs/core", "NestJS"}, {"fastify", "Fastify"}, {"hono", "Hono"}, {"express", "Express"}, {"koa", "Koa"}}
	frontList := [][2]string{{"@angular/core", "Angular"}, {"svelte", "Svelte"}, {"vue", "Vue"}, {"solid-js", "Solid"}, {"preact", "Preact"}, {"react", "React"}}
	slices.Sort(pkgs) // the top-level package.json first
	for _, p := range pkgs {
		raw, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		var pkg struct{ Dependencies, DevDependencies map[string]any }
		if json.Unmarshal(raw, &pkg) != nil {
			continue
		}
		dep := func(name string) bool { return pkg.Dependencies[name] != nil || pkg.DevDependencies[name] != nil }
		first := func(list [][2]string) string {
			for _, x := range list {
				if dep(x[0]) {
					return x[1]
				}
			}
			return ""
		}
		if full == "" {
			full = first(fullList)
		}
		if server == "" {
			server = first(serverList)
		}
		if front == "" {
			front = first(frontList)
		}
	}
	return full, server, front
}

// RuntimeFromExec recognizes interpreters and launchers by the executable's name.
func RuntimeFromExec(exe string) string {
	base := strings.ToLower(filepath.Base(strings.Fields(exe + " x")[0]))
	switch base {
	case "node", "nodejs", "npm", "npx", "pnpm", "yarn", "tsx", "ts-node", "nodemon":
		return "Node.js"
	case "bun", "bunx":
		return "Bun"
	case "deno":
		return "Deno"
	case "gunicorn", "uvicorn", "hypercorn", "daphne", "celery", "uwsgi", "poetry", "pipenv", "uv", "granian":
		return "Python"
	case "ruby", "puma", "unicorn", "sidekiq", "bundle", "rails", "rackup", "passenger":
		return "Ruby"
	case "java", "kotlin":
		return "Java"
	case "dotnet":
		return ".NET"
	case "beam.smp", "beam", "erl", "elixir", "iex", "mix":
		return "Elixir"
	case "perl", "starman", "hypnotoad", "plackup":
		return "Perl"
	case "lua", "luajit":
		return "Lua"
	case "go":
		return "Go" // go run
	case "cargo":
		return "Rust"
	case "dart":
		return "Dart"
	case "frankenphp":
		return "PHP"
	}
	switch {
	case strings.HasPrefix(base, "python"):
		return "Python"
	case strings.HasPrefix(base, "php"): // php8.3, php-fpm8.2
		return "PHP"
	}
	return ""
}
