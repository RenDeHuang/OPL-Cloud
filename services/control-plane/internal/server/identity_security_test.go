package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPasswordStrengthRejectsShortCreatePassword(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	_, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": "weak@provisioned.example", "accountId": "acct-weak", "password": "too-short",
	})
	if !errors.Is(err, errWeakPassword) {
		t.Fatalf("short create error=%v", err)
	}
	accounts, _ := handler.app.tables.ListAccounts(context.Background(), "acct-weak")
	if len(accounts) != 0 {
		t.Fatalf("short create persisted account: %#v", accounts)
	}
}

func TestPasswordStrengthCountsUnicodeRunes(t *testing.T) {
	if err := validatePlaintextPassword(strings.Repeat("界", 11)); err == nil || err.Error() != "weak_password" {
		t.Fatalf("11-rune password error = %v", err)
	}
	if err := validatePlaintextPassword(strings.Repeat("界", 12)); err != nil {
		t.Fatalf("12-rune password error = %v", err)
	}
}

func TestNormalizedEmailCreateRejectsKnownCrossAccountConflictBeforeRemote(t *testing.T) {
	remote := newIdentityTestSub2API()
	server := newIdentityTestServer(t, remote, nil)
	handler := server.(*controlPlaneHTTPHandler)
	created, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": " Owner@Provisioned.Example ", "accountId": "acct-normalized", "password": "CorrectHorseBatteryStaple!",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created["email"] != "owner@provisioned.example" {
		t.Fatalf("created email = %q", created["email"])
	}
	_, err = handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": "OWNER@PROVISIONED.EXAMPLE", "accountId": "acct-normalized-copy", "password": "CorrectHorseBatteryStaple!",
	})
	if !errors.Is(err, errUserExists) {
		t.Fatalf("normalized duplicate error=%v", err)
	}
	if remote.resolveCalls != 1 || remote.remoteCreates != 1 {
		t.Fatalf("resolveCalls=%d remoteCreates=%d", remote.resolveCalls, remote.remoteCreates)
	}
	accounts, _ := handler.app.tables.ListAccounts(context.Background(), "acct-normalized-copy")
	if len(accounts) != 0 {
		t.Fatalf("normalized duplicate persisted account: %#v", accounts)
	}
}
