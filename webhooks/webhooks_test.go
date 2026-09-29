package webhooks_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go/webhooks"
)

var secret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func deliver(h http.Handler, id string, ts time.Time, body, sig string) int {
	req := httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader(body))
	req.Header.Set(webhooks.HeaderID, id)
	req.Header.Set(webhooks.HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set(webhooks.HeaderSignature, sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestHandler(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var got []string
	h := webhooks.NewHandler(secret)
	h.Now = func() time.Time { return now }
	h.Seen = webhooks.NewMemoryDeduper()
	h.Handle(webhooks.MemberAdded, func(_ context.Context, e *webhooks.Envelope) error {
		d, err := e.Member()
		got = append(got, e.App.ProductTenantID+"/"+d.User.Email+"/"+d.Role)
		return err
	}).Handle(webhooks.EntitlementChanged, func(_ context.Context, e *webhooks.Envelope) error {
		d, _ := e.Entitlement()
		if d.Version == 0 {
			return errors.New("fail")
		}
		got = append(got, e.OrgID+"@"+strconv.FormatInt(d.Version, 10))
		return nil
	})

	body := `{"id":"ev1","type":"member.added","version":1,"occurred_at":"2027-01-15T08:00:00Z","org_id":"org-1",` +
		`"app":{"id":"demo","product_tenant_id":"t-1"},"data":{"user":{"id":"u2","email":"b@example.com","display_name":"B"},"role":"admin"}}`
	sig, _ := webhooks.Sign(secret, "ev1", now, []byte(body))
	// A rotation sends two signatures; either verifies.
	if c := deliver(h, "ev1", now, body, "v1,AAAA "+sig); c != 204 {
		t.Fatalf("delivery = %d", c)
	}
	if c := deliver(h, "ev1", now, body, sig); c != 204 || len(got) != 1 {
		t.Fatalf("redelivery = %d, %v", c, got)
	}
	if got[0] != "t-1/b@example.com/admin" {
		t.Fatalf("decoded %v", got)
	}

	if c := deliver(h, "ev2", now, body, sig); c != 401 {
		t.Fatalf("signature for another id = %d", c)
	}
	if c := deliver(h, "ev1x", now.Add(-6*time.Minute), body, sig); c != 401 {
		t.Fatalf("stale = %d", c)
	}
	tampered := strings.Replace(body, "admin", "owner", 1)
	if c := deliver(h, "ev1", now, tampered, sig); c != 401 {
		t.Fatalf("tampered = %d", c)
	}

	// A handler error answers 500 so the hub retries, and is not marked seen.
	bad := `{"id":"ev3","type":"entitlement.changed","version":1,"org_id":"org-1","data":{"version":0,"reason":"x"}}`
	s3, _ := webhooks.Sign(secret, "ev3", now, []byte(bad))
	if c := deliver(h, "ev3", now, bad, s3); c != 500 {
		t.Fatalf("handler error = %d", c)
	}
	good := `{"id":"ev4","type":"entitlement.changed","version":1,"org_id":"org-1","data":{"version":9,"reason":"plan.changed"}}`
	s4, _ := webhooks.Sign(secret, "ev4", now, []byte(good))
	if c := deliver(h, "ev4", now, good, s4); c != 204 || got[1] != "org-1@9" {
		t.Fatalf("entitlement = %d %v", c, got)
	}
	// Unknown types are acknowledged.
	other := `{"id":"ev5","type":"future.event","version":1,"data":{}}`
	s5, _ := webhooks.Sign(secret, "ev5", now, []byte(other))
	if c := deliver(h, "ev5", now, other, s5); c != 204 {
		t.Fatalf("unknown type = %d", c)
	}

	if err := webhooks.Verify([]string{"plain"}, http.Header{"Webhook-Id": {"a"}, "Webhook-Timestamp": {"1800000000"}, "Webhook-Signature": {sig}}, nil, 0, now); !errors.Is(err, webhooks.ErrBadSecret) {
		t.Fatalf("bad secret: %v", err)
	}
}
