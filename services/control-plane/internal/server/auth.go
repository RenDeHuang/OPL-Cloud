package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
)

const (
	sessionCookieName   = "opl_session"
	sessionLookupPrefix = "sub2api-sha256:"
)

func validatePlaintextPassword(password string) error {
	if password == "" {
		return errMissingPassword
	}
	return nil
}

func randomToken(bytesLen int) (string, error) {
	data := make([]byte, bytesLen)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func sessionCookie(sessionID string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookieName, Value: sessionID, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func sessionLookupKey(sessionID string) string {
	sum := sha256.Sum256([]byte("opl-session\x00" + sessionID))
	return sessionLookupPrefix + hex.EncodeToString(sum[:])
}

func validSessionLookupKey(id string) bool { return strings.HasPrefix(id, sessionLookupPrefix) }

func sanitizeUser(user map[string]any) map[string]any {
	output := cloneMap(user)
	delete(output, "password")
	delete(output, "passwordHash")
	return output
}

func bootstrapUsersFromEnv() ([]map[string]any, error) {
	if os.Getenv("OPL_CONSOLE_USERS_JSON") == "" {
		return nil, nil
	}
	return nil, errors.New("OPL_CONSOLE_USERS_JSON is retired")
}

func validRole(role string) bool {
	return role == "owner" || role == "admin"
}
