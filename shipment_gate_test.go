package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestShipmentDecision(t *testing.T) {
	for _, tc := range []struct {
		name, kind, exception      string
		optedOut, proof, cancelled bool
		want                       string
	}{
		{"opted-out delivery", "delivered", "", true, false, true, "cancelled"},
		{"delivery with proof", "delivered", "", false, true, false, "scheduled"},
		{"shipment exception", "exception", "address mismatch", false, false, true, "cancelled"},
		{"missing proof", "delivered", "", false, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/sms/cancel/sms-7" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected cancellation request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					MessageID      string `json:"message_id"`
					IdempotencyKey string `json:"idempotency_key"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MessageID != "sms-7" || body.IdempotencyKey != "event-7" {
					t.Errorf("unexpected cancellation body: %+v (error: %v)", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"data":{},"metadata":{}}`))
			}))
			defer server.Close()
			dir := t.TempDir()
			g, err := newGate(filepath.Join(dir, "opt-outs.txt"), smsClient{baseURL: server.URL, key: "test-key", http: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if tc.optedOut {
				if err := g.optOut("+15551234567"); err != nil {
					t.Fatal(err)
				}
			}
			event := shipmentEvent{ID: "event-7", ShipmentID: "ship-7", Phone: "+15551234567", ScheduledSMSID: "sms-7", Kind: tc.kind, Exception: tc.exception}
			if tc.proof {
				event.ProofPath = filepath.Join(dir, "pod.txt")
				if err := os.WriteFile(event.ProofPath, []byte("signed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := g.handle(context.Background(), event)
			if (err != nil) != (tc.want == "") || got != tc.want {
				t.Fatalf("state=%q error=%v, want %q", got, err, tc.want)
			}
			wantCalls := 0
			if tc.cancelled {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("cancel calls=%d, want %d", calls, wantCalls)
			}
		})
	}
}
