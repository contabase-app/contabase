package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const investmentBodyLimitTarget = 64 << 10

type investmentBodyLimitReader struct {
	reader io.Reader
	reads  int
	bytes  int
}

func (r *investmentBodyLimitReader) Read(p []byte) (int, error) {
	r.reads++
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

type investmentBodyLimitSnapshot struct {
	investments  int
	transactions int
	balance      int
}

func readInvestmentBodyLimitSnapshot(t *testing.T, h *investimentosRoutesHarness) investmentBodyLimitSnapshot {
	t.Helper()
	var snapshot investmentBodyLimitSnapshot
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments WHERE workspace_id = 'workspace-rbac'`).Scan(&snapshot.investments); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&snapshot.transactions); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COALESCE(SUM(current_balance), 0) FROM accounts`).Scan(&snapshot.balance); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func investmentBodyLimitBody(t *testing.T, h *investimentosRoutesHarness, size int, formCSRF bool) (string, string) {
	t.Helper()
	form := investimentosRouteForm()
	token := h.signer.issue(time.Now())
	if formCSRF {
		form.Set("csrf_token", token)
	}
	base := form.Encode()
	padding := size - len(base) - len("&pad=")
	if padding < 0 {
		t.Fatalf("base form length %d exceeds target %d", len(base), size)
	}
	form.Set("pad", strings.Repeat("a", padding))
	body := form.Encode()
	if len(body) != size {
		t.Fatalf("body length = %d, want %d", len(body), size)
	}
	return body, token
}

func investmentBodyLimitRequest(t *testing.T, h *investimentosRoutesHarness, path, session, body string, contentLength int64, csrfToken string, csrfInHeader bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	}
	if csrfToken != "" {
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
		if csrfInHeader {
			req.Header.Set(csrfHeaderName, csrfToken)
		}
	}
	req.ContentLength = contentLength
	if contentLength < 0 {
		req.TransferEncoding = []string{"chunked"}
	}
	return req
}

func investmentBodyLimitServe(h *investimentosRoutesHarness, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	return rr
}

func TestInvestmentBodyLimitAcceptsBelowAndExactLimit(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)

	belowBody := investimentosRouteForm().Encode()
	if len(belowBody) >= investmentBodyLimitTarget {
		t.Fatalf("below-limit form unexpectedly has %d bytes", len(belowBody))
	}
	rr := investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, belowBody, int64(len(belowBody)), h.signer.issue(time.Now()), true))
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)

	exactHeaderBody, exactHeaderToken := investmentBodyLimitBody(t, h, investmentBodyLimitTarget, false)
	rr = investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, exactHeaderBody, int64(len(exactHeaderBody)), exactHeaderToken, true))
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)

	exactFormBody, exactFormToken := investmentBodyLimitBody(t, h, investmentBodyLimitTarget, true)
	rr = investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, exactFormBody, int64(len(exactFormBody)), exactFormToken, false))
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)

	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.investments != 3 || snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("accepted bodies changed state: %+v", snapshot)
	}
}

func TestInvestmentBodyLimitRejectsOversizedContentLengthBeforeReading(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)

	largeBody := strings.Repeat("x", investmentBodyLimitTarget+1)
	counted := &investmentBodyLimitReader{reader: strings.NewReader(largeBody)}
	req := httptest.NewRequest(http.MethodPost, "/investimentos", counted)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.ContentLength = investmentBodyLimitTarget + 1
	rr := investmentBodyLimitServe(h, req)
	requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)
	if counted.reads != 0 || counted.bytes != 0 {
		t.Fatalf("oversized Content-Length was read: reads=%d bytes=%d", counted.reads, counted.bytes)
	}

	token := h.signer.issue(time.Now())
	rr = investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, largeBody, int64(len(largeBody)), token, true))
	requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)

	formToken := h.signer.issue(time.Now())
	formBody := url.Values{"csrf_token": {formToken}, "pad": {strings.Repeat("x", investmentBodyLimitTarget+1)}}.Encode()
	rr = investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, formBody, int64(len(formBody)), formToken, false))
	requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)

	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.investments != 0 || snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("rejected bodies changed state: %+v", snapshot)
	}
}

func TestInvestmentBodyLimitRejectsChunkedOversizedBody(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)

	largeBody := strings.Repeat("x", investmentBodyLimitTarget+1)
	token := h.signer.issue(time.Now())
	rr := investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, largeBody, -1, token, true))
	requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)

	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.investments != 0 || snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("chunked rejection changed state: %+v", snapshot)
	}
}

func TestInvestmentBodyLimitCoversUnsupportedMutationMethods(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)

	body := strings.Repeat("x", investmentBodyLimitTarget+1)
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			token := h.signer.issue(time.Now())
			req := httptest.NewRequest(method, "/investimentos", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.admin})
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
			req.Header.Set(csrfHeaderName, token)
			req.ContentLength = int64(len(body))
			rr := investmentBodyLimitServe(h, req)
			requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)
		})
	}

	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.investments != 0 || snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("rejected mutation methods changed state: %+v", snapshot)
	}
}

func TestInvestmentBodyLimitAcceptsChunkedBodyAtLimit(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)

	body, token := investmentBodyLimitBody(t, h, investmentBodyLimitTarget, true)
	rr := investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, body, -1, token, false))
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)

	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.investments != 1 || snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("chunked accepted body changed state: %+v", snapshot)
	}
}

