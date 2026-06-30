package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/contabase-app/contabase/internal/assets"
	"github.com/contabase-app/contabase/internal/httpcookies"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
)

const (
	csrfCookieName = httpcookies.CSRF
	csrfHeaderName = "X-CSRF-Token"
)

type permission string

const (
	permDashboardRead        permission = "dashboard:read"
	permTransactionsRead     permission = "transactions:read"
	permTransactionsCreate   permission = "transactions:create"
	permTransactionsUpdate   permission = "transactions:update"
	permTransactionsDelete   permission = "transactions:delete"
	permInvoicesStatusUpdate permission = "invoices:status_update"
	permGoalsWrite           permission = "goals:write"
	permConfigRead           permission = "config:read"
	permConfigWrite          permission = "config:write"
	permMembersRead          permission = "members:read"
	permMembersWrite         permission = "members:write"
	permProfileRead          permission = "profile:read"
	permProfileWrite         permission = "profile:write"
	permAttachmentRead       permission = "attachment:read"
	permAdminGlobal          permission = "admin:global"
)

func HasPermission(member *models.WorkspaceMember, permission string) bool {
	return models.HasPermission(member, permission)
}

func hasPermission(role string, p permission) bool {
	member := &models.WorkspaceMember{Role: role}
	return HasPermission(member, string(p))
}

type csrfSigner struct {
	secret []byte
	ttl    time.Duration
}

func newCSRFSigner() (*csrfSigner, error) {
	secret, err := loadOrGenerateCSRFSecret()
	if err != nil {
		return nil, err
	}
	return &csrfSigner{secret: secret, ttl: 12 * time.Hour}, nil
}

func csrfSecretPath() string {
	return paths.CSRFSecretPath()
}

func loadOrGenerateCSRFSecret() ([]byte, error) {
	if existing, err := os.ReadFile(csrfSecretPath()); err == nil && len(existing) == 32 {
		return existing, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(csrfSecretPath()), 0700); err != nil {
		return secret, nil
	}
	if err := os.WriteFile(csrfSecretPath(), secret, 0600); err != nil {
		log.Printf("failed to persist csrf secret: %v", err)
	}
	return secret, nil
}

func (s *csrfSigner) issue(now time.Time) string {
	exp := now.Add(s.ttl).Unix()
	nonce := make([]byte, 24)
	_, _ = rand.Read(nonce)
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{
		strconvI64(exp),
		base64.RawURLEncoding.EncodeToString(nonce),
	}, ".")))
	sig := s.sign(payload)
	return payload + "." + sig
}

func (s *csrfSigner) validate(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	payload, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return false
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	pItems := strings.Split(string(rawPayload), ".")
	if len(pItems) != 2 {
		return false
	}
	expUnix, err := parseI64(pItems[0])
	if err != nil {
		return false
	}
	return now.Unix() < expUnix
}

func (s *csrfSigner) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *csrfSigner) ensureCookie(w http.ResponseWriter, r *http.Request) string {
	now := time.Now()
	if c, err := r.Cookie(csrfCookieName); err == nil && c != nil && s.validate(c.Value, now) {
		return c.Value
	}
	token := s.issue(now)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		Expires:  now.Add(s.ttl),
		MaxAge:   int(s.ttl.Seconds()),
	})
	return token
}

func (s *csrfSigner) tokenFromRequest(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get(csrfHeaderName)); token != "" {
		return token
	}
	if err := r.ParseForm(); err == nil {
		return strings.TrimSpace(r.FormValue("csrf_token"))
	}
	return ""
}

type httpBoundaryConfig struct {
	allowedHosts       map[string]struct{}
	trustedProxyRanges []*net.IPNet
	accessMode         string
	appBaseURLHost     string
}

func newHTTPBoundaryConfigFromEnv() httpBoundaryConfig {
	return newHTTPBoundaryConfigWithAccess(
		os.Getenv("ALLOWED_HOSTS"),
		os.Getenv("TRUSTED_PROXIES"),
		os.Getenv("CONTABASE_ACCESS_MODE"),
		os.Getenv("APP_BASE_URL"),
	)
}

