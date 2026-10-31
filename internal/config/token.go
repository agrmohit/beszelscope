package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// usersCollectionID is PocketBase's ID for the users collection. Superuser
// tokens bypass Beszel's access rules, so only users tokens are accepted.
const usersCollectionID = "_pb_users_auth_"

type claims struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	CollectionID string  `json:"collectionId"`
	Exp          float64 `json:"exp"`
}

func (c claims) expiry() time.Time {
	return time.Unix(int64(c.Exp), 0).UTC()
}

// TokenInfo is what a token says about itself.
type TokenInfo struct {
	UserID    string
	ExpiresAt time.Time // an expired token still parses
}

// ParseToken runs the same checks on tok as [Parse].
func ParseToken(tok string) (TokenInfo, error) {
	c, err := parseToken(tok)
	if err != nil {
		return TokenInfo{}, err
	}
	return TokenInfo{UserID: c.ID, ExpiresAt: c.expiry()}, nil
}

// parseToken checks the token's shape and claims. Only the hub can verify
// the signature.
func parseToken(tok string) (claims, error) {
	if i := strings.IndexFunc(tok, func(r rune) bool { return !isTokenRune(r) }); i >= 0 {
		return claims{}, fmt.Errorf("token has a character that is not base64url or '.' at offset %d "+
			"(whitespace or quotes copied with it?)", i)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		err := fmt.Errorf("token has %d part(s), want 3 (header.payload.signature); it is %d chars long",
			len(parts), len(tok))
		// The `security -w` prompt silently keeps only 128 characters.
		if len(tok) == 128 {
			err = fmt.Errorf("%w: looks truncated by the macOS `security -w` prompt, which keeps 128 chars; "+
				"store it with `pbpaste | beszelscope token set` instead", err)
		}
		return claims{}, err
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, fmt.Errorf("token payload is not base64url: %w", err)
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return claims{}, fmt.Errorf("token payload is not JSON: %w", err)
	}
	switch {
	case c.Type != "auth":
		return claims{}, fmt.Errorf("token type %q: want an auth token", c.Type)
	case c.CollectionID != usersCollectionID:
		return claims{}, fmt.Errorf("token is for collection %q, not users (%s); a superuser token bypasses "+
			"access rules: mint one for the readonly user instead", c.CollectionID, usersCollectionID)
	case c.ID == "":
		return claims{}, errors.New("token has no id claim")
	case c.Exp <= 0:
		return claims{}, errors.New("token has no exp claim")
	}
	return c, nil
}

func isTokenRune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
		r == '-' || r == '_' || r == '.'
}
