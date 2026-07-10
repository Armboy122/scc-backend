package handler

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type loginLimiterConfig struct {
	ClientLimit      int
	ClientWindow     time.Duration
	IdentityLimit    int
	IdentityWindow   time.Duration
	MaxEntriesPerMap int
	CleanupInterval  time.Duration
	Now              func() time.Time
}

func defaultLoginLimiterConfig() loginLimiterConfig {
	return loginLimiterConfig{
		ClientLimit:      30,
		ClientWindow:     5 * time.Minute,
		IdentityLimit:    5,
		IdentityWindow:   15 * time.Minute,
		MaxEntriesPerMap: 4096,
		CleanupInterval:  time.Minute,
		Now:              time.Now,
	}
}

type loginFailureWindow struct {
	failures []time.Time
	lastSeen time.Time
}

// loginAttemptLimiter is a bounded, process-local sliding-window limiter. One
// VPS owns the API, so this avoids a new distributed dependency while still
// constraining both broad client attacks and targeted username guessing.
type loginAttemptLimiter struct {
	mu sync.Mutex

	clients     map[string]*loginFailureWindow
	identities  map[string]*loginFailureWindow
	config      loginLimiterConfig
	lastCleanup time.Time
}

func newLoginAttemptLimiter(config loginLimiterConfig) *loginAttemptLimiter {
	if config.ClientLimit < 1 {
		config.ClientLimit = 1
	}
	if config.IdentityLimit < 1 {
		config.IdentityLimit = 1
	}
	if config.ClientWindow <= 0 {
		config.ClientWindow = time.Minute
	}
	if config.IdentityWindow <= 0 {
		config.IdentityWindow = time.Minute
	}
	if config.MaxEntriesPerMap < 1 {
		config.MaxEntriesPerMap = 1
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = time.Minute
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &loginAttemptLimiter{
		clients: make(map[string]*loginFailureWindow), identities: make(map[string]*loginFailureWindow),
		config: config,
	}
}

func (l *loginAttemptLimiter) RetryAfter(clientIP, username string) (time.Duration, bool) {
	now := l.config.Now().UTC()
	clientKey := normalizedClientKey(clientIP)
	identityKey := clientKey + "\x00" + normalizeLoginUsername(username)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	clientRetry := l.retryAfterLocked(l.clients, clientKey, l.config.ClientLimit, l.config.ClientWindow, now)
	identityRetry := l.retryAfterLocked(l.identities, identityKey, l.config.IdentityLimit, l.config.IdentityWindow, now)
	if identityRetry > clientRetry {
		clientRetry = identityRetry
	}
	return clientRetry, clientRetry > 0
}

// BeginAttempt atomically checks both windows and reserves one attempt. This
// prevents a concurrent login burst from all passing a check-before-record
// race. Successful authentication removes the reserved history via Reset.
func (l *loginAttemptLimiter) BeginAttempt(clientIP, username string) (time.Duration, bool) {
	now := l.config.Now().UTC()
	clientKey := normalizedClientKey(clientIP)
	identityKey := clientKey + "\x00" + normalizeLoginUsername(username)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	clientRetry := l.retryAfterLocked(l.clients, clientKey, l.config.ClientLimit, l.config.ClientWindow, now)
	identityRetry := l.retryAfterLocked(l.identities, identityKey, l.config.IdentityLimit, l.config.IdentityWindow, now)
	if identityRetry > clientRetry {
		clientRetry = identityRetry
	}
	if clientRetry > 0 {
		return clientRetry, true
	}
	l.recordFailureLocked(l.clients, clientKey, l.config.ClientLimit, l.config.ClientWindow, now)
	l.recordFailureLocked(l.identities, identityKey, l.config.IdentityLimit, l.config.IdentityWindow, now)
	return 0, false
}

func (l *loginAttemptLimiter) RecordFailure(clientIP, username string) {
	now := l.config.Now().UTC()
	clientKey := normalizedClientKey(clientIP)
	identityKey := clientKey + "\x00" + normalizeLoginUsername(username)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	l.recordFailureLocked(l.clients, clientKey, l.config.ClientLimit, l.config.ClientWindow, now)
	l.recordFailureLocked(l.identities, identityKey, l.config.IdentityLimit, l.config.IdentityWindow, now)
}

func (l *loginAttemptLimiter) Reset(clientIP, username string) {
	clientKey := normalizedClientKey(clientIP)
	identityKey := clientKey + "\x00" + normalizeLoginUsername(username)
	l.mu.Lock()
	// A successful credential check resets only that identity. Keeping the
	// broad client window prevents one known-good account from being used to
	// repeatedly clear an IP's budget while guessing other usernames.
	delete(l.identities, identityKey)
	l.mu.Unlock()
}

func (l *loginAttemptLimiter) retryAfterLocked(
	windows map[string]*loginFailureWindow,
	key string,
	limit int,
	window time.Duration,
	now time.Time,
) time.Duration {
	bucket := windows[key]
	if bucket == nil {
		return 0
	}
	pruneFailures(bucket, now.Add(-window))
	if len(bucket.failures) == 0 {
		delete(windows, key)
		return 0
	}
	if len(bucket.failures) < limit {
		return 0
	}
	retry := bucket.failures[0].Add(window).Sub(now)
	if retry <= 0 {
		return time.Second
	}
	return retry
}

func (l *loginAttemptLimiter) recordFailureLocked(
	windows map[string]*loginFailureWindow,
	key string,
	limit int,
	window time.Duration,
	now time.Time,
) {
	bucket := windows[key]
	if bucket == nil {
		if len(windows) >= l.config.MaxEntriesPerMap {
			evictOldestLoginWindow(windows)
		}
		bucket = &loginFailureWindow{}
		windows[key] = bucket
	}
	pruneFailures(bucket, now.Add(-window))
	if len(bucket.failures) < limit {
		bucket.failures = append(bucket.failures, now)
	}
	bucket.lastSeen = now
}

func (l *loginAttemptLimiter) cleanupLocked(now time.Time) {
	if !l.lastCleanup.IsZero() && now.Sub(l.lastCleanup) < l.config.CleanupInterval && !now.Before(l.lastCleanup) {
		return
	}
	cleanupLoginWindows(l.clients, now.Add(-l.config.ClientWindow))
	cleanupLoginWindows(l.identities, now.Add(-l.config.IdentityWindow))
	l.lastCleanup = now
}

func cleanupLoginWindows(windows map[string]*loginFailureWindow, cutoff time.Time) {
	for key, bucket := range windows {
		pruneFailures(bucket, cutoff)
		if len(bucket.failures) == 0 {
			delete(windows, key)
		}
	}
}

func pruneFailures(bucket *loginFailureWindow, cutoff time.Time) {
	firstCurrent := 0
	for firstCurrent < len(bucket.failures) && !bucket.failures[firstCurrent].After(cutoff) {
		firstCurrent++
	}
	if firstCurrent > 0 {
		bucket.failures = append(bucket.failures[:0], bucket.failures[firstCurrent:]...)
	}
}

func evictOldestLoginWindow(windows map[string]*loginFailureWindow) {
	var oldestKey string
	var oldest time.Time
	for key, bucket := range windows {
		if oldestKey == "" || bucket.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = bucket.lastSeen
		}
	}
	if oldestKey != "" {
		delete(windows, oldestKey)
	}
}

