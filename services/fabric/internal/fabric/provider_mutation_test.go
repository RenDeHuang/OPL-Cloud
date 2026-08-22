package fabric

import (
	"encoding/json"
	"testing"
)

type providerMutationRoundTripState struct {
	SchemaVersion int    `json:"schemaVersion"`
	Zeta          string `json:"zeta"`
	Alpha         string `json:"alpha"`
	Zero          int    `json:"zero"`
}

func providerMutationStateOperation(t *testing.T, state providerMutationRoundTripState) FabricOperation {
	t.Helper()
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return FabricOperation{RedactedProviderPayload: map[string]any{
		providerMutationStatePayloadKey: persistedProviderMutationState{Value: body, Digest: hashInput(json.RawMessage(body))},
	}}
}

func providerMutationStateJSONRoundTrip(t *testing.T, operation FabricOperation) FabricOperation {
	t.Helper()
	body, err := json.Marshal(operation.RedactedProviderPayload)
	if err != nil {
		t.Fatal(err)
	}
	operation.RedactedProviderPayload = nil
	if err := json.Unmarshal(body, &operation.RedactedProviderPayload); err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestProviderMutationStateAllowsPersistedJSONKeyReorderingOnly(t *testing.T) {
	state := providerMutationRoundTripState{SchemaVersion: 1, Zeta: "last", Alpha: "first", Zero: 0}
	expected := providerMutationStateOperation(t, state)
	persisted := providerMutationStateJSONRoundTrip(t, expected)

	envelope, ok := decodePersistedProviderMutationState(persisted.RedactedProviderPayload[providerMutationStatePayloadKey])
	if !ok || envelope.Digest == hashInput(envelope.Value) {
		t.Fatalf("test did not reproduce JSON key-order digest drift: envelope=%#v ok=%v", envelope, ok)
	}
	var decoded providerMutationRoundTripState
	if !decodeProviderMutationState(persisted, &decoded) || decoded != state {
		t.Fatalf("decoded=%#v want=%#v", decoded, state)
	}
	if !sameProviderMutationState(persisted, expected) {
		t.Fatal("persisted and typed provider mutation states differ")
	}
}

func TestProviderMutationStateRoundTripStillFailsClosed(t *testing.T) {
	state := providerMutationRoundTripState{SchemaVersion: 1, Zeta: "last", Alpha: "first", Zero: 0}
	expected := providerMutationStateOperation(t, state)

	tests := []struct {
		name       string
		value      string
		rehash     bool
		wantDecode bool
	}{
		{name: "digest mismatch", value: `{"schemaVersion":1,"zeta":"last","alpha":"first","zero":0}`},
		{name: "field value drift", value: `{"schemaVersion":1,"zeta":"changed","alpha":"first","zero":0}`, rehash: true, wantDecode: true},
		{name: "missing field", value: `{"schemaVersion":1,"zeta":"last","alpha":"first"}`, rehash: true},
		{name: "unknown field", value: `{"schemaVersion":1,"zeta":"last","alpha":"first","zero":0,"unknown":true}`, rehash: true},
		{name: "field type drift", value: `{"schemaVersion":1,"zeta":"last","alpha":"first","zero":"0"}`, rehash: true},
		{name: "malformed", value: `{"schemaVersion":`, rehash: true},
		{name: "trailing document", value: `{"schemaVersion":1,"zeta":"last","alpha":"first","zero":0} {}`, rehash: true},
		{name: "array", value: `[]`, rehash: true},
		{name: "null", value: `null`, rehash: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := json.RawMessage(test.value)
			digest := "invalid-digest"
			if test.rehash {
				digest = hashInput(body)
			}
			operation := FabricOperation{RedactedProviderPayload: map[string]any{
				providerMutationStatePayloadKey: persistedProviderMutationState{Value: body, Digest: digest},
			}}
			decoded := providerMutationRoundTripState{SchemaVersion: 9, Zeta: "sentinel", Alpha: "sentinel", Zero: 9}
			before := decoded
			if decodedOK := decodeProviderMutationState(operation, &decoded); decodedOK != test.wantDecode {
				t.Fatalf("decoded=%v state=%#v want=%v", decodedOK, decoded, test.wantDecode)
			}
			if !test.wantDecode && decoded != before {
				t.Fatalf("failed decode mutated target: before=%#v after=%#v", before, decoded)
			}
			if sameProviderMutationState(operation, expected) {
				t.Fatal("invalid state matched expected state")
			}
		})
	}

	t.Run("unknown envelope field", func(t *testing.T) {
		body, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		operation := FabricOperation{RedactedProviderPayload: map[string]any{
			providerMutationStatePayloadKey: map[string]any{
				"value": json.RawMessage(body), "digest": hashInput(json.RawMessage(body)), "unknown": true,
			},
		}}
		var decoded providerMutationRoundTripState
		if decodeProviderMutationState(operation, &decoded) || sameProviderMutationState(operation, expected) {
			t.Fatal("unknown provider mutation state envelope field was accepted")
		}
	})
}
