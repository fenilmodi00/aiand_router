package catalog

// CustomToolInput marks whether a model can call a Responses custom tool
// projected to a Chat function with one required string "input" argument.
// CustomToolInputUnknown (zero value) = no restriction. CustomToolInputUnsupported
// flags models observed returning arguments the projection cannot unwrap;
// the proxy excludes them from requests that declare custom tools.
type CustomToolInput int

const (
	CustomToolInputUnknown CustomToolInput = iota
	CustomToolInputUnsupported
)