func TestInvestmentBodyLimitPreservesAuthCSRFAndRBACPrecedence(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	manager := h.session(t, "manager-user", "workspace-rbac")
	regular := h.session(t, "regular-user", "workspace-rbac")

	rr := h.request(t, http.MethodPost, "/investimentos", "", investimentosRouteForm(), true, false)
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)
	if rr.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated redirect = %q", rr.Header().Get("Location"))
	}
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos", "", investimentosRouteForm(), false, false), http.StatusForbidden)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos", h.admin, investimentosRouteForm(), false, false), http.StatusForbidden)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos", manager, investimentosRouteForm(), true, false), http.StatusForbidden)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos", regular, investimentosRouteForm(), true, false), http.StatusForbidden)

	form := investimentosRouteForm()
	formToken := h.signer.issue(time.Now())
	form.Set("csrf_token", formToken)
	formBody := form.Encode()
	rr = investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", h.admin, formBody, int64(len(formBody)), formToken, false))
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)

	largeBody := strings.Repeat("x", investmentBodyLimitTarget+1)
	for _, tc := range []struct {
		name         string
		session      string
		token        string
		csrfInHeader bool
	}{
		{"unauthenticated without csrf", "", "", false},
		{"authenticated without csrf", h.admin, "", false},
		{"authorized role with valid csrf", h.admin, h.signer.issue(time.Now()), true},
		{"denied role with valid csrf", manager, h.signer.issue(time.Now()), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos", tc.session, largeBody, int64(len(largeBody)), tc.token, tc.csrfInHeader))
			requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)
		})
	}

	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.investments != 1 || snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("precedence requests changed state: %+v", snapshot)
	}
}

func TestInvestmentBodyLimitFeatureFlagDisabledKeepsNotFound(t *testing.T) {
	h := newInvestimentosRoutesHarnessWithFlag(t, "false")

	rr := h.request(t, http.MethodPost, "/investimentos", "", investimentosRouteForm(), false, false)
	requireInvestimentosStatus(t, rr, http.StatusNotFound)

	counted := &investmentBodyLimitReader{reader: strings.NewReader(strings.Repeat("x", investmentBodyLimitTarget+1))}
	req := httptest.NewRequest(http.MethodPost, "/investimentos", counted)
	req.ContentLength = investmentBodyLimitTarget + 1
	rr = investmentBodyLimitServe(h, req)
	requireInvestimentosStatus(t, rr, http.StatusNotFound)
	if counted.reads != 0 || counted.bytes != 0 {
		t.Fatalf("disabled feature read the body: reads=%d bytes=%d", counted.reads, counted.bytes)
	}
}

func TestInvestmentBodyLimitRejectsOversizedEditWithoutMutation(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Body limit edit")

	var name string
	var updatedAt int64
	if err := h.db.QueryRow(`SELECT name, updated_at FROM investments WHERE id = ?`, id).Scan(&name, &updatedAt); err != nil {
		t.Fatal(err)
	}

	form := investimentosRouteForm()
	form.Set("name", "Alterado")
	form.Set("pad", strings.Repeat("x", investmentBodyLimitTarget+1))
	body := form.Encode()
	token := h.signer.issue(time.Now())
	rr := investmentBodyLimitServe(h, investmentBodyLimitRequest(t, h, "/investimentos/"+id+"/editar", h.admin, body, int64(len(body)), token, true))
	requireInvestimentosStatus(t, rr, http.StatusRequestEntityTooLarge)

	var afterName string
	var afterUpdatedAt int64
	if err := h.db.QueryRow(`SELECT name, updated_at FROM investments WHERE id = ?`, id).Scan(&afterName, &afterUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if afterName != name || afterUpdatedAt != updatedAt {
		t.Fatalf("rejected edit changed investment: name=%q updated_at=%d", afterName, afterUpdatedAt)
	}
	snapshot := readInvestmentBodyLimitSnapshot(t, h)
	if snapshot.transactions != 0 || snapshot.balance != 0 {
		t.Fatalf("rejected edit changed ledger: %+v", snapshot)
	}
}

func TestInvestmentSettlementBodyLimitRemainsBeforeAuth(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before, proof := f.stable(t), f.proof(t)

	oversized := f.form(0)
	oversized.Set("reason", strings.Repeat("x", investmentBodyLimitTarget+1))
	rr := f.h.request(t, http.MethodPost, f.path()+"/conciliar", f.h.admin, oversized, true, false)
	requireInvestimentosStatus(t, rr, http.StatusBadRequest)

	formToken := f.h.signer.issue(time.Now())
	formBody := url.Values{"csrf_token": {formToken}, "pad": {strings.Repeat("x", investmentBodyLimitTarget+1)}}.Encode()
	rr = investmentBodyLimitServe(f.h, investmentBodyLimitRequest(t, f.h, f.path()+"/conciliar", f.h.admin, formBody, int64(len(formBody)), formToken, false))
	requireInvestimentosStatus(t, rr, http.StatusForbidden)

	if f.stable(t) != before || f.proof(t) != proof {
		t.Fatal("settlement rejection changed financial state")
	}
}
