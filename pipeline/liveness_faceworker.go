package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// FaceworkerVerifier scores liveness with the IDSetu faceworker
// (apps/idsetu/faceworker): passive anti-spoof (/v1/pad), gesture challenge
// (/v1/challenge) and face embedding (/v1/embed) over HTTP with a bearer
// token. Frames and the reference portrait are base64 images.
//
// Scores are raw model output. The face match is cosine similarity of
// embeddings in [0,1] (negative values clamp to 0); it is not a probability,
// so set the node's min_score and min_match from a calibration on your own
// corpus instead of keeping the defaults.
type FaceworkerVerifier struct {
	BaseURL string
	Token   string
	Client  *http.Client
	// MaxFrames caps frames sent per verification (default 12).
	MaxFrames int
}

func (v *FaceworkerVerifier) post(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(v.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.Token)
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("faceworker %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

// VerifyLiveness implements LivenessVerifier. Evidence must carry
// "frames": a list of base64 images. A client error (no frames, no face) is
// a failed check; a transport or server error is returned as an error so the
// attempt is not charged.
func (v *FaceworkerVerifier) VerifyLiveness(ctx context.Context, req LivenessRequest) (LivenessResult, error) {
	var frames []string
	switch f := req.Evidence["frames"].(type) {
	case []string:
		frames = f
	case []any:
		for _, x := range f {
			if s, ok := x.(string); ok && s != "" {
				frames = append(frames, s)
			}
		}
	}
	if len(frames) == 0 {
		return LivenessResult{Reasons: []string{"no frames were submitted"}}, nil
	}
	limit := v.MaxFrames
	if limit <= 0 {
		limit = 12
	}
	if len(frames) > limit {
		return LivenessResult{Reasons: []string{fmt.Sprintf("at most %d frames are accepted", limit)}}, nil
	}

	var pad struct {
		Score   float64  `json:"score"`
		Reasons []string `json:"reasons"`
	}
	if err := v.post(ctx, "/v1/pad", map[string]any{"frames": frames}, &pad); err != nil {
		return LivenessResult{}, err
	}
	var ch struct {
		Results map[string]bool `json:"results"`
	}
	if err := v.post(ctx, "/v1/challenge", map[string]any{"frames": frames, "actions": req.Actions}, &ch); err != nil {
		return LivenessResult{}, err
	}
	res := LivenessResult{Live: true, Score: pad.Score, Reasons: pad.Reasons}
	for _, a := range req.Actions {
		if !ch.Results[a] {
			res.Live = false
			res.Reasons = append(res.Reasons, "challenge not performed: "+a)
		}
	}
	if ref, _ := req.Reference.(string); ref != "" {
		m, err := v.match(ctx, frames[len(frames)/2], ref)
		if err != nil {
			return LivenessResult{}, err
		}
		res.MatchScore = &m
	}
	return res, nil
}

func (v *FaceworkerVerifier) match(ctx context.Context, live, reference string) (float64, error) {
	embed := func(img string) ([]float64, error) {
		var out struct {
			Embedding []float64 `json:"embedding"`
		}
		if err := v.post(ctx, "/v1/embed", map[string]any{"image": img}, &out); err != nil {
			return nil, err
		}
		if len(out.Embedding) == 0 {
			return nil, errors.New("faceworker returned no embedding")
		}
		return out.Embedding, nil
	}
	a, err := embed(live)
	if err != nil {
		return 0, err
	}
	b, err := embed(reference)
	if err != nil {
		return 0, err
	}
	return cosine(a, b), nil
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return math.Max(0, dot/(math.Sqrt(na)*math.Sqrt(nb)))
}
