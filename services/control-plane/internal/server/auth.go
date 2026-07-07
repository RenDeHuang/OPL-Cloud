package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	passwordHashPrefix = "pbkdf2_sha256"
	passwordIterations = 120000
	sessionCookieName  = "opl_session"
)

type sessionRecord struct {
	ID        string
	UserID    string
	CSRF      string
	ExpiresAt time.Time
}

func hashPassword(password string) (string, error) {
	salt, err := randomToken(18)
	if err != nil {
		return "", err
	}
	hash := pbkdf2SHA256([]byte(password), []byte(salt), passwordIterations, 32)
	return strings.Join([]string{passwordHashPrefix, strconv.Itoa(passwordIterations), salt, base64.RawStdEncoding.EncodeToString(hash)}, "$"), nil
}

func verifyPassword(password string, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordHashPrefix {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), []byte(parts[2]), iterations, len(want))
	return hmac.Equal(got, want)
}

func pbkdf2SHA256(password []byte, salt []byte, iterations int, keyLen int) []byte {
	hashLen := sha256.Size
	blocks := (keyLen + hashLen - 1) / hashLen
	output := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		output = append(output, t...)
	}
	return output[:keyLen]
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

func sanitizeUser(user map[string]any) map[string]any {
	output := cloneMap(user)
	delete(output, "password")
	delete(output, "passwordHash")
	return output
}

func bootstrapUsersFromEnv() ([]map[string]any, error) {
	raw := strings.TrimSpace(os.Getenv("OPL_CONSOLE_USERS_JSON"))
	if raw == "" {
		return nil, nil
	}
	var users []map[string]any
	if err := json.Unmarshal([]byte(raw), &users); err != nil {
		return nil, err
	}
	for _, user := range users {
		if stringValue(user["id"]) == "" {
			user["id"] = "usr-" + compactID(firstNonEmpty(stringValue(user["email"]), time.Now().UTC().String()))
		}
		if stringValue(user["status"]) == "" {
			user["status"] = "active"
		}
		if stringValue(user["role"]) == "" {
			user["role"] = "pi"
		}
		if stringValue(user["passwordHash"]) == "" {
			password := stringValue(user["password"])
			if password == "" {
				return nil, errors.New("bootstrap_user_missing_password")
			}
			hash, err := hashPassword(password)
			if err != nil {
				return nil, err
			}
			user["passwordHash"] = hash
		}
		delete(user, "password")
	}
	return users, nil
}
