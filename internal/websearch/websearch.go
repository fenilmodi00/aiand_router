// Package websearch models Anthropic's native web-search server tool for
// upstreams that cannot execute it themselves. Pure: detection only, no I/O.
package websearch

// Query is a single web search.
type Query struct {
	// Text is the search query as the client asked for it.
	Text string
}
