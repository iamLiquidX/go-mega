package mega

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HTTPError reports a status without leaking a URL, session, or public key.
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("MEGA HTTP status %d", e.StatusCode) }

func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("MEGA transport: %w", ue.Err)
	}
	return err
}

func validateTransferURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return EBADRESP
	}
	return nil
}

func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, EAGAIN) || errors.Is(err, ERATELIMIT) || errors.Is(err, ETEMPUNAVAIL) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.StatusCode == 408 || he.StatusCode == 429 || he.StatusCode >= 500
	}
	var ne interface{ Timeout() bool }
	return errors.As(err, &ne) || errors.Is(err, io.EOF)
}

func waitRetry(ctx context.Context, attempt int) error {
	delay := minSleepTime
	for i := 1; i < attempt && delay < maxSleepTime; i++ {
		delay *= 2
	}
	if delay > maxSleepTime {
		delay = maxSleepTime
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// apiRequest serializes API sequence IDs while allowing queued callers to cancel.
// folder is a public-folder handle; sid always remains the account session.
func (m *Mega) apiRequest(ctx context.Context, body []byte, folder string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case m.apiGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { m.sn++; <-m.apiGate }()
	endpoint, err := url.Parse(m.baseurl + "/cs")
	if err != nil {
		return nil, EARGS
	}
	q := endpoint.Query()
	q.Set("id", strconv.FormatInt(m.sn, 10))
	if m.sid != "" {
		q.Set("sid", m.sid)
	}
	if folder != "" {
		q.Set("n", folder)
	}
	endpoint.RawQuery = q.Encode()
	var last error
	var token, cash string
	for attempt := 0; attempt <= m.retries; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			if err = waitRetry(ctx, attempt); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return nil, EARGS
		}
		req.Header.Set("Content-Type", "application/json")
		addRequestHeaders(req)
		if cash != "" {
			addHashCashRequestHeaders(req, token, cash)
		}
		resp, err := m.client.Do(req)
		if err != nil {
			last = transportError(err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode == 402 {
			challenge := resp.Header.Get("X-Hashcash")
			resp.Body.Close()
			ease, t, ok := parseHashcash(challenge)
			if !ok {
				return nil, EBADRESP
			}
			cash, err = solveHashCashChallengeContext(ctx, t, ease, HASHCASH_CHALLENGE_TIMEOUT, 1)
			if err != nil {
				return nil, err
			}
			token = t
			last = &HTTPError{402}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			last = &HTTPError{resp.StatusCode}
			if !retryable(last) {
				return nil, last
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			last = transportError(readErr)
			if errors.Is(last, EBADRESP) {
				return nil, last
			}
			continue
		}
		if closeErr != nil {
			last = transportError(closeErr)
			continue
		}
		if len(data) > 64<<20 {
			return nil, EBADRESP
		}
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil || len(values) != 1 {
			var code ErrorMsg
			if json.Unmarshal(data, &code) == nil && code < 0 {
				last = parseError(code)
				if retryable(last) {
					continue
				}
				return nil, last
			}
			return nil, EBADRESP
		}
		var code ErrorMsg
		if json.Unmarshal(values[0], &code) == nil && code < 0 {
			last = parseError(code)
			if retryable(last) {
				continue
			}
			return nil, last
		}
		return data, nil
	}
	if last == nil {
		last = EBADRESP
	}
	return nil, last
}

// transferRequest bounds reads, retries short/interrupted bodies, and never logs URLs.
func (m *Mega) transferRequest(ctx context.Context, method, endpoint string, body []byte, size int64, exact bool) ([]byte, error) {
	if err := validateTransferURL(endpoint); err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt <= m.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			if err := waitRetry(ctx, attempt); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, EARGS
		}
		resp, err := m.client.Do(req)
		if err != nil {
			last = transportError(err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode != 200 && !(method == "GET" && resp.StatusCode == 206) {
			resp.Body.Close()
			switch resp.StatusCode {
			case 509:
				return nil, EOVERQUOTA
			case 403, 404, 410:
				return nil, EEXPIRED
			}
			last = &HTTPError{resp.StatusCode}
			if !retryable(last) {
				return nil, last
			}
			continue
		}
		var data []byte
		var readErr error
		if exact {
			// Chunk lengths are known and bounded (at most 1 MiB). Read directly
			// into the final buffer instead of growing ReadAll's buffer repeatedly.
			data = make([]byte, int(size))
			_, readErr = io.ReadFull(resp.Body, data)
			if readErr == nil {
				var extra [1]byte
				n, tailErr := io.ReadFull(resp.Body, extra[:])
				if n != 0 {
					readErr = EBADRESP
				} else if tailErr != io.EOF {
					readErr = tailErr
				}
			}
		} else {
			data, readErr = io.ReadAll(io.LimitReader(resp.Body, size+1))
		}
		closeErr := resp.Body.Close()
		if readErr != nil {
			last = transportError(readErr)
			continue
		}
		if closeErr != nil {
			last = transportError(closeErr)
			continue
		}
		if exact && int64(len(data)) < size {
			last = io.ErrUnexpectedEOF
			continue
		}
		if int64(len(data)) > size {
			return nil, EBADRESP
		}
		return data, nil
	}
	if last == nil {
		last = EBADRESP
	}
	return nil, last
}

// openTransfer starts one streamed GET. The caller owns the body and cancel and
// bounds stalls itself; the client's whole-request Timeout does not apply here.
func (m *Mega) openTransfer(ctx context.Context, endpoint string) (*http.Response, context.CancelFunc, error) {
	if err := validateTransferURL(endpoint); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		cancel()
		return nil, nil, EARGS
	}
	resp, err := (&http.Client{Transport: m.client.Transport}).Do(req)
	if err != nil {
		cancel()
		return nil, nil, transportError(err)
	}
	if resp.StatusCode == 200 || resp.StatusCode == 206 {
		return resp, cancel, nil
	}
	resp.Body.Close()
	cancel()
	switch resp.StatusCode {
	case 509:
		return nil, nil, EOVERQUOTA
	case 403, 404, 410:
		return nil, nil, EEXPIRED
	}
	return nil, nil, &HTTPError{resp.StatusCode}
}

// LoginContext authenticates without fetching a filesystem or starting event pollers.
// Authenticate once before sharing the client between concurrent operations.
func (m *Mega) LoginContext(ctx context.Context, email, password string) error {
	return m.MultiFactorLoginContext(ctx, email, password, "")
}

// MultiFactorLoginContext is LoginContext with an optional one-time MFA code.
func (m *Mega) MultiFactorLoginContext(ctx context.Context, email, password, mfa string) error {
	if err := m.preloginContext(ctx, email); err != nil {
		return err
	}
	return m.loginContext(ctx, email, password, mfa)
}

// Close releases idle HTTP connections. Context-aware operations start no poller.
// Cancel active operation contexts before closing the client.
func (m *Mega) Close() { m.client.CloseIdleConnections() }
