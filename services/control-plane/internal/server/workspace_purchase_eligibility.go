package server

type workspacePurchaseEligibilityMutation struct {
	AccountID  string
	Enabled    bool
	AuditEvent map[string]any
}

func workspacePurchaseEligibilityAuditMatches(existing, desired map[string]any) bool {
	if stringValue(existing["id"]) != stringValue(desired["id"]) ||
		stringValue(existing["actorUserId"]) != stringValue(desired["actorUserId"]) ||
		stringValue(existing["actorAccountId"]) != stringValue(desired["actorAccountId"]) ||
		stringValue(existing["targetAccountId"]) != stringValue(desired["targetAccountId"]) ||
		stringValue(existing["action"]) != stringValue(desired["action"]) ||
		stringValue(existing["resourceKind"]) != "account" ||
		stringValue(existing["resourceId"]) != stringValue(desired["resourceId"]) {
		return false
	}
	existingAfter, existingOK := existing["after"].(map[string]any)
	desiredAfter, desiredOK := desired["after"].(map[string]any)
	return existingOK && desiredOK &&
		boolValue(existingAfter["workspacePurchaseEnabled"]) == boolValue(desiredAfter["workspacePurchaseEnabled"]) &&
		stringValue(existingAfter["reason"]) == stringValue(desiredAfter["reason"])
}
