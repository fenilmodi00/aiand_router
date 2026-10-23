package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/requestcontext"
)

func TestServedCredentialLogValue(t *testing.T) {
	service := &Service{}

	source := service.servedCredentialLogValue(context.Background())
	assert.Empty(t, source, "deployment key")

	byok := requestcontext.WithCredentials(context.Background(), &Credentials{APIKey: []byte("sk-byok-0123456789abcdef"), Source: credSourceBYOK})
	source = service.servedCredentialLogValue(byok)
	assert.Equal(t, credSourceBYOK, source)
}
