`scripts/build.ps1` builds `ops-agent-linux-amd64` and `ops-agent-linux-arm64` into this
folder before building ops-server, which embeds them and serves them to `install.sh`.
The binaries are gitignored; this file keeps the folder (and the go:embed pattern) valid.
