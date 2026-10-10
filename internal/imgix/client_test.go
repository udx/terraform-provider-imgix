package imgix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *Client {
	c := NewClient("test-key")
	c.baseURL = server.URL
	c.baseDelay = time.Millisecond // keep retry tests fast
	return c
}

func sourceDoc(id string, attrs map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"data": map[string]interface{}{
			"type":       "sources",
			"id":         id,
			"attributes": attrs,
		},
		"jsonapi": map[string]interface{}{"version": "1.0"},
	}
}

func TestGetSourceDecodesEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sources/abc123" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if got := r.Header.Get("Accept"); got != jsonAPIType {
			t.Fatalf("unexpected accept header %q", got)
		}
		json.NewEncoder(w).Encode(sourceDoc("abc123", map[string]interface{}{
			"name":              "media",
			"enabled":           true,
			"deployment_status": "deployed",
			"date_deployed":     1700000000,
			"deployment": map[string]interface{}{
				"type":             "gcs",
				"imgix_subdomains": []string{"example-media"},
				"gcs_bucket":       "media.example.com",
				"gcs_access_key":   "AKIA...",
				"custom_domains":   []string{},
				"default_params":   map[string]string{"auto": "compress,format"},
			},
		}))
	}))
	defer server.Close()

	src, err := testClient(server).GetSource(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if src.ID != "abc123" || src.Name != "media" || src.DeploymentStatus != "deployed" {
		t.Fatalf("unexpected source: %+v", src)
	}
	if src.Enabled == nil || !*src.Enabled {
		t.Fatal("enabled not decoded")
	}
	if src.Deployment == nil || src.Deployment.GCSBucket != "media.example.com" {
		t.Fatalf("deployment not decoded: %+v", src.Deployment)
	}
	if src.Deployment.DefaultParams["auto"] != "compress,format" {
		t.Fatalf("default_params not decoded: %+v", src.Deployment.DefaultParams)
	}
}

func TestErrorEnvelopeMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": []map[string]interface{}{{
				"detail": "Resource not found.",
				"id":     "req-1",
				"status": "404",
				"title":  "NotFound",
			}},
		})
	}))
	defer server.Close()

	_, err := testClient(server).GetSource(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !NotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	apiErr := err.(*Error)
	if apiErr.RequestID != "req-1" || apiErr.Detail != "Resource not found." {
		t.Fatalf("unexpected error mapping: %+v", apiErr)
	}
}

func TestUnauthorizedIsNotNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": []map[string]interface{}{{"status": "401", "title": "Unauthorized"}},
		})
	}))
	defer server.Close()

	_, err := testClient(server).GetSource(context.Background(), "x")
	if err == nil || NotFound(err) {
		t.Fatalf("401 must surface as error but never as not-found: %v", err)
	}
}

func TestRetryOn429HonorsRetryAfter(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]interface{}{{"status": "429", "title": "TooManyRequests"}},
			})
			return
		}
		json.NewEncoder(w).Encode(sourceDoc("ok", map[string]interface{}{"name": "media"}))
	}))
	defer server.Close()

	src, err := testClient(server).GetSource(context.Background(), "ok")
	if err != nil {
		t.Fatal(err)
	}
	if src.ID != "ok" || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected one retry then success, got %d calls", calls)
	}
}

func TestValidationErrorIsNotRetried(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			atomic.AddInt32(&posts, 1)
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": []map[string]interface{}{{"status": "400", "title": "BadRequest", "detail": "imgix_subdomains has already been taken"}},
		})
	}))
	defer server.Close()

	_, err := testClient(server).CreateSource(context.Background(), map[string]interface{}{
		"name":       "dup",
		"deployment": map[string]interface{}{"type": "gcs", "imgix_subdomains": []string{"taken"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// The follow-up conflict-lookup GET is expected; the POST must not repeat.
	if atomic.LoadInt32(&posts) != 1 {
		t.Fatalf("400 must not be retried, got %d POST calls", posts)
	}
	if !strings.Contains(err.Error(), "already been taken") {
		t.Fatalf("conflict detail must surface: %v", err)
	}
}

func TestAmbiguousCreateRecoversBySubdomains(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sources":
			// Commit server-side, then drop the connection before responding.
			atomic.AddInt32(&posts, 1)
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("hijacking unsupported")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
		case r.Method == "GET" && r.URL.Path == "/sources":
			if got := r.URL.Query().Get("filter[deployment.imgix_subdomains]"); got != "recovered" {
				t.Fatalf("unexpected filter %q", got)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{
					"type": "sources",
					"id":   "recovered-id",
					"attributes": map[string]interface{}{
						"name": "recovered",
						"deployment": map[string]interface{}{
							"type":             "gcs",
							"imgix_subdomains": []string{"recovered"},
						},
					},
				}},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	src, err := testClient(server).CreateSource(context.Background(), map[string]interface{}{
		"name": "recovered",
		"deployment": map[string]interface{}{
			"type":             "gcs",
			"imgix_subdomains": []string{"recovered"},
			"gcs_bucket":       "bucket",
		},
	})
	if err != nil {
		t.Fatalf("expected recovery, got %v", err)
	}
	if src.ID != "recovered-id" {
		t.Fatalf("expected recovered source, got %+v", src)
	}
	if atomic.LoadInt32(&posts) != 1 {
		t.Fatalf("a duplicate POST would have been sent %d times", posts)
	}
}

func TestSparsePatchSendsOnlyManagedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Fatalf("unexpected method %s", r.Method)
		}
		var doc map[string]interface{}
		json.NewDecoder(r.Body).Decode(&doc)
		attrs := doc["data"].(map[string]interface{})["attributes"].(map[string]interface{})
		dep, ok := attrs["deployment"].(map[string]interface{})
		if !ok {
			t.Fatalf("deployment missing from patch: %v", attrs)
		}
		if len(dep) != 1 || dep["cache_ttl_value"] != float64(86400) {
			t.Fatalf("patch must contain only the changed field: %v", dep)
		}
		if _, leaked := dep["gcs_secret_key"]; leaked {
			t.Fatal("secret must never appear in a sparse patch")
		}
		json.NewEncoder(w).Encode(sourceDoc("id1", map[string]interface{}{"name": "media"}))
	}))
	defer server.Close()

	_, err := testClient(server).UpdateSource(context.Background(), "id1", map[string]interface{}{
		"deployment": map[string]interface{}{"cache_ttl_value": 86400},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListFollowsPagination(t *testing.T) {
	var page2Hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page[number]") {
		case "1":
			atomic.AddInt32(&page2Hits, 1)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{
					"type": "sources", "id": "b",
					"attributes": map[string]interface{}{"name": "b"},
				}},
				"meta": map[string]interface{}{"pagination": map[string]interface{}{
					"currentPage": 1, "hasNextPage": false,
				}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{
					"type": "sources", "id": "a",
					"attributes": map[string]interface{}{"name": "a"},
				}},
				"meta": map[string]interface{}{"pagination": map[string]interface{}{
					"currentPage": 0, "hasNextPage": true, "nextPage": 1,
				}},
			})
		}
	}))
	defer server.Close()

	sources, err := testClient(server).ListSources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].ID != "a" || sources[1].ID != "b" {
		t.Fatalf("pagination not followed: %+v", sources)
	}
	if atomic.LoadInt32(&page2Hits) != 1 {
		t.Fatal("page 2 not requested")
	}
}

