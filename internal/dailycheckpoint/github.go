package dailycheckpoint

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHub publishes files to a repository with the REST contents API. It
// never overwrites: a file already there with the same bytes is fine (its
// commit is returned), different bytes are an error. The token is never
// logged or put in an error.
type GitHub struct {
	Repo    string // owner/name
	Token   string
	BaseURL string // default https://api.github.com
	HTTP    *http.Client
}

func (g *GitHub) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "tellproof-checkpoint")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("github: %s %s: unreadable reply", method, path)
		}
	}
	return resp.StatusCode, nil
}

// Publish creates path with content and returns the commit SHA.
func (g *GitHub) Publish(ctx context.Context, path string, content []byte, message string) (string, error) {
	var created struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	code, err := g.do(ctx, http.MethodPut, "/repos/"+g.Repo+"/contents/"+path,
		map[string]string{"message": message, "content": base64.StdEncoding.EncodeToString(content)}, &created)
	if err != nil {
		return "", err
	}
	switch code {
	case http.StatusCreated, http.StatusOK:
		if created.Commit.SHA == "" {
			return "", errors.New("github: the reply names no commit")
		}
		return created.Commit.SHA, nil
	case http.StatusUnprocessableEntity, http.StatusConflict: // already there (a retry after a crash)
		return g.existing(ctx, path, content)
	}
	return "", fmt.Errorf("github: publishing %s: HTTP %d", path, code)
}

func (g *GitHub) existing(ctx context.Context, path string, content []byte) (string, error) {
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	code, err := g.do(ctx, http.MethodGet, "/repos/"+g.Repo+"/contents/"+path, nil, &file)
	if err != nil || code != http.StatusOK {
		return "", fmt.Errorf("github: reading the existing %s: HTTP %d %v", path, code, err)
	}
	got, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || file.Encoding != "base64" || !bytes.Equal(got, content) {
		return "", fmt.Errorf("github: a different %s is already published; checkpoints are never overwritten", path)
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	code, err = g.do(ctx, http.MethodGet, "/repos/"+g.Repo+"/commits?path="+path+"&per_page=1", nil, &commits)
	if err != nil || code != http.StatusOK || len(commits) == 0 {
		return "", fmt.Errorf("github: finding the commit of %s: HTTP %d %v", path, code, err)
	}
	return commits[0].SHA, nil
}
