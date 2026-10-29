package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type HTTP struct{}

func (HTTP) Capability() Capability { return Capability{Mode: "idempotent_write", Lookup: true} }

func validateEndpoint(g Grant) error {
	u, err := url.Parse(g.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || !filepath.IsAbs(g.CredentialFile) {
		return ErrDenied
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return ErrDenied
		}
	}
	return nil
}

// PrivateCredential reads a bounded operator-owned token without following the
// final symlink. Credentials are never copied to task state or error messages.
func PrivateCredential(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", ErrDenied
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return "", ErrDenied
	}
	if s, ok := info.Sys().(*syscall.Stat_t); !ok || s.Uid != uint32(os.Getuid()) {
		return "", ErrDenied
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	token := strings.TrimSpace(string(b))
	if err != nil || len(b) > 8192 || token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return "", ErrDenied
	}
	return token, nil
}

func (HTTP) request(ctx context.Context, g Grant, c Call, action, method, path string, payload any, out any) (bool, error) {
	if err := g.Authorize(c, action); err != nil {
		return false, err
	}
	token, err := PrivateCredential(g.CredentialFile)
	if err != nil {
		return false, err
	}
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return false, errors.New("invalid connector request")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(g.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("invalid connector request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if p, ok := payload.(submission); ok {
		req.Header.Set("Idempotency-Key", p.OperationID)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	response, err := client.Do(req)
	if err != nil {
		return false, errors.New("connector transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && action == "lookup" {
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, errors.New("connector response refused")
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, g.MaxResponseBytes+1))
	if err != nil || int64(len(b)) > g.MaxResponseBytes {
		return false, errors.New("connector response exceeds limit or is unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return false, errors.New("invalid connector response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return false, errors.New("trailing connector response")
	}
	return true, nil
}

type submission struct {
	OperationID   string            `json:"operation_id"`
	RequestDigest string            `json:"request_digest"`
	Value         map[string]string `json:"value"`
}

func (h HTTP) Submit(ctx context.Context, g Grant, c Call, id, digest string) (Receipt, error) {
	var r Receipt
	_, err := h.request(ctx, g, c, "write", http.MethodPut, "/records/"+url.PathEscape(c.Target), submission{id, digest, c.Value}, &r)
	return r, err
}
func (h HTTP) Lookup(ctx context.Context, g Grant, c Call, id string) (*Receipt, error) {
	var r Receipt
	found, err := h.request(ctx, g, c, "lookup", http.MethodGet, "/operations/"+url.PathEscape(id), nil, &r)
	if !found {
		return nil, err
	}
	return &r, err
}
func (h HTTP) Read(ctx context.Context, g Grant, c Call) (State, error) {
	var s State
	_, err := h.request(ctx, g, c, "read", http.MethodGet, "/records/"+url.PathEscape(c.Target), nil, &s)
	return s, err
}
