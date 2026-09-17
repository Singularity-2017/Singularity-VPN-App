package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config configures a Client. The zero value of every field except Base_url
// is a reasonable default.
type Config struct {
	Base_url    string                                           // required; trailing slash tolerated
	Http_client *http.Client                                     // nil means a client with sane transport defaults
	Timeout     time.Duration                                    // per attempt; zero means 15s
	Retries     int                                              // extra attempts after the first; zero means 2
	Sleep       func(ctx context.Context, d time.Duration) error // nil means real

	// Mirror selection belongs here in the design, but the update channels
	// that need it are stage 6 work; a mirror list this client cannot yet
	// receive would be speculation, so it is not modeled yet.
}

// Client talks to one control plane. It is safe for concurrent use.
type Client struct {
	base_url    string
	http_client *http.Client
	timeout     time.Duration
	retries     int
	sleep       func(ctx context.Context, d time.Duration) error
}

// New builds a Client, refusing a Base_url that is empty or does not parse
// into an absolute URL -- a client with nowhere to send requests is a
// configuration mistake caught here, not at the first request.
func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.Base_url) == "" {
		return nil, fmt.Errorf("api: base url is required")
	}
	parsed, err := url.Parse(config.Base_url)
	if err != nil {
		return nil, fmt.Errorf("api: base url does not parse: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("api: base url %q is not absolute", config.Base_url)
	}

	http_client := config.Http_client
	if http_client == nil {
		http_client = &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				MaxIdleConns:    10,
				IdleConnTimeout: 90 * time.Second,
			},
		}
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	retries := config.Retries
	if retries == 0 {
		retries = 2
	}

	sleep := config.Sleep
	if sleep == nil {
		sleep = real_sleep
	}

	return &Client{
		base_url:    strings.TrimRight(parsed.String(), "/"),
		http_client: http_client,
		timeout:     timeout,
		retries:     retries,
		sleep:       sleep,
	}, nil
}

// real_sleep is the default Sleep: it waits out d in wall-clock time unless
// ctx ends first.
func real_sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// is_retryable_status reports whether a response with this status is worth
// asking again: a 429 or a 5xx may clear on its own, a 4xx will not.
func is_retryable_status(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// envelope is the shape every control plane response has, decoded once in
// one place so no caller reaches into raw JSON.
type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *envelope_error `json:"error"`
}

type envelope_error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// build_error turns a non-success response into the one error type callers
// see. A body that did not decode still produces an *Error carrying the
// status, never a partly filled model.
func build_error(status int, env envelope, decode_err error) *Error {
	api_err := &Error{Status: status}
	if decode_err == nil && env.Error != nil {
		api_err.Code = env.Error.Code
		api_err.Message = env.Error.Message
	}
	if api_err.Message == "" {
		if decode_err != nil {
			api_err.Message = "control plane returned a body that is not valid JSON"
		} else {
			api_err.Message = http.StatusText(status)
		}
	}
	return api_err
}

// do sends one logical request, retrying it according to allow_retry and the
// client's configuration. token is sent as a Bearer header when non-empty;
// body, when non-nil, is marshaled as the JSON request body; out, when
// non-nil, receives the decoded "data" field of a successful response.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, token string, body any, out any, allow_retry bool) error {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("api: encoding request body: %w", err)
		}
		payload = encoded
	}

	full_url := c.base_url + path
	if len(query) > 0 {
		full_url += "?" + query.Encode()
	}

	attempts := 1 + c.retries
	if !allow_retry {
		attempts = 1
	}

	delay := 500 * time.Millisecond
	var last_err error

	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		attempt_ctx, cancel := context.WithTimeout(ctx, c.timeout)

		var body_reader io.Reader
		if payload != nil {
			body_reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(attempt_ctx, method, full_url, body_reader)
		if err != nil {
			cancel()
			return fmt.Errorf("api: building request: %w", err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := c.http_client.Do(req)
		if err != nil {
			cancel()
			// The caller's own context, not just this attempt's deadline,
			// ending is not something a retry can fix, and backing off
			// would only delay reporting it.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !allow_retry || attempt == attempts-1 {
				return err
			}
			last_err = err
			if sleep_err := c.sleep(ctx, delay); sleep_err != nil {
				return sleep_err
			}
			delay *= 2
			continue
		}

		raw, read_err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		var env envelope
		var decode_err error
		if read_err != nil {
			decode_err = read_err
		} else {
			decode_err = json.Unmarshal(raw, &env)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 && decode_err == nil && env.Success {
			if out != nil && len(env.Data) > 0 {
				if err := json.Unmarshal(env.Data, out); err != nil {
					return fmt.Errorf("api: decoding response data: %w", err)
				}
			}
			return nil
		}

		api_err := build_error(resp.StatusCode, env, decode_err)
		if is_retryable_status(resp.StatusCode) && allow_retry && attempt != attempts-1 {
			last_err = api_err
			if sleep_err := c.sleep(ctx, delay); sleep_err != nil {
				return sleep_err
			}
			delay *= 2
			continue
		}
		return api_err
	}

	return last_err
}
