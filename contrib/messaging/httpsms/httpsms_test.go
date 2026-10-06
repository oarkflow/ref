package httpsms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oarkflow/ref/platform/spi"
)

func TestTwilioHandler(t *testing.T) {
	h := &TwilioHandler{}
	cfg := Config{
		AccountSID: "AC123",
		AuthToken:  "token123",
		From:       "+15005550006",
		BaseURL:    "https://api.twilio.com/2010-04-01",
	}
	msg := spi.SMSMessage{
		To:   "+15551234567",
		Text: "Hello from Twilio test",
	}

	req, err := h.BuildRequest(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("build request failed: %v", err)
	}

	user, pass, ok := req.BasicAuth()
	if !ok || user != "AC123" || pass != "token123" {
		t.Fatalf("bad basic auth: user=%s, pass=%s", user, pass)
	}

	// Test success response
	respSuccess := &http.Response{StatusCode: 201}
	msgID, smsErr, err := h.ParseResponse(respSuccess, []byte(`{"sid":"SM12345","status":"queued"}`))
	if err != nil || smsErr != nil || msgID != "SM12345" {
		t.Fatalf("unexpected success parse: id=%s, smsErr=%v, err=%v", msgID, smsErr, err)
	}

	// Test failure response
	respFail := &http.Response{StatusCode: 400}
	_, smsErr, err = h.ParseResponse(respFail, []byte(`{"code":21211,"message":"Invalid 'To' Phone Number"}`))
	if err != nil || smsErr == nil || smsErr.Code != "21211" {
		t.Fatalf("expected 21211 code, got: %v", smsErr)
	}
}

func TestVonageHandler(t *testing.T) {
	h := &VonageHandler{}
	cfg := Config{
		APIKey:    "test_key",
		APISecret: "test_secret",
		From:      "VonageTest",
	}
	msg := spi.SMSMessage{
		To:   "+15551234567",
		Text: "Hello from Vonage test",
	}

	req, err := h.BuildRequest(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("build request failed: %v", err)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json, got %s", req.Header.Get("Content-Type"))
	}

	// Test success
	respSuccess := &http.Response{StatusCode: 200}
	msgID, smsErr, err := h.ParseResponse(respSuccess, []byte(`{"messages":[{"status":"0","message-id":"VON123"}]}`))
	if err != nil || smsErr != nil || msgID != "VON123" {
		t.Fatalf("unexpected response: id=%s, smsErr=%v, err=%v", msgID, smsErr, err)
	}

	// Test throttled failure
	_, smsErr, err = h.ParseResponse(respSuccess, []byte(`{"messages":[{"status":"1","error-text":"Throttled"}]}`))
	if smsErr == nil || !smsErr.Retryable {
		t.Fatalf("expected retryable throttled error, got: %v", smsErr)
	}
}

func TestClientSubmitWithHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "msg_999",
			"status": "sent",
		})
	}))
	defer srv.Close()

	client := &Client{
		name: "test_custom_gateway",
		cfg: Config{
			Provider:  "custom",
			SubmitURL: srv.URL + "/send",
			APIKey:    "secret",
			From:      "MySystem",
		},
		handler:    &CustomHandler{},
		httpClient: srv.Client(),
		tracker:    newHealthTracker(10),
	}

	res, err := client.Submit(context.Background(), spi.SMSMessage{
		To:   "+9779800000000",
		Text: "Test dynamic submit",
	})
	if err != nil {
		t.Fatalf("submit returned unexpected Go error: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected OK true, got error: %+v", res.Error)
	}
	if res.ProviderMsgID != "msg_999" {
		t.Fatalf("expected id msg_999, got %s", res.ProviderMsgID)
	}

	h := client.Health(context.Background())
	if !h.Available || h.SuccessRate != 1.0 {
		t.Fatalf("expected healthy report, got %+v", h)
	}
}
