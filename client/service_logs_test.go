package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestServiceLogsError(t *testing.T) {
	client := &Client{
		client: newMockClient(errorMock(http.StatusInternalServerError, "Server error")),
	}
	_, err := client.ServiceLogs(context.Background(), "service_id", container.LogsOptions{})
	assert.Check(t, is.ErrorType(err, cerrdefs.IsInternal))

	_, err = client.ServiceLogs(context.Background(), "service_id", container.LogsOptions{
		Since: "2006-01-02TZ",
	})
	assert.Check(t, is.ErrorContains(err, `parsing time "2006-01-02TZ"`))

	_, err = client.ServiceLogs(context.Background(), "", container.LogsOptions{})
	assert.Check(t, is.ErrorType(err, cerrdefs.IsInvalidArgument))
	assert.Check(t, is.ErrorContains(err, "value is empty"))

	_, err = client.ServiceLogs(context.Background(), "    ", container.LogsOptions{})
	assert.Check(t, is.ErrorType(err, cerrdefs.IsInvalidArgument))
	assert.Check(t, is.ErrorContains(err, "value is empty"))
}

func TestServiceLogs(t *testing.T) {
	expectedURL := "/services/service_id/logs"
	cases := []struct {
		options             container.LogsOptions
		expectedQueryParams map[string]string
		expectedError       string
	}{
		{
			expectedQueryParams: map[string]string{
				"tail": "",
			},
		},
		{
			options: container.LogsOptions{
				Tail: "any",
			},
			expectedQueryParams: map[string]string{
				"tail": "any",
			},
		},
		{
			options: container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Timestamps: true,
				Details:    true,
				Follow:     true,
			},
			expectedQueryParams: map[string]string{
				"tail":       "",
				"stdout":     "1",
				"stderr":     "1",
				"timestamps": "1",
				"details":    "1",
				"follow":     "1",
			},
		},
		{
			options: container.LogsOptions{
				// timestamp will be passed as is
				Since: "1136073600.000000001",
			},
			expectedQueryParams: map[string]string{
				"tail":  "",
				"since": "1136073600.000000001",
			},
		},
		{
			options: container.LogsOptions{
				// An complete invalid date will not be passed
				Since: "invalid value",
			},
			expectedError: `invalid value for "since": failed to parse value as time or duration: "invalid value"`,
		},
	}
	for _, logCase := range cases {
		client := &Client{
			client: newMockClient(func(r *http.Request) (*http.Response, error) {
				if !strings.HasPrefix(r.URL.Path, expectedURL) {
					return nil, fmt.Errorf("expected URL '%s', got '%s'", expectedURL, r.URL)
				}
				// Check query parameters
				query := r.URL.Query()
				for key, expected := range logCase.expectedQueryParams {
					actual := query.Get(key)
					if actual != expected {
						return nil, fmt.Errorf("%s not set in URL query properly. Expected '%s', got %s", key, expected, actual)
					}
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte("response"))),
				}, nil
			}),
		}
		body, err := client.ServiceLogs(context.Background(), "service_id", logCase.options)
		if logCase.expectedError != "" {
			assert.Check(t, is.Error(err, logCase.expectedError))
			continue
		}
		assert.NilError(t, err)
		defer body.Close()
		content, err := io.ReadAll(body)
		assert.NilError(t, err)
		assert.Check(t, is.Contains(string(content), "response"))
	}
}

func ExampleClient_ServiceLogs_withTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, _ := NewClientWithOpts(FromEnv)
	reader, err := client.ServiceLogs(ctx, "service_id", container.LogsOptions{})
	if err != nil {
		log.Fatal(err)
	}

	_, err = io.Copy(os.Stdout, reader)
	if err != nil && !errors.Is(err, io.EOF) {
		log.Fatal(err)
	}
}
