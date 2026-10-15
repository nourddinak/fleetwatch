package projects

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	react = `{"dependencies":{"react":"18","react-dom":"18"},"devDependencies":{"vite":"5"}}`
	vue   = `{"dependencies":{"vue":"3"}}`
)

func TestRuntime(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		// a backend and a JavaScript frontend, side by side or in subfolders
		{"go api + react at the top", map[string]string{"package.json": react, "backend/go.mod": "module x"}, "Go + React"},
		{"go root + vue folder", map[string]string{"go.mod": "module x", "frontend/package.json": vue}, "Go + Vue"},
		{"rust + react", map[string]string{"server/Cargo.toml": "[package]", "web/package.json": react}, "Rust + React"},
		{"rust + svelte", map[string]string{"Cargo.toml": "", "package.json": `{"devDependencies":{"svelte":"4"}}`}, "Rust + Svelte"},
		{"django + react", map[string]string{"manage.py": "", "requirements.txt": "Django==5.0", "frontend/package.json": react}, "Django + React"},
		{"fastapi + vue", map[string]string{"api/requirements.txt": "fastapi\nuvicorn", "ui/package.json": vue}, "FastAPI + Vue"},
		{"flask", map[string]string{"pyproject.toml": `dependencies = ["Flask>=3"]`}, "Flask"},
		{"laravel + vue", map[string]string{"artisan": "", "composer.json": `{"require":{"laravel/framework":"^11"}}`, "package.json": vue}, "Laravel + Vue"},
		{"symfony", map[string]string{"composer.json": `{"require":{"symfony/framework-bundle":"7"}}`}, "Symfony"},
		{"rails + react", map[string]string{"Gemfile": "gem \"rails\", \"~> 7.1\"", "package.json": react}, "Rails + React"},
		{"spring boot + angular", map[string]string{"pom.xml": "<artifactId>spring-boot-starter-web</artifactId>", "client/package.json": `{"dependencies":{"@angular/core":"17"}}`}, "Spring Boot + Angular"},
		{"kotlin", map[string]string{"build.gradle.kts": `plugins { kotlin("jvm") }`}, "Kotlin"},
		{"asp.net + react", map[string]string{"Api/Api.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">`, "ClientApp/package.json": react}, "ASP.NET + React"},
		{"phoenix", map[string]string{"mix.exs": "{:phoenix, \"~> 1.7\"}"}, "Phoenix"},
		{"go + next.js", map[string]string{"go.mod": "", "web/package.json": `{"dependencies":{"next":"14","react":"18"}}`}, "Go + Next.js"},
		{"zig", map[string]string{"build.zig": ""}, "Zig"},
		{"haskell", map[string]string{"app.cabal": ""}, "Haskell"},
		// JavaScript only
		{"express api", map[string]string{"package.json": `{"dependencies":{"express":"4"}}`}, "Express"},
		{"express + react", map[string]string{"package.json": `{"dependencies":{"express":"4","react":"18"}}`}, "Express + React"},
		{"nestjs", map[string]string{"package.json": `{"dependencies":{"@nestjs/core":"10"}}`}, "NestJS"},
		{"next", map[string]string{"package.json": `{"dependencies":{"next":"14","react":"18"}}`}, "Next.js"},
		{"nuxt", map[string]string{"package.json": `{"dependencies":{"nuxt":"3"}}`}, "Nuxt"},
		{"sveltekit", map[string]string{"package.json": `{"devDependencies":{"@sveltejs/kit":"2","svelte":"4"}}`}, "SvelteKit"},
		{"vite react spa", map[string]string{"package.json": react}, "React"},
		{"plain node", map[string]string{"package.json": `{"dependencies":{"ws":"8"}}`}, "Node.js"},
		{"bun", map[string]string{"package.json": `{}`, "bun.lockb": ""}, "Bun"},
		{"deno", map[string]string{"deno.json": "{}"}, "Deno"},
		// edges
		{"build tooling is not an app part", map[string]string{"Cargo.toml": "", "package.json": `{"devDependencies":{"tailwindcss":"3"}}`}, "Rust"},
		{"dependencies are not the app", map[string]string{"go.mod": "", "node_modules/react/package.json": `{}`}, "Go"},
		{"static", map[string]string{"index.html": "<h1>"}, "Static"},
		{"public/index.html is not static", map[string]string{"go.mod": "", "public/index.html": ""}, "Go"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for name, body := range c.files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		}
		p := Detect(dir)
		if p == nil {
			t.Errorf("%s: not detected as a project", c.name)
		} else if p.Runtime != c.want {
			t.Errorf("%s: got %q (markers %v), want %q", c.name, p.Runtime, p.Markers, c.want)
		}
	}
}

func TestCombine(t *testing.T) {
	cases := []struct{ process, project, want string }{
		{"Go", "React", "Go + React"},                  // Go binary serving a React build
		{"Go", "Go + React", "Go + React"},             // project agrees
		{"Rust", "Rust + Svelte", "Rust + Svelte"},     //
		{"Rust", "Vue", "Rust + Vue"},                  //
		{"Python", "Django + React", "Django + React"}, // framework refines the interpreter
		{"Python", "Flask", "Flask"},                   //
		{"Java", "Spring Boot + Angular", "Spring Boot + Angular"},
		{"Node.js", "Next.js", "Next.js"},
		{"Node.js", "Express + React", "Express + React"},
		{"Bun", "Hono", "Hono"},
		{"Binary", "Rust + React", "Rust + React"}, // unrecognized native binary of a Rust project
		{"Binary", "React", "Binary"},
		{"Go", "Node.js", "Go"},              // the old bug: package.json must not win over a Go binary
		{"Go", "Express", "Go"},              // a server framework is not this process
		{"Node.js", "Go + React", "Node.js"}, // a Node process in a Go project (dev server, tooling)
		{"Python", "", "Python"},
		{"", "Laravel", "Laravel"},
		{"Go", "Next.js", "Go"},
	}
	for _, c := range cases {
		if got := Combine(c.process, c.project); got != c.want {
			t.Errorf("Combine(%q, %q) = %q, want %q", c.process, c.project, got, c.want)
		}
	}
}

func TestRuntimeFromExec(t *testing.T) {
	for exe, want := range map[string]string{
		"/usr/bin/node server.js": "Node.js", "/usr/bin/npm start": "Node.js", "/usr/local/bin/tsx src/index.ts": "Node.js",
		"/root/.bun/bin/bun run index.ts": "Bun", "/usr/bin/deno run main.ts": "Deno",
		"/usr/bin/python3.12 app.py": "Python", "/srv/venv/bin/gunicorn app:app": "Python", "/srv/venv/bin/uvicorn main:app": "Python", "/usr/bin/celery -A x worker": "Python",
		"/usr/sbin/php-fpm8.2 --nodaemonize": "PHP", "/usr/local/bin/frankenphp run": "PHP",
		"/usr/local/bin/puma -C config/puma.rb": "Ruby", "/usr/bin/java -jar app.jar": "Java", "/usr/bin/dotnet Api.dll": ".NET",
		"/usr/lib/erlang/erts-14/bin/beam.smp": "Elixir", "/usr/local/go/bin/go run .": "Go", "/usr/bin/perl app.pl": "Perl",
		"/usr/local/bin/node_exporter": "", "/opt/app/server": "",
	} {
		if got := RuntimeFromExec(exe); got != want {
			t.Errorf("%s: %q want %q", exe, got, want)
		}
	}
}

func TestBinaryRuntime(t *testing.T) {
	self, err := os.Executable() // this test binary is a Go program
	if err != nil {
		t.Fatal(err)
	}
	if rt := BinaryRuntime(self); rt != "Go" {
		t.Fatalf("go binary: %q", rt)
	}
	script := filepath.Join(t.TempDir(), "start.sh")
	os.WriteFile(script, []byte("#!/bin/sh\nexec node server.js\n"), 0o755)
	if rt := BinaryRuntime(script); rt != "" {
		t.Fatalf("script: %q", rt)
	}
	if rt := ExecRuntime("/usr/bin/node /srv/app/server.js", 0); rt != "Node.js" {
		t.Fatalf("interpreter: %q", rt)
	}
	if rt := ExecRuntime(self+" --port 5000", 0); rt != "Go" {
		t.Fatalf("exec start of a go binary: %q", rt)
	}
}
