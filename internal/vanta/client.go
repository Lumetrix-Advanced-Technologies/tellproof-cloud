// Package vanta sends evidence objectives and packs to a vendor's Vanta
// account through their own private integration (P6-1, D-049). Each hour the
// sync mints an access token with the client-credentials grant, replaces the
// vendor's Tellproof custom resources (one per objective per AI system, from
// its latest pack) in one full-state PUT, and uploads packs not yet sent as
// files of a Vanta document. Credentials and tokens are never logged or put in
// an error; errors carry only HTTP status codes.
package vanta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"time"
)

// Scopes the sync asks for.
const (
	ScopeWriteResource = "connectors.self:write-resource"
	ScopeWriteDocument = "connectors.self:write-document"
)

// Client calls the Vanta API.
type Client struct {
	BaseURL string // default https://api.vanta.com
	HTTP    *http.Client
}

// APIError is a reply other than success. It holds no reply body: Vanta's
// error text may echo what was sent.
type APIError struct {
	Op     string
	Status int
}

func (e *APIError) Error() string { return fmt.Sprintf("vanta: %s: HTTP %d", e.Op, e.Status) }

// RateLimited reports whether err is Vanta's 429 (20 requests a minute per token).
func RateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests
}

// Unauthorised reports whether Vanta refused the credentials or a scope.
func Unauthorised(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden)
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.vanta.com"
}

func (c *Client) do(ctx context.Context, op string, req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tellproof-vanta-sync")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is ours, but keep errors to the cause
		}
		return fmt.Errorf("vanta: %s: %w", op, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return &APIError{Op: op, Status: resp.StatusCode}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("vanta: %s: unreadable reply", op)
		}
	}
	return nil
}

// Token mints an access token. Vanta keeps one token per application:
// minting revokes the previous one, so each sync mints its own.
func (c *Client) Token(ctx context.Context, clientID, clientSecret string, scopes string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id": clientID, "client_secret": clientSecret, "scope": scopes, "grant_type": "client_credentials",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.do(ctx, "token", req, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", errors.New("vanta: token: the reply has no access token")
	}
	return tok.AccessToken, nil
}

// Resource is one custom resource. Properties are flat (string, boolean,
// int32 or an RFC 3339 timestamp string), as the resource's schema in the
// Vanta developer console declares them.
type Resource struct {
	DisplayName      string         `json:"displayName"`
	UniqueID         string         `json:"uniqueId"`
	ExternalURL      string         `json:"externalUrl"`
	CustomProperties map[string]any `json:"customProperties"`
}

// SyncResources replaces every custom resource of resourceID with resources:
// any not sent is deleted in Vanta.
func (c *Client) SyncResources(ctx context.Context, token, resourceID string, resources []Resource) error {
	if resources == nil {
		resources = []Resource{}
	}
	body, err := json.Marshal(map[string]any{"resourceId": resourceID, "resources": resources})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base()+"/v1/resources/custom_resource", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	var out struct {
		Success bool `json:"success"`
	}
	if err := c.do(ctx, "sync resources", req, &out); err != nil {
		return err
	}
	if !out.Success {
		return errors.New("vanta: sync resources: Vanta did not accept the resources")
	}
	return nil
}

// Upload adds a file to a Vanta document.
func (c *Client) Upload(ctx context.Context, token, documentID, filename string, content []byte, effective time.Time, description string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", "application/json")
	fw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, _ = fw.Write(content)
	_ = mw.WriteField("effectiveAtDate", effective.UTC().Format(time.RFC3339))
	_ = mw.WriteField("description", description)
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v1/documents/"+url.PathEscape(documentID)+"/uploads", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	return c.do(ctx, "upload document", req, nil)
}
