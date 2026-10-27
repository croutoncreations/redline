package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

type Push struct {
	RelayURL      string `yaml:"relay_url"`
	AuthTokenFile string `yaml:"auth_token_file"`
}

func (p Push) Validate() error {
	if p.RelayURL == "" {
		return nil
	}
	u, err := url.Parse(p.RelayURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("push.relay_url must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return fmt.Errorf("push.relay_url requires HTTPS (HTTP is allowed only on loopback)")
	}
	info, err := os.Stat(p.AuthTokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("push.auth_token_file must be an existing regular file with mode 0600")
	}
	return nil
}

// ReadAuthToken reads the shared secret at startup without including its path
// or content in errors. It is never serialized into config or API responses.
func (p Push) ReadAuthToken() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	bytes, err := os.ReadFile(p.AuthTokenFile)
	if err != nil {
		return "", fmt.Errorf("cannot read push auth token file")
	}
	token := strings.TrimSpace(string(bytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("push auth token file must contain one nonempty line")
	}
	return token, nil
}
