package client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

type ClientVersionV1 struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Endpoint string `json:"endpoint"`
}

// Client defines the interface for verifying execution.
type Client interface {
	VerifyV1(data []byte, endpoint string) ([]byte, error)
}

// httpClient is a concrete implementation of Client.
type httpClient struct {
	httpClient *http.Client
}

// NewClient returns a new instance of Client.
func NewClient() Client {
	return &httpClient{
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// VerifyV1 sends a POST request with the given data to the provided endpoint.
func (c *httpClient) VerifyV1(data []byte, endpoint string) ([]byte, error) {
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response: %v", err)
	}

	return body, nil
}
