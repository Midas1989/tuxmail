// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ai implements chat completion clients for the optional AI
// assistance (KONZEPT 8.10): the OpenAI-compatible API (OpenAI, Ollama,
// DeepSeek, Mistral and custom endpoints) and the native Anthropic and
// Gemini APIs. Only the minimal non-streaming request is implemented.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default base URLs and models per provider.
var defaults = map[string][2]string{
	"openai":    {"https://api.openai.com/v1", "gpt-4o-mini"},
	"ollama":    {"http://localhost:11434/v1", "llama3.1"},
	"deepseek":  {"https://api.deepseek.com/v1", "deepseek-chat"},
	"mistral":   {"https://api.mistral.ai/v1", "mistral-small-latest"},
	"anthropic": {"https://api.anthropic.com/v1", "claude-3-5-haiku-latest"},
	"gemini":    {"https://generativelanguage.googleapis.com/v1beta", "gemini-1.5-flash"},
}

// Client is a provider client.
type Client struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
	HTTP     *http.Client
}

// New creates a client; empty base URL and model take the provider defaults.
func New(provider, baseURL, model, apiKey string, hc *http.Client) (*Client, error) {
	d, known := defaults[provider]
	if !known && provider != "custom" {
		return nil, fmt.Errorf("ai: unknown provider %q", provider)
	}
	if baseURL == "" {
		baseURL = d[0]
	}
	if model == "" {
		model = d[1]
	}
	if baseURL == "" || model == "" {
		return nil, errors.New("ai: base URL and model are required")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("ai: invalid base URL: %w", err)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{Provider: provider, BaseURL: strings.TrimRight(baseURL, "/"), Model: model, APIKey: apiKey, HTTP: hc}, nil
}

// Complete sends a system and a user prompt and returns the answer text.
func (c *Client) Complete(ctx context.Context, system, prompt string) (string, error) {
	switch c.Provider {
	case "anthropic":
		return c.anthropic(ctx, system, prompt)
	case "gemini":
		return c.gemini(ctx, system, prompt)
	}
	return c.openAI(ctx, system, prompt)
}

func (c *Client) post(ctx context.Context, u string, body any, hdr map[string]string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("ai: %s returned %s: %s", c.Provider, res.Status, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
	return json.Unmarshal(data, out)
}

func (c *Client) openAI(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{"model": c.Model, "messages": []map[string]string{
		{"role": "system", "content": system}, {"role": "user", "content": prompt}}}
	hdr := map[string]string{}
	if c.APIKey != "" {
		hdr["Authorization"] = "Bearer " + c.APIKey
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.post(ctx, c.BaseURL+"/chat/completions", body, hdr, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("ai: empty answer")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *Client) anthropic(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{"model": c.Model, "max_tokens": 1024, "system": system,
		"messages": []map[string]string{{"role": "user", "content": prompt}}}
	hdr := map[string]string{"x-api-key": c.APIKey, "anthropic-version": "2023-06-01"}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.post(ctx, c.BaseURL+"/messages", body, hdr, &out); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range out.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

func (c *Client) gemini(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": system}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}},
	}
	u := fmt.Sprintf("%s/models/%s:generateContent", c.BaseURL, url.PathEscape(c.Model))
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := c.post(ctx, u, body, map[string]string{"x-goog-api-key": c.APIKey}, &out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 {
		return "", errors.New("ai: empty answer")
	}
	var b strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		b.WriteString(p.Text)
	}
	return b.String(), nil
}
