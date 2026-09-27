// Package proto holds the types shared by the ship client and the shipd agent.
// The agent always writes exactly one JSON Response to stdout; progress goes to stderr.
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
	CodeSSH            = "ssh_failed"
	CodeServerNotReady = "server_not_ready"
	CodeStackUnknown   = "stack_not_detected"
	CodeBuild          = "build_failed"
	CodeUpload         = "upload_failed"
	CodeContainer      = "container_failed"
	CodeHealth         = "health_check_failed"
	CodeProxy          = "proxy_config_failed"
	CodeNotFound       = "app_not_found"
	CodeNoPrevious     = "no_previous_release"
	CodeLocked         = "deploy_in_progress"
	CodeConfirm        = "confirmation_required"
	CodeInternal       = "internal"
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
	case CodeConfig, CodeStackUnknown, CodeServerNotReady:
		return 3
	case CodeBuild, CodeUpload:
		return 4
	case CodeContainer, CodeHealth, CodeProxy, CodeLocked:
		return 5
	case CodeSSH:
		return 6
	case CodeNotFound, CodeNoPrevious:
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
	Current    *Release  `json:"current,omitempty"`
	Previous   *Release  `json:"previous,omitempty"`
	CertByShip bool      `json:"cert_by_ship,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type AppStatus struct {
	App
	State    string `json:"state"` // running | exited | restarting | missing
	Healthy  bool   `json:"healthy"`
	Restarts int    `json:"restarts"`
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
	Arch       string   `json:"arch"`
	PublicIP   string   `json:"public_ip"`
	BaseDomain string   `json:"base_domain,omitempty"`
	NginxDir   string   `json:"nginx_dir"`
	SSLListen  []string `json:"ssl_listen"`
	HTTPListen []string `json:"http_listen"`
	Docker     string   `json:"docker"`
	Nginx      string   `json:"nginx"`
	Certbot    bool     `json:"certbot"`
	Apps       int      `json:"apps"`
}
