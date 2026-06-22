package tunnel

import (
	"fmt"
	"math"
	"testing"
	"time"

	"cs-cloud/internal/device"
)

// ── backoff() ──────────────────────────────────────────────────────────

func TestBackoff_NeverReturnsZeroOrNegative(t *testing.T) {
	// 验证任何 attempt 值都不会返回 <= 0 的延迟
	for a := -10; a <= 100; a++ {
		d := backoff(a)
		if d <= 0 {
			t.Fatalf("backoff(%d) = %v, expected positive duration", a, d)
		}
	}
	// 超大值
	for a := 1000; a <= 1100; a++ {
		d := backoff(a)
		if d <= 0 {
			t.Fatalf("backoff(%d) = %v, expected positive duration (large)", a, d)
		}
	}
	// int max 边界
	d := backoff(math.MaxInt)
	if d <= 0 {
		t.Fatalf("backoff(MaxInt) = %v, expected positive duration", d)
	}
}

func TestBackoff_ExponentialProgression(t *testing.T) {
	// 验证前几次是标准的指数增长
	expected := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
	}
	for a, want := range expected {
		got := backoff(a)
		// 允许 jitter 偏差: ±25%
		min, max := want*3/4, want*5/4
		if got < min || got > max {
			t.Errorf("backoff(%d) = %v, expected ≈ %v (range [%v, %v])", a, got, want, min, max)
		}
	}
}

func TestBackoff_SaturatesAtMaxDelay(t *testing.T) {
	// attempt >= 6 时饱和在 maxDelay, jitter ±25% 使范围扩展到 [45s, 75s]
	maxMin := maxDelay * 3 / 4 // 45s (75% of 60s — jitter floor)
	maxMax := maxDelay * 5 / 4 // 75s (125% of 60s — jitter ceiling)
	for a := 6; a <= 100; a++ {
		d := backoff(a)
		if d < maxMin {
			t.Errorf("backoff(%d) = %v, expected >= %v (saturated near maxDelay)", a, d, maxMin)
		}
		if d > maxMax {
			t.Errorf("backoff(%d) = %v, expected <= %v (jitter ceiling)", a, d, maxMax)
		}
	}
}

func TestBackoff_NegativeAttempt(t *testing.T) {
	d := backoff(-1)
	if d <= 0 || d > maxDelay*5/4 {
		t.Fatalf("backoff(-1) = %v, expected positive <= %v", d, maxDelay*5/4)
	}
}

func TestBackoff_JitterRange(t *testing.T) {
	// 多次调用验证 jitter 在 ±25% 范围内
	base := 1 * time.Second
	for i := 0; i < 50; i++ {
		d := backoff(0)
		min := base * 3 / 4 // 0.75s
		max := base * 5 / 4 // 1.25s
		if d < min || d > max {
			t.Errorf("backoff(0) iteration %d = %v, expected range [%v, %v]", i, d, min, max)
		}
	}
}

// ── rateLimitBackoff() ──────────────────────────────────────────────────

func TestRateLimitBackoff_WithRetryAfterSeconds(t *testing.T) {
	err := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
		RetryAfter: "120",
	}
	d := rateLimitBackoff(err, 0)
	// Retry-After=120s + 1s buffer = 121s
	// 允许 jitter: ±25% of 121s
	want := 121 * time.Second
	min, max := want*3/4, want*5/4
	if d < min || d > max {
		t.Errorf("rateLimitBackoff with Retry-After=120 = %v, expected ≈ %v (range [%v, %v])", d, want, min, max)
	}
}

func TestRateLimitBackoff_WithRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(90 * time.Second)
	err := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
		RetryAfter: future.Format(time.RFC1123),
	}
	d := rateLimitBackoff(err, 0)
	// 期望 ≈ 90s + 1s buffer = 91s
	want := 91 * time.Second
	min, max := want*3/4, want*5/4
	if d < min || d > max {
		t.Errorf("rateLimitBackoff with Retry-After=HTTP-date = %v, expected ≈ %v (range [%v, %v])", d, want, min, max)
	}
}

func TestRateLimitBackoff_WithoutRetryAfter(t *testing.T) {
	err := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
		RetryAfter: "", // 无 Retry-After
	}
	// attempt=0: 30s * 1 = 30s
	d := rateLimitBackoff(err, 0)
	want := 30 * time.Second
	min, max := want*3/4, want*5/4
	if d < min || d > max {
		t.Errorf("rateLimitBackoff(attempt=0, no Retry-After) = %v, expected ≈ %v (range [%v, %v])", d, want, min, max)
	}
}