func (l *loginAttemptLimiter) sizes() (clients, identities int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients), len(l.identities)
}

func normalizeLoginUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func normalizedClientKey(clientIP string) string {
	if address, err := netip.ParseAddr(strings.TrimSpace(clientIP)); err == nil {
		return address.Unmap().String()
	}
	return "unknown"
}

// loginClientIP uses the right-most syntactically valid forwarded hop. A
// trusted edge appends/sets that hop, so attacker-supplied values earlier in
// the chain cannot choose an arbitrary limiter bucket. RemoteAddr is the safe
// fallback when no forwarded IP is usable.
func loginClientIP(r *http.Request) string {
	forwardedValues := r.Header.Values("X-Forwarded-For")
	for valueIndex := len(forwardedValues) - 1; valueIndex >= 0; valueIndex-- {
		hops := strings.Split(forwardedValues[valueIndex], ",")
		for hopIndex := len(hops) - 1; hopIndex >= 0; hopIndex-- {
			if address, ok := parseLoginIP(hops[hopIndex]); ok {
				return address
			}
		}
	}
	if address, ok := parseLoginIP(r.RemoteAddr); ok {
		return address
	}
	return "unknown"
}

func parseLoginIP(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap().String(), true
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return "", false
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return "", false
	}
	return address.Unmap().String(), true
}

func retryAfterSeconds(duration time.Duration) int {
	seconds := int(math.Ceil(duration.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}
