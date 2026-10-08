// Package proto holds the types shared by the homebase client and the shipd
// agent on the server, and homebase's error codes. The agent always writes
// exactly one JSON Response to stdout; progress goes to stderr.
package proto

import (
	"encoding/json"
	"fmt"
	"time"
)

// Error codes are stable: agents may branch on them.
const (
	CodeUsage          = "usage"
	CodeConfig         = "config"
	CodeStackUnknown   = "stack_not_detected"
	CodeDepsMissing    = "dependencies_missing"
	CodeConfirm        = "confirmation_required"
	CodeServerNotReady = "server_not_ready"
	CodeShareTaken     = "share_taken"

	// the dev server on this Mac
	CodePortInUse      = "port_in_use"
	CodeStartFailed    = "start_failed"  // the process exited while starting
	CodeNotListening   = "not_listening" // running, but the port never opened
	CodeLaunchd        = "launchd_failed"
	CodeServerNotFound = "server_not_found" // no dev server for this folder

	// deploys
	CodeBuild      = "build_failed"
	CodeUpload     = "upload_failed"
	CodeContainer  = "container_failed"
	CodeHealth     = "health_check_failed"
	CodeProxy      = "proxy_config_failed"
	CodeRelease    = "release_command_failed"
	CodeDatabase   = "database_failed"
	CodeLocked     = "deploy_in_progress"
	CodeSSH        = "ssh_failed"
	CodeNotFound   = "app_not_found" // not deployed on the server
	CodeNoPrevious = "no_previous_release"

	CodeInternal = "internal"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Logs    string `json:"logs,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func Errf(code, hint, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Hint: hint}
}

// ExitCode maps error codes to process exit codes.
func ExitCode(code string) int {
	switch code {
	case CodeUsage, CodeConfirm:
		return 2
	case CodeConfig, CodeStackUnknown, CodeDepsMissing, CodeServerNotReady, CodeShareTaken:
		return 3
	case CodeBuild, CodeUpload:
		return 4
	case CodePortInUse, CodeStartFailed, CodeNotListening, CodeLaunchd,
		CodeContainer, CodeHealth, CodeProxy, CodeLocked, CodeRelease, CodeDatabase:
		return 5
	case CodeSSH:
		return 6
	case CodeServerNotFound, CodeNotFound, CodeNoPrevious:
		return 7
	}
	return 1
}

type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

type Release struct {
	ID            string    `json:"id"`
	Image         string    `json:"image"`
	Container     string    `json:"container"`
	HostPort      int       `json:"host_port"`
	ContainerPort int       `json:"container_port"`
	DeployedAt    time.Time `json:"deployed_at"`
}

type App struct {
	Name       string    `json:"name"`
	Domain     string    `json:"domain"`
	URL        string    `json:"url"`
	TLS        bool      `json:"tls"`
	HealthPath string    `json:"health_path"`
	Volumes    []string  `json:"volumes,omitempty"` // container paths backed by persistent volumes
	Memory     string    `json:"memory,omitempty"`  // docker memory limit, e.g. "256m"; empty = none
	Current    *Release  `json:"current,omitempty"`
	Previous   *Release  `json:"previous,omitempty"`
	CertByShip bool      `json:"cert_by_ship,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Database is an app's database in the server's shared Postgres.
type Database struct {
	Engine    string    `json:"engine"`
	Name      string    `json:"name"`
	User      string    `json:"user"`
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"created_at"`
}

type Backup struct {
	App       string    `json:"app"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Resources is a point-in-time snapshot of what an app uses.
type Resources struct {
	CPUPercent    float64 `json:"cpu_percent"`               // 100 = one full core
	MemBytes      int64   `json:"mem_bytes"`                 // current memory use
	MemLimitBytes int64   `json:"mem_limit_bytes,omitempty"` // set when the app has a memory limit
	VolumeBytes   int64   `json:"volume_bytes,omitempty"`
	DatabaseBytes int64   `json:"database_bytes,omitempty"`
}

type AppStatus struct {
	App
	Database  *Database  `json:"database,omitempty"`
	Resources *Resources `json:"resources,omitempty"`
	OOMKills  int        `json:"oom_kills,omitempty"` // out-of-memory kills since the current release started
	State     string     `json:"state"`               // running | exited | restarting | stopped (no release, e.g. destroyed with data kept)
	Healthy   bool       `json:"healthy"`
	Restarts  int        `json:"restarts"`
}

type DeployResult struct {
	App      string   `json:"app"`
	URL      string   `json:"url"`
	Domain   string   `json:"domain"`
	TLS      bool     `json:"tls"`
	Release  Release  `json:"release"`
	Warnings []string `json:"warnings,omitempty"`
}

type ServerInfo struct {
	Arch         string   `json:"arch"`
	PublicIP     string   `json:"public_ip"`
	BaseDomain   string   `json:"base_domain,omitempty"`
	DevDomain    string   `json:"dev_domain,omitempty"`   // shares get <name>.<dev_domain>
	WildcardDNS  string   `json:"wildcard_dns,omitempty"` // set when *.base_domain has one shared certificate
	Proxy        string   `json:"proxy"`                  // caddy | nginx
	ProxyVersion string   `json:"proxy_version"`
	NginxDir     string   `json:"nginx_dir,omitempty"`
	SSLListen    []string `json:"ssl_listen,omitempty"`
	Docker       string   `json:"docker"`
	Apps         int      `json:"apps"`
	CPUs         int      `json:"cpus"`
	Load1        float64  `json:"load1"`
	MemTotal     int64    `json:"mem_total_bytes"`
	MemAvailable int64    `json:"mem_available_bytes"`
	DiskTotal    int64    `json:"disk_total_bytes"`
	DiskFree     int64    `json:"disk_free_bytes"`
}

// Share is a dev server published from someone's Mac at https://<name>.<dev domain>.
// Traffic goes from the server's proxy through the Mac's reverse SSH tunnel.
type Share struct {
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	URL        string    `json:"url"`
	Mac        string    `json:"mac"`      // id of the Mac that shares it
	MacName    string    `json:"mac_name"` // its name, for people
	TLS        bool      `json:"tls"`
	CertByShip bool      `json:"cert_by_ship,omitempty"` // nginx mode: certbot certificate to delete with the share
	CreatedAt  time.Time `json:"created_at"`
	Warnings   []string  `json:"warnings,omitempty"`
}

// Tunnel is where a Mac's reverse SSH tunnel ends on the server.
type Tunnel struct {
	Port      int    `json:"port"` // on 127.0.0.1
	DevDomain string `json:"dev_domain"`
}