func TestRateLimitBackoff_ExponentialWithoutRetryAfter(t *testing.T) {
	err := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
	}
	expected := []time.Duration{
		30 * time.Second,  // attempt=0: 30s
		60 * time.Second,  // attempt=1: 60s
		120 * time.Second, // attempt=2: 120s
		240 * time.Second, // attempt=3: 240s (4min)
		300 * time.Second, // attempt=4: saturated at 5min
		300 * time.Second, // attempt=5: saturated
	}
	for a, want := range expected {
		got := rateLimitBackoff(err, a)
		min, max := want*3/4, want*5/4
		if got < min || got > max {
			t.Errorf("rateLimitBackoff(attempt=%d) = %v, expected ≈ %v (range [%v, %v])", a, got, want, min, max)
		}
	}
}

func TestRateLimitBackoff_SaturatesAtRateLimitMaxDelay(t *testing.T) {
	err := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
	}
	maxMin := rateLimitMaxDelay * 3 / 4 // 75% of 5min
	maxMax := rateLimitMaxDelay * 5 / 4 // 125% of 5min — jitter ceiling
	for a := 5; a <= 100; a++ {
		d := rateLimitBackoff(err, a)
		if d < maxMin {
			t.Errorf("rateLimitBackoff(attempt=%d) = %v, expected >= %v", a, d, maxMin)
		}
		if d > maxMax {
			t.Errorf("rateLimitBackoff(attempt=%d) = %v, expected <= %v", a, d, maxMax)
		}
	}
}

func TestRateLimitBackoff_FallbackToBackoff(t *testing.T) {
	// 非 GatewayAssignError 的错误应该回退到通用 backoff
	plainErr := fmt.Errorf("some other error")
	d := rateLimitBackoff(plainErr, 0)
	want := 1 * time.Second
	min, max := want*3/4, want*5/4
	if d < min || d > max {
		t.Errorf("rateLimitBackoff(plainErr, 0) = %v, expected ≈ %v (range [%v, %v])", d, want, min, max)
	}
}

func TestRateLimitBackoff_WrappedError(t *testing.T) {
	// 验证 errors.As 能穿透 fmt.Errorf("%w") 包装
	inner := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
	}
	wrapped := fmt.Errorf("upstream: %w", inner)
	d := rateLimitBackoff(wrapped, 0)
	want := 30 * time.Second
	min, max := want*3/4, want*5/4
	if d < min || d > max {
		t.Errorf("rateLimitBackoff(wrapped, 0) = %v, expected ≈ %v", d, want)
	}
}

func TestRateLimitBackoff_RetryAfterExceedsMax(t *testing.T) {
	err := &device.GatewayAssignError{
		StatusCode: 429,
		Message:    "too many requests",
		RetryAfter: "600", // 10 min > rateLimitMaxDelay (5 min)
	}
	d := rateLimitBackoff(err, 0)
	maxMin := rateLimitMaxDelay * 3 / 4
	maxMax := rateLimitMaxDelay * 5 / 4
	if d < maxMin || d > maxMax {
		t.Errorf("rateLimitBackoff with Retry-After=600 = %v, expected saturated ≈ %v (range [%v, %v])", d, rateLimitMaxDelay, maxMin, maxMax)
	}
}

// ── applyJitter() ─────────────────────────────────────────────────────

func TestApplyJitter_Range(t *testing.T) {
	tests := []time.Duration{
		time.Second,
		30 * time.Second,
		time.Minute,
		5 * time.Minute,
	}
	for _, base := range tests {
		for i := 0; i < 100; i++ {
			got := applyJitter(base)
			min := base * 3 / 4
			max := base * 5 / 4
			if got < min || got > max {
				t.Errorf("applyJitter(%v) iteration %d = %v, expected range [%v, %v]", base, i, got, min, max)
			}
		}
	}
}

func TestApplyJitter_ZeroOrNegative(t *testing.T) {
	if d := applyJitter(0); d != 0 {
		t.Errorf("applyJitter(0) = %v, expected 0", d)
	}
	if d := applyJitter(-time.Second); d != 0 {
		t.Errorf("applyJitter(-1s) = %v, expected 0", d)
	}
}

func TestApplyJitter_DeterministicRange(t *testing.T) {
	// 统计 jitter 分布：应该大致均匀分布在 ±25% 以内
	base := 10 * time.Second
	min := base * 3 / 4 // 7.5s
	max := base * 5 / 4 // 12.5s
	buckets := make(map[time.Duration]int)
	for i := 0; i < 1000; i++ {
		d := applyJitter(base)
		if d < min || d > max {
			t.Fatalf("applyJitter(%v) = %v, out of range [%v, %v]", base, d, min, max)
		}
		buckets[d]++
	}
	if len(buckets) < 2 {
		t.Error("applyJitter appears deterministic (only 1 unique value in 1000 iterations)")
	}
}

