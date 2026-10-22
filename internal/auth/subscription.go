package auth

// SubscriptionOwner identifies a verified requester and installation. SubscriberID
// is the stable user owner; APIKeyID records the key the request arrived on.
type SubscriptionOwner struct {
	SubscriberID   string
	APIKeyID       string
	InstallationID string
}

// Valid reports whether this owner identifies anyone.
func (o SubscriptionOwner) Valid() bool {
	return o.SubscriberID != "" || o.APIKeyID != "" || o.InstallationID != ""
}

// SubscriptionOwnerForKey derives linked-account ownership from an
// authenticated key: its credential subject where one exists, plus the key
// itself for rows enrolled before ownership moved to the subscriber.
func SubscriptionOwnerForKey(key *APIKey) SubscriptionOwner {
	if key == nil {
		return SubscriptionOwner{}
	}
	return SubscriptionOwner{SubscriberID: key.CredentialSubjectID, APIKeyID: key.ID, InstallationID: key.InstallationID}
}