func newHTTPBoundaryConfig(allowedHostsRaw, trustedProxiesRaw string) httpBoundaryConfig {
	return newHTTPBoundaryConfigWithAccess(allowedHostsRaw, trustedProxiesRaw, "", "")
}

func newHTTPBoundaryConfigWithAccess(allowedHostsRaw, trustedProxiesRaw, accessModeRaw, appBaseURLRaw string) httpBoundaryConfig {
	cfg := httpBoundaryConfig{
		allowedHosts:       make(map[string]struct{}),
		trustedProxyRanges: defaultTrustedProxyRanges(),
		accessMode:         normalizeAccessMode(accessModeRaw),
		appBaseURLHost:     normalizeHost(appBaseURLRaw),
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		cfg.allowedHosts[host] = struct{}{}
	}
	for _, item := range strings.Split(allowedHostsRaw, ",") {
		host := normalizeHost(item)
		if host == "" || host == "*" {
			continue
		}
		cfg.allowedHosts[host] = struct{}{}
	}
	for _, item := range strings.Split(trustedProxiesRaw, ",") {
		if ipNet, ok := parseTrustedProxyRange(item); ok {
			cfg.trustedProxyRanges = append(cfg.trustedProxyRanges, ipNet)
		}
	}
	return cfg
}

func normalizeAccessMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "lan":
		return "lan"
	case "proxy":
		return "proxy"
	case "local-docker", "docker-local":
		return "local-docker"
	default:
		return "local"
	}
}

func defaultTrustedProxyRanges() []*net.IPNet {
	ranges := make([]*net.IPNet, 0, 2)
	for _, cidr := range []string{"127.0.0.0/8", "::1/128"} {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			ranges = append(ranges, ipNet)
		}
	}
	return ranges
}

func parseTrustedProxyRange(raw string) (*net.IPNet, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, false
	}
	if _, ipNet, err := net.ParseCIDR(value); err == nil {
		return ipNet, true
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return nil, false
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, true
}

func normalizeHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if slash := strings.Index(host, "/"); slash >= 0 {
		host = host[:slash]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	host = strings.TrimSuffix(host, ".")
	return host
}

func (c httpBoundaryConfig) hostAllowed(host string) bool {
	_, ok := c.allowedHosts[normalizeHost(host)]
	return ok
}

func (c httpBoundaryConfig) requestHostAllowed(r *http.Request) bool {
	host := r.Host
	if strings.TrimSpace(host) == "" {
		host = r.URL.Host
	}
	return c.hostAllowed(host)
}

func remoteIP(r *http.Request) net.IP {
	remoteAddr := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = host
	}
	return net.ParseIP(strings.Trim(remoteAddr, "[]"))
}