// ── Integration: IsGatewayAssignRateLimitError ─────────────────────────

func TestIsGatewayAssignRateLimitError_Structured(t *testing.T) {
	err := &device.GatewayAssignError{StatusCode: 429, Message: "limit"}
	if !device.IsGatewayAssignRateLimitError(err) {
		t.Error("IsGatewayAssignRateLimitError should return true for 429 GatewayAssignError")
	}
}

func TestIsGatewayAssignRateLimitError_StringFallback(t *testing.T) {
	err := fmt.Errorf("gateway-assign failed: 429 too many requests")
	if !device.IsGatewayAssignRateLimitError(err) {
		t.Error("IsGatewayAssignRateLimitError should match string 'gateway-assign failed: 429'")
	}
}

func TestIsGatewayAssignRateLimitError_OtherCode(t *testing.T) {
	err := &device.GatewayAssignError{StatusCode: 503, Message: "unavailable"}
	if device.IsGatewayAssignRateLimitError(err) {
		t.Error("IsGatewayAssignRateLimitError should return false for 503")
	}
}

func TestIsGatewayAssignRateLimitError_Nil(t *testing.T) {
	if device.IsGatewayAssignRateLimitError(nil) {
		t.Error("IsGatewayAssignRateLimitError(nil) should return false")
	}
}

// ── Integration: IsGatewayAssignAuthError ──────────────────────────────

func TestIsGatewayAssignAuthError_401Structured(t *testing.T) {
	err := &device.GatewayAssignError{StatusCode: 401, Message: "unauthorized"}
	if !device.IsGatewayAssignAuthError(err) {
		t.Error("IsGatewayAssignAuthError should return true for 401 GatewayAssignError")
	}
}

func TestIsGatewayAssignAuthError_403Structured(t *testing.T) {
	err := &device.GatewayAssignError{StatusCode: 403, Message: "forbidden"}
	if !device.IsGatewayAssignAuthError(err) {
		t.Error("IsGatewayAssignAuthError should return true for 403 GatewayAssignError")
	}
}

func TestIsGatewayAssignAuthError_429NotAuth(t *testing.T) {
	err := &device.GatewayAssignError{StatusCode: 429, Message: "limit"}
	if device.IsGatewayAssignAuthError(err) {
		t.Error("IsGatewayAssignAuthError should return false for 429")
	}
}

func TestIsGatewayAssignAuthError_StringFallback(t *testing.T) {
	err401 := fmt.Errorf("gateway-assign failed: 401 invalid token")
	if !device.IsGatewayAssignAuthError(err401) {
		t.Error("IsGatewayAssignAuthError should match string 'gateway-assign failed: 401'")
	}
	err403 := fmt.Errorf("gateway-assign failed: 403 forbidden")
	if !device.IsGatewayAssignAuthError(err403) {
		t.Error("IsGatewayAssignAuthError should match string 'gateway-assign failed: 403'")
	}
}

func TestIsGatewayAssignAuthError_NoFalseMatchOn429Body(t *testing.T) {
	// 429 的响应体中如果包含 "401" 不应该被误判为 auth error
	err := fmt.Errorf("gateway-assign failed: 429 rate limit error, see doc 401.html")
	if device.IsGatewayAssignAuthError(err) {
		t.Error("IsGatewayAssignAuthError should NOT match 429 even if body contains 401")
	}
}

func TestIsGatewayAssignAuthError_Nil(t *testing.T) {
	if device.IsGatewayAssignAuthError(nil) {
		t.Error("IsGatewayAssignAuthError(nil) should return false")
	}
}

// ── Wrapped error passthrough ──────────────────────────────────────────

func TestErrorsAsPassthrough(t *testing.T) {
	// 验证 errors.As 能穿透多层 fmt.Errorf("%w") 找到 GatewayAssignError
	inner := &device.GatewayAssignError{StatusCode: 429, Message: "limit"}
	wrapped := fmt.Errorf("wrap1: %w", fmt.Errorf("wrap2: %w", inner))

	if !device.IsGatewayAssignRateLimitError(wrapped) {
		t.Error("IsGatewayAssignRateLimitError should unwrap nested errors")
	}
	if device.IsGatewayAssignAuthError(wrapped) {
		t.Error("IsGatewayAssignAuthError should NOT match 429 even when wrapped")
	}
}

func TestBackoff_NoSharedState(t *testing.T) {
	// 验证多次调用之间没有共享状态导致冲突
	results := make(map[time.Duration]int)
	for i := 0; i < 100; i++ {
		d := backoff(0)
		results[d]++
	}
	// 应该有多个不同的 jitter 值
	if len(results) < 2 {
		t.Error("backoff(0) should produce varying jitter values across calls")
	}
}
