package host

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

// smi is the path of nvidia-smi, or "" on hosts without an NVIDIA driver.
var smi = sync.OnceValue(func() string {
	p, _ := exec.LookPath("nvidia-smi")
	return p
})

// docker reports whether the docker CLI is installed (the dashboard then shows a Docker tab).
var docker = sync.OnceValue(func() bool {
	_, err := exec.LookPath("docker")
	return err == nil
})

// gpus reads every NVIDIA GPU; nil when there is none or the driver does not answer.
func gpus() []proto.GPU {
	if smi() == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, smi(), "--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	return parseSMI(string(out))
}

// parseSMI reads nvidia-smi CSV lines: name, util %, memory used MiB, memory total MiB,
// temperature °C, power W. Values the card does not report ("[N/A]") stay 0.
func parseSMI(out string) []proto.GPU {
	var list []proto.GPU
	num := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue
		}
		list = append(list, proto.GPU{
			Name: strings.TrimSpace(f[0]), Util: num(f[1]),
			MemUsed: uint64(num(f[2]) * (1 << 20)), MemTotal: uint64(num(f[3]) * (1 << 20)),
			TempC: num(f[4]), PowerW: num(f[5]),
		})
	}
	return list
}