func (c httpBoundaryConfig) requestFromTrustedProxy(r *http.Request) bool {
	ip := remoteIP(r)
	if ip == nil {
		return false
	}
	for _, ipNet := range c.trustedProxyRanges {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func (c httpBoundaryConfig) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !c.requestFromTrustedProxy(r) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func requestIsHTTPS(r *http.Request) bool {
	return newHTTPBoundaryConfigFromEnv().requestIsHTTPS(r)
}

func requestHostName(r *http.Request) string {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = strings.TrimSpace(r.URL.Host)
	}
	return normalizeHost(host)
}

func requestHostIsLoopback(r *http.Request) bool {
	switch requestHostName(r) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func hostNameIsLoopback(host string) bool {
	switch normalizeHost(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func isLoopbackLocalRequest(r *http.Request) bool {
	return requestHostIsLoopback(r) && isLoopbackIP(remoteIP(r))
}

func shouldUseSecureCookie(r *http.Request) bool {
	cfg := newHTTPBoundaryConfigFromEnv()
	return cfg.requestIsHTTPS(r) || !cfg.requestAllowsPlainHTTP(r)
}

func isLoopbackIP(ip net.IP) bool {
	return ip != nil && ip.IsLoopback()
}

func isPrivateOrLoopbackIP(ip net.IP) bool {
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func isPrivateOrLoopbackHost(host string) bool {
	normalized := normalizeHost(host)
	switch normalized {
	case "localhost":
		return true
	}
	return isPrivateOrLoopbackIP(net.ParseIP(normalized))
}

func isPrivateIPHost(host string) bool {
	ip := net.ParseIP(normalizeHost(host))
	return ip != nil && ip.IsPrivate()
}

func (c httpBoundaryConfig) requestAllowedLANHTTP(r *http.Request) bool {
	if c.accessMode != "lan" {
		return false
	}
	requestHost := requestHostName(r)
	if !isPrivateIPHost(requestHost) {
		return false
	}
	if c.appBaseURLHost == "" || !isPrivateIPHost(c.appBaseURLHost) {
		return false
	}
	if normalizeHost(requestHost) != c.appBaseURLHost {
		return false
	}
	return isPrivateOrLoopbackIP(remoteIP(r))
}

func (c httpBoundaryConfig) requestAllowedDockerLocalHTTP(r *http.Request) bool {
	if c.accessMode != "local-docker" {
		return false
	}
	for host := range c.allowedHosts {
		if !hostNameIsLoopback(host) {
			return false
		}
	}
	if !requestHostIsLoopback(r) {
		return false
	}
	if c.appBaseURLHost == "" || !hostNameIsLoopback(c.appBaseURLHost) {
		return false
	}
	return isPrivateOrLoopbackIP(remoteIP(r))
}

func (c httpBoundaryConfig) requestAllowsPlainHTTP(r *http.Request) bool {
	if !c.requestHostAllowed(r) {
		return false
	}
	return isLoopbackLocalRequest(r) || c.requestAllowedDockerLocalHTTP(r) || c.requestAllowedLANHTTP(r)
}

func enforceRemoteHTTPS(next http.Handler) http.Handler {
	return newHTTPBoundaryConfigFromEnv().enforceRemoteHTTPS(next)
}

func (c httpBoundaryConfig) enforceRemoteHTTPS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.requestHostAllowed(r) {
			http.Error(w, "host not allowed", http.StatusMisdirectedRequest)
			return
		}
		if c.requestAllowsPlainHTTP(r) || c.requestIsHTTPS(r) || strings.HasPrefix(r.URL.Path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(fmt.Sprintf(`<!doctype html>
<html lang="pt-BR" class="dark">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Acesso Remoto Bloqueado</title>
  <link rel="stylesheet" href="%s">
  <style>
    body { background: radial-gradient(circle at top, #1f1734 0%, #0b0b11 58%, #08080d 100%); }
  </style>
</head>
<body class="min-h-screen flex items-center justify-center p-4 sm:p-6 text-zinc-100 antialiased font-sans">
  <section class="w-full max-w-2xl bg-[#12121c]/90 backdrop-blur-md border border-amber-500/30 rounded-2xl p-6 sm:p-8 shadow-[0_18px_52px_rgba(0,0,0,0.45)]">
    <div class="flex items-center gap-4 mb-6">
      <div class="flex-shrink-0 w-12 h-12 rounded-full bg-amber-500/10 border border-amber-500/20 flex items-center justify-center text-amber-500">
        <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10"></path>
          <path d="m9 12 2 2 4-4"></path>
        </svg>
      </div>
      <div>
        <h1 class="text-xl sm:text-2xl font-semibold text-white tracking-tight">Acesso Remoto Bloqueado por Segurança</h1>
      </div>
    </div>

    <div class="text-zinc-300 text-sm sm:text-base leading-relaxed mb-8 pb-8 border-b border-white/10">
      O ContaBase detectou uma tentativa de acesso vinda de um endereço externo (Host detectado: <span class="font-mono text-amber-400 bg-amber-400/10 px-1.5 py-0.5 rounded">`+html.EscapeString(requestHostName(r))+`</span>) sem uma camada de proteção ativa.
    </div>

    <div class="space-y-6">
      <!-- Opção A -->
      <div class="border border-white/10 rounded-xl overflow-hidden bg-white/5">
        <div class="px-5 py-4 border-b border-white/10">
          <h2 class="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-zinc-400"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"></path><polyline points="9 22 9 12 15 12 15 22"></polyline></svg>
            Opção A: Acesso Local (Mesma Máquina)
          </h2>
          <p class="text-sm text-zinc-400 mt-2 leading-relaxed">
            Por segurança, o ContaBase exige HTTPS para acesso remoto, exceto quando a instalação foi configurada explicitamente em modo LAN para um IP privado. Para uso somente local, acesse diretamente do navegador da própria máquina onde o servidor está rodando usando <code class="text-amber-400">http://localhost:8080</code> ou <code class="text-amber-400">http://127.0.0.1:8080</code>.
          </p>
          <p class="text-xs text-zinc-500 mt-2 leading-relaxed">
            Em Docker local somente nesta máquina, use <code class="text-amber-400">CONTABASE_ACCESS_MODE=local-docker</code> com <code class="text-amber-400">APP_BASE_URL=http://localhost:8080</code>. O acesso HTTP por IP de rede local (ex: 192.168.x.x) só é aceito quando <code class="text-amber-400">CONTABASE_ACCESS_MODE=lan</code>, <code class="text-amber-400">APP_BASE_URL</code> aponta para esse IP privado e o host está em <code class="text-amber-400">ALLOWED_HOSTS</code>. IP público ou domínio HTTP sem proxy continua bloqueado.
          </p>
        </div>
      </div>

      <!-- Opção B -->
      <div class="border border-violet-500/20 rounded-xl overflow-hidden bg-violet-500/5">
        <div class="px-5 py-4 border-b border-violet-500/10">
          <h2 class="text-sm font-semibold flex items-center gap-2 text-violet-300">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect><path d="M7 11V7a5 5 0 0 1 10 0v4"></path></svg>
            Opção B: Uso Seguro com Domínio e SSL (Recomendado para Internet ou Redes Corporativas)
          </h2>
          <p class="text-sm text-zinc-400 mt-2 leading-relaxed">
            Para acessar o sistema de forma profissional e criptografada (seja na internet ou via rede local segura), configure seu Proxy Reverso (Nginx, Caddy, etc.) gerenciando o certificado SSL e aponte para a porta 8080. Em seguida, proteja o sistema registrando estritamente o seu domínio oficial:
          </p>
        </div>
        <div class="bg-black/40 p-4 overflow-x-auto">
          <pre class="text-sm text-emerald-400 font-mono"><code>environment:
  - ALLOWED_HOSTS=seu-dominio.com</code></pre>
        </div>
      </div>
    </div>
  </section>
</body>
</html>`, "/"+assets.VersionedPath("assets/css/style.css"))))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return newHTTPBoundaryConfigFromEnv().securityHeaders(next)
}

const cspReportOnlyPolicy = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'"

const cspEnforcingPolicy = "frame-ancestors 'none'; form-action 'self'; base-uri 'self'; object-src 'none'; connect-src 'self'"

func (c httpBoundaryConfig) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy-Report-Only", cspReportOnlyPolicy)
		w.Header().Set("Content-Security-Policy", cspEnforcingPolicy)
		if c.requestIsHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type rateLimitPolicy struct {
	Limit  int
	Window time.Duration
}

type rateLimitDecision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAt    time.Time
	RetryAfter time.Duration
}

type rateLimitBucket struct {
	hits     []time.Time
	lastSeen time.Time
	window   time.Duration
}

type memoryRateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]*rateLimitBucket
	cleanupEvery   time.Duration
	lastCleanupRun time.Time
}

func newMemoryRateLimiter() *memoryRateLimiter {
	return &memoryRateLimiter{
		buckets:      make(map[string]*rateLimitBucket),
		cleanupEvery: 2 * time.Minute,
	}
}

func (l *memoryRateLimiter) Allow(key string, policy rateLimitPolicy, now time.Time) rateLimitDecision {
	if policy.Limit <= 0 || policy.Window <= 0 {
		return rateLimitDecision{Allowed: true, Limit: policy.Limit}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.IsZero() {
		now = time.Now()
	}

	if l.lastCleanupRun.IsZero() || now.Sub(l.lastCleanupRun) >= l.cleanupEvery {
		for entryKey, bucket := range l.buckets {
			ttl := bucket.window * 2
			if ttl <= 0 {
				ttl = policy.Window * 2
			}
			if now.Sub(bucket.lastSeen) > ttl {
				delete(l.buckets, entryKey)
			}
		}
		l.lastCleanupRun = now
	}

	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &rateLimitBucket{}
		l.buckets[key] = bucket
	}
	bucket.lastSeen = now
	bucket.window = policy.Window

	cutoff := now.Add(-policy.Window)
	activeHits := bucket.hits[:0]
	for _, hit := range bucket.hits {
		if hit.After(cutoff) {
			activeHits = append(activeHits, hit)
		}
	}
	bucket.hits = activeHits

	if len(bucket.hits) >= policy.Limit {
		resetAt := bucket.hits[0].Add(policy.Window)
		retryAfter := resetAt.Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return rateLimitDecision{
			Allowed:    false,
			Limit:      policy.Limit,
			Remaining:  0,
			ResetAt:    resetAt,
			RetryAfter: retryAfter,
		}
	}

	bucket.hits = append(bucket.hits, now)
	resetAt := bucket.hits[0].Add(policy.Window)
	return rateLimitDecision{
		Allowed:   true,
		Limit:     policy.Limit,
		Remaining: policy.Limit - len(bucket.hits),
		ResetAt:   resetAt,
	}
}

func writeRateLimitExceeded(w http.ResponseWriter, _ *http.Request, decision rateLimitDecision) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(decision.RetryAfter)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, "muitas tentativas, tente novamente em alguns minutos")
}

