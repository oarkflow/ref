package httpsms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/oarkflow/ref/platform/spi"
)

// ProviderHandler abstracts the provider-specific HTTP request construction and response parsing.
type ProviderHandler interface {
	BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error)
	ParseResponse(resp *http.Response, body []byte) (msgID string, smsErr *spi.SMSError, err error)
}

// GetProviderHandler returns the ProviderHandler for the configured provider type.
func GetProviderHandler(provider string) ProviderHandler {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "twilio":
		return &TwilioHandler{}
	case "vonage", "nexmo":
		return &VonageHandler{}
	case "messagebird":
		return &MessageBirdHandler{}
	case "infobip":
		return &InfobipHandler{}
	case "aws_sns", "sns":
		return &AWSSNSHandler{}
	default:
		return &CustomHandler{}
	}
}

// ---------------------------------------------------------------------------
// Twilio
// ---------------------------------------------------------------------------

type TwilioHandler struct{}

func (h *TwilioHandler) BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.twilio.com/2010-04-01"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	reqURL := fmt.Sprintf("%s/Accounts/%s/Messages.json", baseURL, cfg.AccountSID)

	from := msg.From
	if from == "" {
		from = cfg.From
	}

	form := url.Values{}
	form.Set("To", msg.To)
	form.Set("From", from)
	form.Set("Body", msg.Text)
	if msg.DLR && cfg.CallbackURL != "" {
		form.Set("StatusCallback", cfg.CallbackURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(cfg.AccountSID, cfg.AuthToken)
	return req, nil
}

func (h *TwilioHandler) ParseResponse(resp *http.Response, body []byte) (string, *spi.SMSError, error) {
	var twResp struct {
		SID     string `json:"sid"`
		Status  string `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &twResp)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return twResp.SID, nil, nil
	}

	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return "", &spi.SMSError{
		Kind:       "http",
		Protocol:   "http",
		StatusCode: resp.StatusCode,
		Code:       fmt.Sprintf("%d", twResp.Code),
		Message:    twResp.Message,
		Retryable:  retryable,
	}, nil
}

// ---------------------------------------------------------------------------
// Vonage / Nexmo
// ---------------------------------------------------------------------------

type VonageHandler struct{}

func (h *VonageHandler) BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://rest.nexmo.com"
	}
	reqURL := strings.TrimSuffix(baseURL, "/") + "/sms/json"

	from := msg.From
	if from == "" {
		from = cfg.From
	}

	payload := map[string]any{
		"api_key":    cfg.APIKey,
		"api_secret": cfg.APISecret,
		"to":         msg.To,
		"from":       from,
		"text":       msg.Text,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (h *VonageHandler) ParseResponse(resp *http.Response, body []byte) (string, *spi.SMSError, error) {
	var vResp struct {
		MessageCount string `json:"message-count"`
		Messages     []struct {
			To           string `json:"to"`
			MessageID    string `json:"message-id"`
			Status       string `json:"status"`
			ErrorText    string `json:"error-text"`
			RemainingBal string `json:"remaining-balance"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &vResp)

	if len(vResp.Messages) > 0 {
		m := vResp.Messages[0]
		if m.Status == "0" {
			return m.MessageID, nil, nil
		}
		retryable := m.Status == "1" || m.Status == "5" // throttled or internal error
		return "", &spi.SMSError{
			Kind:       "provider",
			Protocol:   "http",
			StatusCode: resp.StatusCode,
			Code:       m.Status,
			Message:    m.ErrorText,
			Retryable:  retryable,
		}, nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "", nil, nil
	}

	return "", &spi.SMSError{
		Kind:       "http",
		Protocol:   "http",
		StatusCode: resp.StatusCode,
		Message:    string(body),
		Retryable:  resp.StatusCode >= 500,
	}, nil
}

// ---------------------------------------------------------------------------
// MessageBird
// ---------------------------------------------------------------------------

type MessageBirdHandler struct{}

func (h *MessageBirdHandler) BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://rest.messagebird.com"
	}
	reqURL := strings.TrimSuffix(baseURL, "/") + "/messages"

	from := msg.From
	if from == "" {
		from = cfg.From
	}

	payload := map[string]any{
		"recipients": []string{msg.To},
		"originator": from,
		"body":       msg.Text,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = cfg.AuthToken
	}
	req.Header.Set("Authorization", "AccessKey "+apiKey)
	return req, nil
}

