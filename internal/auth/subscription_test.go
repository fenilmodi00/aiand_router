package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionOwnerForKeyPrefersCredentialSubject(t *testing.T) {
	subscriberOwner := SubscriptionOwnerForKey(&APIKey{ID: "key-1", CredentialSubjectID: "subscriber-1"})
	require.Equal(t, SubscriptionOwner{SubscriberID: "subscriber-1", APIKeyID: "key-1"}, subscriberOwner)

	sibling := SubscriptionOwnerForKey(&APIKey{ID: "key-2", CredentialSubjectID: "subscriber-1"})
	require.Equal(t, subscriberOwner.SubscriberID, sibling.SubscriberID)
	legacyOwner := SubscriptionOwnerForKey(&APIKey{ID: "key-3"})
	require.Empty(t, legacyOwner.SubscriberID)
	require.Equal(t, "key-3", legacyOwner.APIKeyID)
	require.True(t, legacyOwner.Valid())
	require.False(t, SubscriptionOwnerForKey(nil).Valid())
}
