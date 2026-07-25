package server

import (
	"context"
	"errors"
	"testing"
)

func TestPasswordValidationDelegatesNonEmptyPasswordToSub2API(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	created, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": "short@provisioned.example", "accountId": "acct-short", "password": "x",
	})
	if err != nil || created["email"] != "short@provisioned.example" {
		t.Fatalf("short non-empty password was not delegated: created=%#v err=%v", created, err)
	}
	accounts, _ := handler.app.tables.ListAccounts(context.Background(), "acct-short")
	if len(accounts) != 1 {
		t.Fatalf("delegated create accounts=%#v", accounts)
	}
}

func TestPasswordValidationRejectsOnlyEmptyInputLocally(t *testing.T) {
	if err := validatePlaintextPassword(""); !errors.Is(err, errMissingPassword) {
		t.Fatalf("empty password error = %v", err)
	}
	if err := validatePlaintextPassword("界"); err != nil {
		t.Fatalf("non-empty password error = %v", err)
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