func (h *MessageBirdHandler) ParseResponse(resp *http.Response, body []byte) (string, *spi.SMSError, error) {
	var mbResp struct {
		ID     string `json:"id"`
		Errors []struct {
			Code        int    `json:"code"`
			Description string `json:"description"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(body, &mbResp)

	if len(mbResp.Errors) > 0 {
		e := mbResp.Errors[0]
		return "", &spi.SMSError{
			Kind:       "provider",
			Protocol:   "http",
			StatusCode: resp.StatusCode,
			Code:       fmt.Sprintf("%d", e.Code),
			Message:    e.Description,
			Retryable:  e.Code == 9 || resp.StatusCode >= 500,
		}, nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return mbResp.ID, nil, nil
	}

	return "", &spi.SMSError{
		Kind:       "http",
		Protocol:   "http",
		StatusCode: resp.StatusCode,
		Message:    string(body),
		Retryable:  resp.StatusCode >= 500,
	}, nil
}

// ---------------------------------------------------------------------------
// Infobip
// ---------------------------------------------------------------------------

type InfobipHandler struct{}

func (h *InfobipHandler) BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		return nil, fmt.Errorf("infobip requires base_url (e.g. https://xxxx.api.infobip.com)")
	}
	reqURL := strings.TrimSuffix(baseURL, "/") + "/sms/2/text/advanced"

	from := msg.From
	if from == "" {
		from = cfg.From
	}

	payload := map[string]any{
		"messages": []map[string]any{
			{
				"from": from,
				"destinations": []map[string]string{
					{"to": msg.To},
				},
				"text": msg.Text,
			},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = cfg.AuthToken
	}
	req.Header.Set("Authorization", "App "+apiKey)
	return req, nil
}

func (h *InfobipHandler) ParseResponse(resp *http.Response, body []byte) (string, *spi.SMSError, error) {
	var ibResp struct {
		Messages []struct {
			MessageID string `json:"messageId"`
			Status    struct {
				GroupID     int    `json:"groupId"`
				GroupName   string `json:"groupName"`
				ID          int    `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"status"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &ibResp)

	if len(ibResp.Messages) > 0 {
		m := ibResp.Messages[0]
		// GroupID 1 = PENDING, 3 = DELIVERED
		if m.Status.GroupID <= 3 {
			return m.MessageID, nil, nil
		}
		return "", &spi.SMSError{
			Kind:       "provider",
			Protocol:   "http",
			StatusCode: resp.StatusCode,
			Code:       m.Status.Name,
			Message:    m.Status.Description,
			Retryable:  m.Status.GroupID == 5 || resp.StatusCode >= 500, // 5 = REJECTED/TRANSIENT
		}, nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "", nil, nil
	}

	return "", &spi.SMSError{
		Kind:       "http",
		Protocol:   "http",
		StatusCode: resp.StatusCode,
		Message:    string(body),
		Retryable:  resp.StatusCode >= 500,
	}, nil
}

// ---------------------------------------------------------------------------
// AWS SNS Handler
// ---------------------------------------------------------------------------

type AWSSNSHandler struct{}

func (h *AWSSNSHandler) BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error) {
	reqURL := cfg.BaseURL
	if reqURL == "" {
		reqURL = cfg.SubmitURL
	}
	if reqURL == "" {
		return nil, fmt.Errorf("aws_sns requires base_url or submit_url")
	}

	form := url.Values{}
	form.Set("Action", "Publish")
	form.Set("PhoneNumber", msg.To)
	form.Set("Message", msg.Text)
	if cfg.From != "" {
		form.Set("MessageAttributes.entry.1.Name", "AWS.SNS.SMS.SenderID")
		form.Set("MessageAttributes.entry.1.Value.DataType", "String")
		form.Set("MessageAttributes.entry.1.Value.StringValue", cfg.From)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cfg.AuthToken != "" {
		req.Header.Set("Authorization", cfg.AuthToken)
	}
	return req, nil
}

func (h *AWSSNSHandler) ParseResponse(resp *http.Response, body []byte) (string, *spi.SMSError, error) {
	s := string(body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Look for <MessageId>...</MessageId>
		start := strings.Index(s, "<MessageId>")
		end := strings.Index(s, "</MessageId>")
		if start >= 0 && end > start {
			return s[start+11 : end], nil, nil
		}
		return "sns-published", nil, nil
	}

	return "", &spi.SMSError{
		Kind:       "http",
		Protocol:   "http",
		StatusCode: resp.StatusCode,
		Message:    s,
		Retryable:  resp.StatusCode >= 500,
	}, nil
}

// ---------------------------------------------------------------------------
// Custom Generic HTTP Handler
// ---------------------------------------------------------------------------

type CustomHandler struct{}

func (h *CustomHandler) BuildRequest(ctx context.Context, cfg Config, msg spi.SMSMessage) (*http.Request, error) {
	reqURL := cfg.SubmitURL
	if reqURL == "" {
		reqURL = cfg.BaseURL
	}
	if reqURL == "" {
		return nil, fmt.Errorf("custom httpsms requires submit_url or base_url")
	}

	method := strings.ToUpper(cfg.Method)
	if method == "" {
		method = http.MethodPost
	}

	from := msg.From
	if from == "" {
		from = cfg.From
	}

	toField := cfg.ToField
	if toField == "" {
		toField = "to"
	}
	fromField := cfg.FromField
	if fromField == "" {
		fromField = "from"
	}
	textField := cfg.TextField
	if textField == "" {
		textField = "text"
	}

	var bodyReader io.Reader
	contentType := "application/json"

	if strings.EqualFold(cfg.BodyType, "form") {
		contentType = "application/x-www-form-urlencoded"
		form := url.Values{}
		form.Set(toField, msg.To)
		form.Set(fromField, from)
		form.Set(textField, msg.Text)
		for k, v := range msg.Tags {
			form.Set(k, v)
		}
		bodyReader = strings.NewReader(form.Encode())
	} else {
		payload := map[string]any{
			toField:   msg.To,
			fromField: from,
			textField: msg.Text,
		}
		for k, v := range msg.Tags {
			payload[k] = v
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)

	if cfg.AccountSID != "" && cfg.AuthToken != "" {
		req.SetBasicAuth(cfg.AccountSID, cfg.AuthToken)
	} else if cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	} else if cfg.APIKey != "" {
		req.Header.Set("X-API-Key", cfg.APIKey)
	}

	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

func (h *CustomHandler) ParseResponse(resp *http.Response, body []byte) (string, *spi.SMSError, error) {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var generic map[string]any
		if err := json.Unmarshal(body, &generic); err == nil {
			for _, k := range []string{"id", "message_id", "messageId", "msg_id", "sid"} {
				if val, ok := generic[k]; ok {
					return fmt.Sprintf("%v", val), nil, nil
				}
			}
		}
		return "ack", nil, nil
	}

	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return "", &spi.SMSError{
		Kind:       "http",
		Protocol:   "http",
		StatusCode: resp.StatusCode,
		Message:    string(body),
		Retryable:  retryable,
	}, nil
}