func allowRateLimitedRequest(w http.ResponseWriter, r *http.Request, limiter *memoryRateLimiter, key string, policy rateLimitPolicy, now time.Time) bool {
	if limiter == nil {
		return true
	}
	decision := limiter.Allow(key, policy, now)
	if decision.Allowed {
		return true
	}
	writeRateLimitExceeded(w, r, decision)

	family := key
	if idx := strings.Index(key, ":"); idx >= 0 {
		family = key[:idx]
	}
	slog.Warn("rate_limit_blocked",
		"family", family,
		"key_hash", rateLimitKeyHash(key),
		"client_ip", clientIP(r),
		"method", r.Method,
		"path", r.URL.Path,
		"retry_after", retryAfterSeconds(decision.RetryAfter),
	)

	return false
}

func rateLimitKeyHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func retryAfterSeconds(duration time.Duration) int {
	if duration <= 0 {
		return 1
	}
	seconds := int((duration + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func clientIP(r *http.Request) string {
	return newHTTPBoundaryConfigFromEnv().clientIP(r)
}

func (c httpBoundaryConfig) clientIP(r *http.Request) string {
	directIP := directClientIP(r)
	if c.requestFromTrustedProxy(r) {
		if ip := forwardedHeaderClientIP(r); ip != "" {
			return ip
		}
	}
	return directIP
}

func forwardedHeaderClientIP(r *http.Request) string {
	if ip := firstForwardedIP(r.Header.Get("X-Forwarded-For")); ip != "" {
		return ip
	}
	if ip := validHeaderIP(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	if ip := validHeaderIP(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	return ""
}

func firstForwardedIP(value string) string {
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		return validHeaderIP(part)
	}
	return ""
}

func validHeaderIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func directClientIP(r *http.Request) string {
	if ip := remoteIP(r); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func parseI64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func strconvI64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
