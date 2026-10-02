package locations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/locations/addresses/format":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":"invalid address","fields":{"phone":"bad"}}`))
		case "/api/locations/cities/9":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		default:
			_, _ = w.Write([]byte(`{"items":[{"type":"city","id":1,"name_en":"Cairo"}],"total":1}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/", nil)
	ctx := context.Background()
	hits, err := c.Search(ctx, "cai", SearchOptions{Type: "city", Limit: 5})
	if err != nil || len(hits) != 1 || hits[0].NameEN != "Cairo" {
		t.Fatalf("search: %+v %v", hits, err)
	}
	if _, err := c.Cities(ctx, 65, 10, 0); err != nil {
		t.Fatal(err)
	}
	if got[0] != "/api/locations/search?limit=5&q=cai&type=city" || got[1] != "/api/locations/cities?country_id=65&limit=10" {
		t.Errorf("requests: %v", got)
	}
	var e *Error
	if _, err := c.City(ctx, 9); !errors.As(err, &e) || e.Status != 404 {
		t.Errorf("404: %v", err)
	}
	if _, _, err := c.FormatAddress(ctx, Address{Phone: "1"}); !errors.As(err, &e) || e.Status != 422 || e.Fields["phone"] == "" {
		t.Errorf("422: %v", err)
	}
}
