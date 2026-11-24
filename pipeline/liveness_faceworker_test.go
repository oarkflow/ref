package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeFaceworker(t *testing.T, pad float64, performed map[string]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/pad":
			_ = json.NewEncoder(w).Encode(map[string]any{"score": pad})
		case "/v1/challenge":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": performed})
		case "/v1/embed":
			emb := []float64{1, 0, 0}
			if body["image"] == "other" {
				emb = []float64{0, 1, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": emb})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFaceworkerVerifier(t *testing.T) {
	srv := fakeFaceworker(t, 0.93, map[string]bool{"blink": true, "smile": true})
	defer srv.Close()
	v := &FaceworkerVerifier{BaseURL: srv.URL, Token: "secret"}
	ctx := context.Background()
	req := LivenessRequest{Actions: []string{"blink", "smile"}, Evidence: map[string]any{"frames": []any{"a", "b", "c"}}, Reference: "a"}

	res, err := v.VerifyLiveness(ctx, req)
	if err != nil || !res.Live || res.Score != 0.93 || res.MatchScore == nil || *res.MatchScore != 1 {
		t.Fatalf("same face should pass: %+v %v", res, err)
	}
	req.Reference = "other"
	if res, _ = v.VerifyLiveness(ctx, req); res.MatchScore == nil || *res.MatchScore != 0 {
		t.Fatalf("different face must score 0, got %+v", res.MatchScore)
	}
	req.Actions = []string{"blink", "nod"}
	if res, _ = v.VerifyLiveness(ctx, req); res.Live {
		t.Fatal("an unperformed gesture must fail liveness")
	}
	if res, _ = v.VerifyLiveness(ctx, LivenessRequest{Evidence: map[string]any{}}); res.Live || len(res.Reasons) == 0 {
		t.Fatal("no frames is a failed check, not an error")
	}
	bad := &FaceworkerVerifier{BaseURL: srv.URL, Token: "wrong"}
	if _, err := bad.VerifyLiveness(ctx, req); err == nil {
		t.Fatal("a server error must surface as an error so the attempt is refunded")
	}
}

func TestLivenessCaptureAssetIsEmbedded(t *testing.T) {
	if !bytes.Contains(LivenessCaptureJS, []byte("export async function captureLiveness")) {
		t.Fatal("the capture module is not embedded")
	}
}
