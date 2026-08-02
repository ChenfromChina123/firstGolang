package biz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return httptest.NewServer(NewHandler(st).Routes())
}

func doJSON(t *testing.T, method, url, userID string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-User-ID", userID)
	req.Header.Set("X-Auth-Username", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func createOrder(t *testing.T, srv, userID string) int64 {
	t.Helper()
	resp, out := doJSON(t, http.MethodPost, srv+"/api/orders", userID, map[string]any{
		"type": "服务", "title": "测试订单", "amount": 10000, "pay_amount": 10000,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create order: %d %v", resp.StatusCode, out)
	}
	return int64(out["id"].(float64))
}

func TestParseID(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         int64
		ok           bool
	}{
		{"/api/orders/12", "/api/orders/", 12, true},
		{"/api/orders/12/assign", "/api/orders/", 12, true},
		{"/api/orders/abc", "/api/orders/", 0, false},
		{"/api/orders/0", "/api/orders/", 0, false},
	}
	for _, c := range cases {
		got, ok := parseID(c.path, c.prefix)
		if got != c.want || ok != c.ok {
			t.Errorf("parseID(%q): got %d,%v want %d,%v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestPayOrderRequiresOwner(t *testing.T) {
	srv := newTestServer(t)
	id := createOrder(t, srv.URL, "user-a")

	// 他人支付 → 403
	resp, _ := doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/orders/%d", srv.URL, id), "user-b", map[string]any{"channel": "mock", "pay_no": "P1"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner pay: want 403 got %d", resp.StatusCode)
	}
	// 本人支付 → 200
	resp, _ = doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/orders/%d", srv.URL, id), "user-a", map[string]any{"channel": "mock", "pay_no": "P1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner pay: want 200 got %d", resp.StatusCode)
	}
}

func TestRefundRequiresOwnOrder(t *testing.T) {
	srv := newTestServer(t)
	id := createOrder(t, srv.URL, "user-a")
	doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/orders/%d", srv.URL, id), "user-a", map[string]any{"channel": "mock", "pay_no": "P2"})
	// 他人申请退款 → 403
	resp, _ := doJSON(t, http.MethodPost, srv.URL+"/api/refunds", "user-b", map[string]any{"order_id": id, "amount": 10000, "reason": "not mine"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner refund: want 403 got %d", resp.StatusCode)
	}
	// 本人申请退款 → 201
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/refunds", "user-a", map[string]any{"order_id": id, "amount": 10000, "reason": "change plan"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner refund: want 201 got %d", resp.StatusCode)
	}
}

func TestRBACRolesRequiresPermission(t *testing.T) {
	srv := newTestServer(t)
	// 普通用户（seed 只绑定了 admin）→ 403
	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/api/rbac/roles", "user-a", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("roles for non-admin: want 403 got %d", resp.StatusCode)
	}
	// admin（seed super_admin）→ 200
	resp, _ = doJSON(t, http.MethodGet, srv.URL+"/api/rbac/roles", "admin", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("roles for admin: want 200 got %d", resp.StatusCode)
	}
}

func TestResourceWhitelistRequiresOwner(t *testing.T) {
	srv := newTestServer(t)
	resp, out := doJSON(t, http.MethodPost, srv.URL+"/api/resources", "user-a", map[string]any{"name": "白名单测试", "type": "文档", "policy": "whitelist"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create resource: %d %v", resp.StatusCode, out)
	}
	id := int64(out["id"].(float64))
	// 非所有者加白名单 → 403
	resp, _ = doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/resources/%d/whitelist", srv.URL, id), "user-b", map[string]any{"user_id": "user-c"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner whitelist: want 403 got %d", resp.StatusCode)
	}
	// 所有者加白名单 → 200
	resp, _ = doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/resources/%d/whitelist", srv.URL, id), "user-a", map[string]any{"user_id": "user-c"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner whitelist: want 200 got %d", resp.StatusCode)
	}
}

func TestServiceOrderRequiresPaidOrder(t *testing.T) {
	srv := newTestServer(t)
	// 无 order_id → 400
	resp, _ := doJSON(t, http.MethodPost, srv.URL+"/api/services/orders", "user-a", map[string]any{"service_id": 1, "requirement": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("service order without order_id: want 400 got %d", resp.StatusCode)
	}
	// 未支付订单 → 400
	unpaid := createOrder(t, srv.URL, "user-a")
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/services/orders", "user-a", map[string]any{"order_id": unpaid, "service_id": 1, "requirement": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("service order with unpaid order: want 400 got %d", resp.StatusCode)
	}
	// 已支付订单 → 201
	paid := createOrder(t, srv.URL, "user-a")
	if resp, _ = doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/orders/%d", srv.URL, paid), "user-a", map[string]any{"channel": "mock", "pay_no": "P3"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pay order failed: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/services/orders", "user-a", map[string]any{"order_id": paid, "service_id": 1, "requirement": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("service order with paid order: want 201 got %d", resp.StatusCode)
	}
}

func TestMain(m *testing.M) {
	// handler 测试直接带 X-Auth-* 头（不经中间件），等价于 AUTH_MODE=dev
	_ = os.Setenv("AUTH_MODE", "dev")
	os.Exit(m.Run())
}
