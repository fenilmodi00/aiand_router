package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServedOnBYOK_KeysOffCredentialSource(t *testing.T) {
	assert.True(t, servedOnBYOK(ctxWithCreds(&Credentials{Source: credSourceBYOK})),
		"a turn dispatched on a BYOK credential bills at the fee rate")
	assert.False(t, servedOnBYOK(ctxWithCreds(&Credentials{Source: credSourceClient})),
		"a client-credential turn is not fee-billed")
	assert.False(t, servedOnBYOK(context.Background()),
		"a deployment-key turn bills full cost")
}