func TestWaitForStatusTerminals(t *testing.T) {
	t.Run("deployed", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			status := "deploying"
			if atomic.AddInt32(&calls, 1) >= 2 {
				status = "deployed"
			}
			json.NewEncoder(w).Encode(sourceDoc("id1", map[string]interface{}{"name": "x", "deployment_status": status}))
		}))
		defer server.Close()

		src, err := testClient(server).WaitForStatus(context.Background(), "id1", false, time.Millisecond)
		if err != nil || src.DeploymentStatus != "deployed" {
			t.Fatalf("unexpected: %v %+v", err, src)
		}
	})

	t.Run("disabled target does not wait for deployed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(sourceDoc("id1", map[string]interface{}{"name": "x", "deployment_status": "disabled"}))
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		src, err := testClient(server).WaitForStatus(ctx, "id1", true, time.Millisecond)
		if err != nil || src.DeploymentStatus != "disabled" {
			t.Fatalf("unexpected: %v %+v", err, src)
		}
	})

	t.Run("deleted fails fast", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(sourceDoc("id1", map[string]interface{}{"name": "x", "deployment_status": "deleted"}))
		}))
		defer server.Close()

		_, err := testClient(server).WaitForStatus(context.Background(), "id1", false, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "deleted") {
			t.Fatalf("expected deleted error, got %v", err)
		}
	})

	t.Run("timeout names the source and last status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(sourceDoc("id9", map[string]interface{}{"name": "x", "deployment_status": "deploying"}))
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := testClient(server).WaitForStatus(ctx, "id9", false, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "id9") || !strings.Contains(err.Error(), "deploying") {
			t.Fatalf("timeout must name source and last status: %v", err)
		}
	})

	t.Run("auth failure fails fast", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]interface{}{{"status": "401", "title": "Unauthorized"}},
			})
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := testClient(server).WaitForStatus(ctx, "id1", false, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected fast auth failure, got %v", err)
		}
		if atomic.LoadInt32(&calls) > 3 {
			t.Fatalf("auth failure must not be polled until timeout, got %d calls", calls)
		}
	})
}

func TestDeleteSemantics(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			t.Fatalf("unexpected %s", r.Method)
		}))
		defer server.Close()
		if err := testClient(server).DeleteSource(context.Background(), "id1"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("405 means unsupported", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]interface{}{{"status": "405", "title": "MethodNotAllowed"}},
			})
		}))
		defer server.Close()
		if err := testClient(server).DeleteSource(context.Background(), "id1"); !DeletionUnsupported(err) {
			t.Fatalf("expected ErrDeletionUnsupported, got %v", err)
		}
	})

	t.Run("404 route miss with surviving source means unsupported", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []map[string]interface{}{{"status": "404", "title": "NotFound"}},
				})
				return
			}
			// GET still finds the source → the DELETE 404 was a route miss.
			json.NewEncoder(w).Encode(sourceDoc("id1", map[string]interface{}{"name": "x"}))
		}))
		defer server.Close()
		if err := testClient(server).DeleteSource(context.Background(), "id1"); !DeletionUnsupported(err) {
			t.Fatalf("expected ErrDeletionUnsupported, got %v", err)
		}
	})

	t.Run("404 verification read failure surfaces", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []map[string]interface{}{{"status": "404", "title": "NotFound"}},
				})
				return
			}
			// The verification GET fails transiently — that is not proof of
			// an unsupported DELETE.
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]interface{}{{"status": "500", "title": "Internal"}},
			})
		}))
		defer server.Close()
		err := testClient(server).DeleteSource(context.Background(), "id1")
		if DeletionUnsupported(err) {
			t.Fatalf("a failed verification GET must not masquerade as unsupported deletion: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "verifying delete") {
			t.Fatalf("expected the GET failure to surface, got %v", err)
		}
	})

	t.Run("404 with gone source is success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]interface{}{{"status": "404", "title": "NotFound"}},
			})
		}))
		defer server.Close()
		if err := testClient(server).DeleteSource(context.Background(), "id1"); err != nil {
			t.Fatalf("source gone must be success, got %v", err)
		}
	})
}
