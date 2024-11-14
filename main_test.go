package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateClientInvalidURL(t *testing.T) {
	cfg := ElasticConfig{URL: "://invalid-url", User: "user", Pass: "pass"}
	_, err := CreateClient(cfg, false, false)
	assert.Error(t, err, "Expected error for invalid URL")
}
