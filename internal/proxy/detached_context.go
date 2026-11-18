package proxy

import "context"

// requestValuesHidden keeps ctx's cancellation and deadline but none of its
// values.
type requestValuesHidden struct{ context.Context }

func (requestValuesHidden) Value(any) any { return nil }

// withoutRequestValues detaches a background call from the served request's
// tenant-scoped values (credentials, identity, aliases) while still deriving
// cancellation from the request. Use it for work that must not inherit the
// turn's authority, such as the escalation judge and the policy shadow route.
func withoutRequestValues(ctx context.Context) context.Context {
	return requestValuesHidden{ctx}
}
