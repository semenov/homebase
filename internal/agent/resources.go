package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/semenov/homebase/internal/proto"
)

var (
	sizeRe   = regexp.MustCompile(`^([\d.]+)\s*([kKMGTP]?i?B)$`)
	memoryRe = regexp.MustCompile(`(?i)^\d+[bkmg]?$`)
)

// parseSize converts docker's human sizes ("30.04MiB", "25.1kB") to bytes.
func parseSize(s string) int64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{
		"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
	}[m[2]]
	return int64(n * mult)
}

// memoryBytes converts a docker memory limit ("256m") to bytes.
func memoryBytes(s string) int64 {
	if s == "" {
		return 0
	}
	s = strings.ToLower(s)
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k':
		mult = 1 << 10
	case 'm':
		mult = 1 << 20
	case 'g':
		mult = 1 << 30
	}
	n, _ := strconv.ParseInt(strings.TrimRight(s, "bkmg"), 10, 64)
	return n * mult
}

type containerStats struct {
	cpu float64
	mem int64
}

// statsFor samples CPU and memory of the given containers in one docker call.
func statsFor(containers []string) map[string]containerStats {
	out := map[string]containerStats{}
	if len(containers) == 0 {
		return out
	}
	args := append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, containers...)
	raw, _ := output("docker", args...)
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		var s struct{ Name, CPUPerc, MemUsage string }
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(s.CPUPerc, "%"), 64)
		used, _, _ := strings.Cut(s.MemUsage, "/")
		out[s.Name] = containerStats{cpu: cpu, mem: parseSize(used)}
	}
	return out
}

func dirBytes(path string) int64 {
	out, err := output("du", "-sb", path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.Fields(out)[0], 10, 64)
	return n
}

func volumeBytes(a *proto.App) int64 {
	var total int64
	for _, v := range a.Volumes {
		total += dirBytes("/var/lib/docker/volumes/" + volumeName(a.Name, v) + "/_data")
	}
	return total
}

func databaseBytes(d *proto.Database) int64 {
	if d == nil {
		return 0
	}
	out, err := psql(fmt.Sprintf("SELECT pg_database_size('%s');", d.Name))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(out, 10, 64)
	return n
}

// oomKills counts out-of-memory kills of a container since t, from docker's
// event log (the OOMKilled flag is reset when the container restarts).
func oomKills(container string, since time.Time) int {
	out, err := output("docker", "events", "--since", strconv.FormatInt(since.Unix(), 10), "--until", strconv.FormatInt(time.Now().Unix(), 10),
		"--filter", "container="+container, "--filter", "event=oom", "--format", "{{.Time}}")
	if err != nil {
		return 0
	}
	return len(strings.Fields(out))
}

// fillResources adds resource usage to app statuses (one docker stats call for all).
func fillResources(sts []*proto.AppStatus) {
	var names []string
	for _, st := range sts {
		if st.Current != nil && st.State == "running" {
			names = append(names, st.Current.Container)
		}
	}
	stats := statsFor(names)
	for _, st := range sts {
		r := &proto.Resources{MemLimitBytes: memoryBytes(st.Memory), VolumeBytes: volumeBytes(&st.App), DatabaseBytes: databaseBytes(st.Database)}
		if st.Current != nil {
			if cs, ok := stats[st.Current.Container]; ok {
				r.CPUPercent, r.MemBytes = cs.cpu, cs.mem
			}
		}
		st.Resources = r
	}
}

// hostResources fills the server-wide numbers of info.
func hostResources(info *proto.ServerInfo) {
	info.CPUs = runtime.NumCPU()
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		info.Load1, _ = strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				info.MemTotal = kb << 10
			case "MemAvailable:":
				info.MemAvailable = kb << 10
			}
		}
		f.Close()
	}
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) == nil {
		info.DiskTotal = int64(fs.Blocks) * int64(fs.Bsize)
		info.DiskFree = int64(fs.Bavail) * int64(fs.Bsize)
	}
}
