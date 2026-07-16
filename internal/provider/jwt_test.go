package provider

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// buildToken 把 claims marshal 成一个未签名 JWT（header.payload.sig 三段，sig 用 "sig" 占位）。
// ParseJWT 只解 payload 段，不验签，所以 sig 内容无关紧要。
func buildToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "none", "typ": "JWT"}
	headerBytes, _ := json.Marshal(header)
	payloadBytes, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerBytes)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return headerB64 + "." + payloadB64 + ".sig"
}

func TestParseJWTLegacyFlatOnly(t *testing.T) {
	// 旧 Casdoor JWT：仅顶层扁平字段，无 user 嵌套 Map
	claims := map[string]any{
		"sub":                "u_legacy_001",
		"universal_id":       "u_legacy_001",
		"preferred_username": "alice_legacy",
		"name":               "Alice Legacy",
		"displayName":        "Alice Legacy Display",
		"email":              "alice@legacy.com",
		"phone":              "+86-13800000001",
		"provider":           "github",
		"exp":                int64(1893456000),
	}
	token := buildToken(t, claims)
	p, err := ParseJWT(token)
	if err != nil {
		t.Fatalf("ParseJWT failed: %v", err)
	}
	if got := p.UserID(); got != "u_legacy_001" {
		t.Errorf("UserID() = %q, want %q", got, "u_legacy_001")
	}
	if got := p.ResolveProvider(); got != "github" {
		t.Errorf("ResolveProvider() = %q, want %q", got, "github")
	}
	if got := p.emailOrFallback(); got != "alice@legacy.com" {
		t.Errorf("emailOrFallback() = %q, want %q", got, "alice@legacy.com")
	}
	if got := p.preferredUsernameOrFallback(); got != "alice_legacy" {
		t.Errorf("preferredUsernameOrFallback() = %q, want %q", got, "alice_legacy")
	}
	if got := p.phoneOrFallback(); got != "+86-13800000001" {
		t.Errorf("phoneOrFallback() = %q, want %q", got, "+86-13800000001")
	}
	if got := p.displayNameOrFallback(); got != "Alice Legacy Display" {
		t.Errorf("displayNameOrFallback() = %q, want %q", got, "Alice Legacy Display")
	}
}

func TestParseJWTNewNestedOnly(t *testing.T) {
	// 新 cs-user strict canonical JWT：仅嵌套 user Map + primary_provider，无扁平字段
	claims := map[string]any{
		"sub":              "u_canonical_002",
		"universal_id":     "u_canonical_002",
		"primary_provider": "idtrust",
		"exp":              int64(1893456000),
		"user": map[string]any{
			"id":           "u_canonical_002",
			"username":     "bob_canonical",
			"display_name": "Bob Canonical",
			"email":        "bob@canonical.com",
			"phone":        "+86-13900000002",
			"avatar_url":   "https://avatars.example.com/bob.png",
		},
	}
	token := buildToken(t, claims)
	p, err := ParseJWT(token)
	if err != nil {
		t.Fatalf("ParseJWT failed: %v", err)
	}
	if got := p.UserID(); got != "u_canonical_002" {
		t.Errorf("UserID() = %q, want %q", got, "u_canonical_002")
	}
	if got := p.ResolveProvider(); got != "idtrust" {
		t.Errorf("ResolveProvider() = %q, want %q", got, "idtrust")
	}
	if got := p.emailOrFallback(); got != "bob@canonical.com" {
		t.Errorf("emailOrFallback() = %q, want %q", got, "bob@canonical.com")
	}
	if got := p.preferredUsernameOrFallback(); got != "bob_canonical" {
		t.Errorf("preferredUsernameOrFallback() = %q, want %q", got, "bob_canonical")
	}
	if got := p.phoneOrFallback(); got != "+86-13900000002" {
		t.Errorf("phoneOrFallback() = %q, want %q", got, "+86-13900000002")
	}
	if got := p.displayNameOrFallback(); got != "Bob Canonical" {
		t.Errorf("displayNameOrFallback() = %q, want %q", got, "Bob Canonical")
	}
}

