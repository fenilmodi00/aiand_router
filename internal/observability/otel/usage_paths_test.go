package otel_test

import (
	"net/http"
)

type usageTranslator interface {
	http.ResponseWriter
	Finalize() error
}
