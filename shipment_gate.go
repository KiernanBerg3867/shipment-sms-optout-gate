package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type shipmentEvent struct {
	ID             string `json:"id"`
	ShipmentID     string `json:"shipment_id"`
	Phone          string `json:"phone"`
	ScheduledSMSID string `json:"scheduled_sms_id"`
	Kind           string `json:"kind"`
	ProofPath      string `json:"proof_path"`
	Exception      string `json:"exception"`
}

type apiEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code string `json:"code"`
		Hint string `json:"hint"`
	} `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiError struct {
	Status int
	Code   string
	Hint   string
}

func (e *apiError) Error() string { return fmt.Sprintf("sms cancellation: %s: %s", e.Code, e.Hint) }

type smsClient struct {
	baseURL string
	key     string
	http    *http.Client
}

func (c smsClient) cancel(ctx context.Context, smsID, eventID string) error {
	path := "/v1/sms/cancel/" + url.PathEscape(smsID)
	body, err := json.Marshal(struct {
		MessageID      string `json:"message_id"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}{MessageID: smsID, IdempotencyKey: eventID})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			return err
		}
		var env apiEnvelope
		err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&env)
		res.Body.Close()
		if err != nil {
			return fmt.Errorf("decode sms response: %w", err)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := time.Duration(1<<attempt) * time.Second
			if seconds, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && seconds >= 0 {
				delay = time.Duration(seconds) * time.Second
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if !env.OK {
			return &apiError{res.StatusCode, env.Error.Code, env.Error.Hint}
		}
		if res.StatusCode >= 400 {
			return fmt.Errorf("sms cancellation HTTP %d", res.StatusCode)
		}
		return nil
	}
	return errors.New("sms cancellation retry budget exhausted")
}

type gate struct {
	mu       sync.RWMutex
	optedOut map[string]bool
	listPath string
	sms      smsClient
}

func newGate(listPath string, sms smsClient) (*gate, error) {
	g := &gate{optedOut: make(map[string]bool), listPath: listPath, sms: sms}
	b, err := os.ReadFile(listPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, phone := range strings.Split(string(b), "\n") {
		if phone != "" {
			g.optedOut[phone] = true
		}
	}
	return g, nil
}

func (g *gate) optOut(phone string) error {
	if phone == "" || strings.ContainsAny(phone, "\r\n") {
		return errors.New("valid phone required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.optedOut[phone] {
		return nil
	}
	f, err := os.OpenFile(g.listPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(phone + "\n"); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	g.optedOut[phone] = true
	return nil
}

func (g *gate) handle(ctx context.Context, event shipmentEvent) (string, error) {
	if event.ID == "" || event.ShipmentID == "" || event.Phone == "" || event.ScheduledSMSID == "" {
		return "", errors.New("id, shipment_id, phone and scheduled_sms_id required")
	}
	if event.Kind != "delivered" && event.Kind != "exception" {
		return "", errors.New("kind must be delivered or exception")
	}
	g.mu.RLock()
	suppressed := g.optedOut[event.Phone]
	g.mu.RUnlock()
	if event.Kind == "exception" && event.Exception == "" {
		return "", errors.New("exception required")
	}
	if event.Kind == "delivered" && !suppressed {
		if event.ProofPath == "" {
			return "", errors.New("proof_path required")
		}
		info, err := os.Stat(filepath.Clean(event.ProofPath))
		if err != nil || info.IsDir() {
			return "", errors.New("proof_path must name an existing file")
		}
	}
	if suppressed || event.Kind == "exception" {
		if err := g.sms.cancel(ctx, event.ScheduledSMSID, event.ID); err != nil {
			return "", err
		}
		return "cancelled", nil
	}
	return "scheduled", nil
}

func (g *gate) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /opt-outs", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Phone string `json:"phone"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&input) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if err := g.optOut(input.Phone); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "suppressed"})
	})
	mux.HandleFunc("POST /shipment-events", func(w http.ResponseWriter, r *http.Request) {
		var event shipmentEvent
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&event) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		state, err := g.handle(r.Context(), event)
		if err != nil {
			status := 400
			var upstream *apiError
			if errors.As(err, &upstream) {
				status = 502
				if upstream.Status >= 400 && upstream.Status < 500 {
					status = upstream.Status
				}
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, 200, map[string]string{"shipment_id": event.ShipmentID, "sms_status": state})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
