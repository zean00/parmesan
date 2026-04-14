package toolsecurity

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ProviderURLPolicy struct {
	AllowedHosts   []string
	AllowLocalDev  bool
	RequestTimeout time.Duration
}

func (p ProviderURLPolicy) Validate(rawURL string) error {
	if len(p.AllowedHosts) == 0 && !p.AllowLocalDev {
		return nil
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("provider uri is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid provider uri: %w", err)
	}
	if !parsed.IsAbs() {
		return errors.New("provider uri must be absolute")
	}
	host := normalizeHost(parsed.Hostname())
	if host == "" {
		return errors.New("provider uri host is required")
	}
	if isLocalHost(host) {
		if !p.AllowLocalDev {
			return errors.New("provider uri local hosts are not allowed")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("provider uri local hosts must use http or https")
		}
		return nil
	}
	if parsed.Scheme != "https" {
		return errors.New("provider uri must use https")
	}
	if len(p.AllowedHosts) == 0 {
		return errors.New("provider uri host is not allowed")
	}
	for _, item := range p.AllowedHosts {
		if host == normalizeHost(item) {
			return nil
		}
	}
	return fmt.Errorf("provider uri host %q is not allowed", host)
}

func (p ProviderURLPolicy) HTTPClient() *http.Client {
	timeout := p.RequestTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return p.Validate(req.URL.String())
		},
	}
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func isLocalHost(host string) bool {
	switch normalizeHost(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
