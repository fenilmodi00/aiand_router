package requestcontext

import (
	"net/http"

	"aiand-os/router/internal/auth"

	"github.com/google/uuid"
)

type RequestFacts struct {
	RequestID      string
	APIKeyID       string
	ExternalID     string
	InstallationID uuid.UUID
	Client         ClientIdentity
	Headers        http.Header
	ExternalKeys   []*auth.ExternalAPIKey
}

func (f RequestFacts) RouterKeyed() bool {
	return f.InstallationID != uuid.Nil
}
