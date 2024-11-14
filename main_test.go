package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateClient(t *testing.T) {
	// Test with invalid URL
	cfg := ElasticConfig{URL: "://invalid-url", User: "user", Pass: "pass"}
	client, err := CreateClient(cfg, false, false)
	assert.Error(t, err, "Expected error for invalid URL")

	// Test with valid URL but incorrect credentials
	cfg.URL = "http://localhost:9200"
	cfg.User = "wronguser"
	cfg.Pass = "wrongpass"
	client, err = CreateClient(cfg, false, false)
	if err != nil {
		t.Skip("Elasticsearch not available on localhost:9200, skipping")
	} else {
		client.Ping(cfg.URL).Do(context.Background())
		assert.Error(t, err, "Expected authentication error with wrong credentials")
	}
}
