package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPiThinkingTransportAndReceiptMustMatchExactly(t *testing.T) {
	for _, thinking := range []string{"medium", "max"} {
		t.Run(thinking, func(t *testing.T) {
			var job PiJob
			require.NoError(t, json.Unmarshal([]byte(`{"tokens":400000,"thinking":"`+thinking+`","brief":{"thinking":"not_authority"}}`), &job))
			p := NewPi(context.Background(), job, nil, nil)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/job", nil))
			require.Equal(t, http.StatusOK, w.Code)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fields))
			require.Equal(t, thinking, fields["thinking"])
			for _, reported := range []string{"", "high", "medium", "max"} {
				result := PiResult{Outcome: "executor_returned", Usage: &store.SubscriptionUsage{}, Requests: 1, Provider: "openai-codex", Model: "gpt-6-astra", Thinking: reported}
				raw, err := json.Marshal(result)
				require.NoError(t, err)
				bridge := NewPi(context.Background(), job, nil, nil)
				w := httptest.NewRecorder()
				bridge.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(string(raw))))
				if reported == thinking {
					require.Equal(t, http.StatusOK, w.Code)
					require.NotNil(t, bridge.Result())
				} else {
					require.Equal(t, http.StatusBadRequest, w.Code)
					require.Nil(t, bridge.Result())
				}
			}
		})
	}
}

func TestPiThinkingJobCannotOmitOrInventEffort(t *testing.T) {
	for _, value := range []string{"", "high", "MAX"} {
		var job PiJob
		require.NoError(t, json.Unmarshal([]byte(`{"tokens":400000,"thinking":"`+value+`"}`), &job))
		p := NewPi(context.Background(), job, nil, nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/job", nil))
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Nil(t, p.Result())
	}
}
