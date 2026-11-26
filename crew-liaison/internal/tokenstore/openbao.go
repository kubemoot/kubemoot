package tokenstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBaoReply bounds what is read from the server, so a hostile or broken one cannot fill
// memory.
const maxBaoReply = 1 << 20

// OpenBao is a Backend in an OpenBao (or Vault compatible) KV version 2 mount, for installs
// that want a vault with its own audit log and policies. A record is one secret at
// "<prefix>/<group>/<name>"; the compare-and-swap is KV v2's "cas" option on the secret's
// version, and the time to live is the secret's "delete_version_after", set before each write.
type OpenBao struct {
	client *http.Client
	base   *url.URL
	mount  string
	prefix string
	token  func() (string, error)
}

// OpenBaoConfig is what NewOpenBao needs.
type OpenBaoConfig struct {
	// Address is the server, https unless AllowInsecure.
	Address string
	// Mount is the KV v2 mount, for example "secret".
	Mount string
	// Prefix is the path the liaison keeps its records under inside the mount.
	Prefix string
	// Token returns the token to present, read each time so a rotated token is picked up.
	Token func() (string, error)
	// Client carries the install CA; nil is a plain client with a timeout.
	Client *http.Client
	// AllowInsecure accepts an http address (tests only; the chart never sets it).
	AllowInsecure bool
}

// NewOpenBao returns the backend. Nothing is asked of the server until the first use.
func NewOpenBao(cfg OpenBaoConfig) (*OpenBao, error) {
	u, err := url.Parse(cfg.Address)
	if err != nil || u.Host == "" {
		return nil, errors.New("openbao: the address is not a URL")
	}
	if u.Scheme != "https" && (!cfg.AllowInsecure || u.Scheme != "http") {
		return nil, errors.New("openbao: the address must be https")
	}
	if cfg.Token == nil {
		return nil, errors.New("openbao: a token source is required")
	}
	mount := strings.Trim(cfg.Mount, "/")
	if mount == "" {
		mount = "secret"
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix == "" {
		prefix = "kubemoot-liaison"
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &OpenBao{client: client, base: u, mount: mount, prefix: prefix, token: cfg.Token}, nil
}

func (b *OpenBao) path(kind, group, name string) string {
	p := "/v1/" + b.mount + "/" + kind + "/" + b.prefix
	if group != "" {
		p += "/" + group
	}
	if name != "" {
		p += "/" + name
	}
	return p
}

// call sends one request and returns the status and the bounded body. The token is never
// part of an error.
func (b *OpenBao) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	tok, err := b.token()
	if err != nil || tok == "" {
		return 0, nil, errors.New("token store: the openbao token is not available")
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base.JoinPath(path).String(), rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Vault-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, nil, errors.New("token store: openbao is not reachable")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBaoReply))
	return resp.StatusCode, data, err
}

// Get implements Backend.
func (b *OpenBao) Get(ctx context.Context, group, name string) (Entry, error) {
	status, body, err := b.call(ctx, http.MethodGet, b.path("data", group, name), nil)
	if err != nil {
		return Entry{}, err
	}
	if status == http.StatusNotFound {
		return Entry{}, ErrNotFound
	}
	if status != http.StatusOK {
		return Entry{}, baoStatus(status)
	}
	var reply struct {
		Data struct {
			Data     map[string]string `json:"data"`
			Metadata struct {
				Version uint64 `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return Entry{}, errors.New("token store: openbao answered something unreadable")
	}
	raw, err := base64.StdEncoding.DecodeString(reply.Data.Data["v"])
	if err != nil {
		return Entry{}, errors.New("token store: openbao held a value that is not ours")
	}
	return Entry{Value: raw, Revision: reply.Data.Metadata.Version}, nil
}

// Put implements Backend.
func (b *OpenBao) Put(
	ctx context.Context, group, name string, value []byte, ttl time.Duration, ifRevision uint64,
) (uint64, error) {
	if ttl <= 0 {
		return 0, ErrBadTTL
	}
	status, _, err := b.call(ctx, http.MethodPost, b.path("metadata", group, name),
		map[string]any{"delete_version_after": wholeSeconds(ttl).String()})
	if err != nil {
		return 0, err
	}
	if status/100 != 2 {
		return 0, baoStatus(status)
	}
	payload := map[string]any{"data": map[string]string{"v": base64.StdEncoding.EncodeToString(value)}}
	if ifRevision != AnyRevision {
		payload["options"] = map[string]uint64{"cas": ifRevision}
	}
	status, body, err := b.call(ctx, http.MethodPost, b.path("data", group, name), payload)
	if err != nil {
		return 0, err
	}
	if status == http.StatusBadRequest && strings.Contains(string(body), "check-and-set") {
		return 0, ErrConflict
	}
	if status != http.StatusOK {
		return 0, baoStatus(status)
	}
	var reply struct {
		Data struct {
			Version uint64 `json:"version"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return 0, errors.New("token store: openbao answered something unreadable")
	}
	return reply.Data.Version, nil
}

// Delete implements Backend: every version and the metadata go.
func (b *OpenBao) Delete(ctx context.Context, group, name string) error {
	status, _, err := b.call(ctx, http.MethodDelete, b.path("metadata", group, name), nil)
	if err != nil {
		return err
	}
	if status/100 != 2 && status != http.StatusNotFound {
		return baoStatus(status)
	}
	return nil
}

// Names implements Backend.
func (b *OpenBao) Names(ctx context.Context, group string) ([]string, error) {
	status, body, err := b.call(ctx, "LIST", b.path("metadata", group, ""), nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, baoStatus(status)
	}
	var reply struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return nil, errors.New("token store: openbao answered something unreadable")
	}
	return reply.Data.Keys, nil
}

func baoStatus(status int) error {
	return fmt.Errorf("token store: openbao answered %d", status)
}
