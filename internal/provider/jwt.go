package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type JWTPayload struct {
	Exp               int64          `json:"exp,omitempty"`
	Iat               int64          `json:"iat,omitempty"`
	Sub               string         `json:"sub,omitempty"`
	Name              string         `json:"name,omitempty"`
	PreferredUsername string         `json:"preferred_username,omitempty"`
	DisplayName       string         `json:"displayName,omitempty"`
	Provider          string         `json:"provider,omitempty"`
	PrimaryProvider   string         `json:"primary_provider,omitempty"`
	Owner             string         `json:"owner,omitempty"`
	Email             string         `json:"email,omitempty"`
	Phone             string         `json:"phone,omitempty"`
	PhoneNumber       string         `json:"phone_number,omitempty"`
	UniversalID       string         `json:"universal_id,omitempty"`
	Properties        map[string]any `json:"properties,omitempty"`
	User              *JWTUser       `json:"user,omitempty"`
}

// JWTUser 对应 MULTI_TENANCY §12.1 canonical claims 的嵌套 user Map。
// 旧 Casdoor JWT 无此字段；新 cs-user JWT 在嵌套 user Map 内携带用户基本身份。
// 字段访问走 JWTPayload 的 OrFallback 系列方法（flat first → nested fallback）。
type JWTUser struct {
	ID          string `json:"id,omitempty"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// 一组 flat first → nested fallback 的访问器。
// 旧 token（仅 flat）走第一段；新 strict canonical token（仅 nested）走第二段；
// 新 compat 模式两段都有，flat 优先以保旧兼容。
func (p *JWTPayload) emailOrFallback() string {
	if p.Email != "" {
		return p.Email
	}
	if p.User != nil {
		return p.User.Email
	}
	return ""
}

func (p *JWTPayload) nameOrFallback() string {
	if p.Name != "" {
		return p.Name
	}
	if p.User != nil {
		return p.User.DisplayName
	}
	return ""
}

func (p *JWTPayload) displayNameOrFallback() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	if p.User != nil {
		return p.User.DisplayName
	}
	return ""
}

func (p *JWTPayload) preferredUsernameOrFallback() string {
	if p.PreferredUsername != "" {
		return p.PreferredUsername
	}
	if p.User != nil {
		return p.User.Username
	}
	return ""
}

func (p *JWTPayload) phoneOrFallback() string {
	return firstNonEmptyStr(p.PhoneNumber, p.Phone, p.userPhone())
}

func (p *JWTPayload) userPhone() string {
	if p.User != nil {
		return p.User.Phone
	}
	return ""
}

func (p *JWTPayload) providerOrFallback() string {
	return firstNonEmptyStr(p.Provider, p.PrimaryProvider)
}

func (p *JWTPayload) UserID() string {
	if p.UniversalID != "" {
		return p.UniversalID
	}
	return p.Sub
}

func (p *JWTPayload) ResolveProvider() string {
	if prov := p.providerOrFallback(); prov != "" {
		return strings.ToLower(strings.TrimSpace(prov))
	}
	phone := p.phoneOrFallback()
	if phone != "" {
		return "phone"
	}
	if p.Properties != nil {
		for k := range p.Properties {
			kl := strings.ToLower(k)
			if strings.HasPrefix(kl, "oauth_github") {
				return "github"
			}
			if strings.HasPrefix(kl, "oauth_custom") {
				return "idtrust"
			}
		}
	}
	email := p.emailOrFallback()
	if email != "" && strings.Contains(email, "@") {
		return "email"
	}
	return ""
}

func (p *JWTPayload) ResolveDisplayName() string {
	provider := p.ResolveProvider()
	prefix := providerPropertyPrefix(provider)
	providerDisplayName := ""
	providerUsername := ""
	if prefix != "" && p.Properties != nil {
		if dn, ok := p.Properties[prefix+"_displayName"].(string); ok {
			providerDisplayName = strings.TrimSpace(dn)
		}
		if un, ok := p.Properties[prefix+"_username"].(string); ok {
			providerUsername = strings.TrimSpace(un)
		}
	}

	email := p.emailOrFallback()
	if email != "" && !strings.Contains(email, "@") {
		email = ""
	}

	name := p.nameOrFallback()
	displayName := firstNonEmptyStr(providerDisplayName, p.preferredUsernameOrFallback(), p.displayNameOrFallback(), name)
	username := ""

	switch provider {
	case "github":
		username = firstNonEmptyStr(providerUsername, name, usernameFromEmail(email))
	case "idtrust":
		username = firstNonEmptyStr(providerUsername)
		displayName = firstNonEmptyStr(providerDisplayName, p.displayNameOrFallback(), username)
		name = username
	case "phone":
		phone := p.phoneOrFallback()
		if phone != "" {
			username = "phone_" + phone
		}
		displayName = firstNonEmptyStr(p.displayNameOrFallback(), username)
	default:
		username = firstNonEmptyStr(providerUsername, p.preferredUsernameOrFallback(), name, usernameFromEmail(email))
	}

	if username == "" {
		username = firstNonEmptyStr(p.UniversalID, p.Sub)
		if username != "" {
			if len(username) > 12 {
				username = username[:12]
			}
			username = "user_" + strings.ToLower(username)
		} else {
			username = "user"
		}
	}
	if name == "" || provider == "idtrust" {
		name = username
	}
	if displayName == "" {
		displayName = username
	}
	return displayName
}

func usernameFromEmail(email string) string {
	if email == "" {
		return ""
	}
	idx := strings.Index(email, "@")
	if idx <= 0 {
		return ""
	}
	return strings.TrimSpace(email[:idx])
}

func providerPropertyPrefix(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return "oauth_GitHub"
	case "idtrust", "custom":
		return "oauth_Custom"
	default:
		return ""
	}
}

func ParseJWT(token string) (*JWTPayload, error) {
	parts := splitToken(token)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}
	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	var p JWTPayload
	if err := json.Unmarshal(decoded, &p); err != nil {
		return nil, fmt.Errorf("parse JWT payload: %w", err)
	}
	return &p, nil
}

func ExtractExpiryFromJWT(token string) int64 {
	p, err := ParseJWT(token)
	if err != nil || p.Exp == 0 {
		return 0
	}
	return p.Exp * 1000
}

func IsTokenValid(accessToken string, refreshToken string, expiryDate int64) bool {
	now := time.Now().UnixMilli()
	buffer := int64(30 * 60 * 1000)

	if expiryDate > 0 {
		return now < expiryDate-buffer
	}

	if refreshToken != "" {
		if p, err := ParseJWT(refreshToken); err == nil && p.Exp > 0 {
			return p.Exp*1000 > now
		}
	}

	if accessToken != "" {
		if p, err := ParseJWT(accessToken); err == nil && p.Exp > 0 {
			return now < p.Exp*1000-buffer
		}
	}

	return false
}

// IsRefreshTokenExpired reports whether the refresh token's own JWT exp is in the past.
// Returns false when the token is unparseable or carries no exp claim, so callers fall
// back to attempting a network refresh instead of preemptively refusing.
func IsRefreshTokenExpired(refreshToken string) bool {
	if refreshToken == "" {
		return false
	}
	p, err := ParseJWT(refreshToken)
	if err != nil || p.Exp == 0 {
		return false
	}
	return time.Now().UnixMilli() >= p.Exp*1000
}

func splitToken(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	if start < len(token) {
		parts = append(parts, token[start:])
	}
	return parts
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
