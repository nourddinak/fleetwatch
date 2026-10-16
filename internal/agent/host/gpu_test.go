package host

import "testing"

func TestParseSMI(t *testing.T) {
	g := parseSMI("NVIDIA A10G, 87, 20480, 23028, 61, 142.31\nTesla T4, 0, 0, 15360, 34, [N/A]\n")
	if len(g) != 2 || g[0].Name != "NVIDIA A10G" || g[0].Util != 87 || g[0].MemUsed != 20480<<20 || g[0].TempC != 61 || g[0].PowerW != 142.31 {
		t.Fatalf("%+v", g)
	}
	if g[1].PowerW != 0 || g[1].MemTotal != 15360<<20 {
		t.Fatalf("%+v", g[1])
	}
	if parseSMI("") != nil {
		t.Fatal("empty output")
	}
}
