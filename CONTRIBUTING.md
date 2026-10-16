# Contributing to Fleetwatch

Thanks for helping! Fleetwatch is source-available: anyone can read, use and suggest
changes to the code. Changes reach this repository only through pull requests that the
maintainer reviews and merges; nobody else can push to it.

## How to send a change

1. **Fork** this repository (the "Fork" button on GitHub) and clone your fork.
2. Create a branch: `git checkout -b fix-something`.
3. Make your change. Keep it focused: one fix or feature per pull request.
4. Check it:
   ```bash
   go vet ./...
   go test ./...
   cd web && npx tsc --noEmit
   ```
5. Push the branch to your fork and open a **pull request** against `main`.
   Describe what it changes and why; screenshots help for UI changes.

The maintainer reviews every pull request. The automatic checks on a pull request from
outside start once the maintainer approves them, so do not worry if they wait.

## Bigger ideas

For a new feature or a large change, open an issue first to talk it through: it saves
work on both sides.

## Security issues

Do not open a public issue. Use **Security → Report a vulnerability** on this repository
to report it privately.

## License

By sending a pull request you agree that your contribution is released under the
the project license (see LICENSE).