func TestParseJWTCompatSameValue(t *testing.T) {
	// 新 compat 模式：flat + nested 都填，值相同
	claims := map[string]any{
		"sub":                "u_compat_003",
		"universal_id":       "u_compat_003",
		"preferred_username": "charlie_compat",
		"name":               "Charlie Compat",
		"displayName":        "Charlie Compat Display",
		"email":              "charlie@compat.com",
		"phone":              "+86-13700000003",
		"provider":           "github",
		"primary_provider":   "github",
		"exp":                int64(1893456000),
		"user": map[string]any{
			"id":           "u_compat_003",
			"username":     "charlie_compat",
			"display_name": "Charlie Compat Display",
			"email":        "charlie@compat.com",
			"phone":        "+86-13700000003",
		},
	}
	token := buildToken(t, claims)
	p, err := ParseJWT(token)
	if err != nil {
		t.Fatalf("ParseJWT failed: %v", err)
	}
	if got := p.UserID(); got != "u_compat_003" {
		t.Errorf("UserID() = %q, want %q", got, "u_compat_003")
	}
	if got := p.ResolveProvider(); got != "github" {
		t.Errorf("ResolveProvider() = %q, want %q", got, "github")
	}
	if got := p.emailOrFallback(); got != "charlie@compat.com" {
		t.Errorf("emailOrFallback() = %q, want %q", got, "charlie@compat.com")
	}
}

func TestParseJWTCompatFlatPriority(t *testing.T) {
	// 新 compat 模式但 flat 与 nested 值不同 → 验证 flat 优先（保旧兼容）
	claims := map[string]any{
		"sub":                "u_compat_004",
		"universal_id":       "u_compat_004",
		"preferred_username": "flat_username",
		"name":               "Flat Name",
		"displayName":        "Flat Display",
		"email":              "flat@example.com",
		"phone":              "+86-13600000004",
		"provider":           "github",
		"primary_provider":   "idtrust",
		"exp":                int64(1893456000),
		"user": map[string]any{
			"id":           "u_nested_different",
			"username":     "nested_username",
			"display_name": "Nested Display",
			"email":        "nested@example.com",
			"phone":        "+86-13500000005",
		},
	}
	token := buildToken(t, claims)
	p, err := ParseJWT(token)
	if err != nil {
		t.Fatalf("ParseJWT failed: %v", err)
	}
	// flat 优先
	if got := p.emailOrFallback(); got != "flat@example.com" {
		t.Errorf("emailOrFallback() = %q, want flat value %q (flat-first policy)", got, "flat@example.com")
	}
	if got := p.preferredUsernameOrFallback(); got != "flat_username" {
		t.Errorf("preferredUsernameOrFallback() = %q, want flat value %q", got, "flat_username")
	}
	if got := p.phoneOrFallback(); got != "+86-13600000004" {
		t.Errorf("phoneOrFallback() = %q, want flat value %q", got, "+86-13600000004")
	}
	if got := p.displayNameOrFallback(); got != "Flat Display" {
		t.Errorf("displayNameOrFallback() = %q, want flat value %q", got, "Flat Display")
	}
	// ResolveProvider 也应优先 flat 的 "github"，不是 nested 的 "idtrust"
	if got := p.ResolveProvider(); got != "github" {
		t.Errorf("ResolveProvider() = %q, want flat value %q (flat-first policy)", got, "github")
	}
}

func TestParseJWTPartialFallback(t *testing.T) {
	// 混合：部分字段仅 flat，部分仅 nested（如新 token 缺少 flat preferred_username 但有 user.username）
	claims := map[string]any{
		"sub":            "u_mixed_005",
		"universal_id":   "u_mixed_005",
		"email":          "mixed@example.com",
		"provider":       "email",
		"primary_provider": "email",
		"exp":            int64(1893456000),
		"user": map[string]any{
			"id":           "u_mixed_005",
			"username":     "nested_only_username",
			"display_name": "Nested Only Display",
		},
	}
	token := buildToken(t, claims)
	p, err := ParseJWT(token)
	if err != nil {
		t.Fatalf("ParseJWT failed: %v", err)
	}
	// email 走 flat
	if got := p.emailOrFallback(); got != "mixed@example.com" {
		t.Errorf("emailOrFallback() = %q, want flat value", got)
	}
	// username 走 nested（flat 缺失）
	if got := p.preferredUsernameOrFallback(); got != "nested_only_username" {
		t.Errorf("preferredUsernameOrFallback() = %q, want nested fallback %q", got, "nested_only_username")
	}
	// display_name 走 nested（flat 缺失）
	if got := p.displayNameOrFallback(); got != "Nested Only Display" {
		t.Errorf("displayNameOrFallback() = %q, want nested fallback %q", got, "Nested Only Display")
	}
}
