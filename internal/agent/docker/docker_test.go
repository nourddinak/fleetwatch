package docker

import (
	"context"
	"testing"

	"fleetwatch/internal/proto"
)

func TestSize(t *testing.T) {
	for in, want := range map[string]int64{"12.3MB": 12_300_000, "1.5GiB": 1610612736, "0B": 0, "512kB": 512_000, "7": 7, "": 0} {
		if got := size(in); got != want {
			t.Errorf("size(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestLinesAndLabels(t *testing.T) {
	rows := lines[psRow](`{"ID":"abc","Names":"web","Labels":"com.docker.compose.project=shop,com.docker.compose.service=web"}
not json
{"ID":"def","Names":"db"}`)
	if len(rows) != 2 || labels(rows[0].Labels)["com.docker.compose.project"] != "shop" {
		t.Fatalf("%+v", rows)
	}
}

func TestActRefusesBadInput(t *testing.T) {
	for _, a := range []proto.DockerAction{
		{Kind: "container", Target: "web; rm -rf /", Action: "stop"},
		{Kind: "container", Target: "-v", Action: "remove"},
		{Kind: "project", Target: "../etc", Action: "start"},
		{Kind: "volume", Target: "data", Action: "prune"},
		{Kind: "network", Target: "x", Action: "remove"},
	} {
		if res := Act(context.Background(), a); res.OK {
			t.Errorf("accepted %+v", a)
		}
	}
}

func TestRepoDigests(t *testing.T) {
	got := repoDigests("sha256:aaa [\"nginx@sha256:111\",\"docker.io/library/nginx@sha256:111\"]\nsha256:bbb []\nsha256:ccc null\n")
	if len(got["sha256:aaa"]) != 2 || got["sha256:aaa"][0] != "sha256:111" || len(got["sha256:bbb"]) != 0 {
		t.Fatal(got)
	}
}
