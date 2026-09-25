package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPiThinkingZeroValueAndMergedSelections(t *testing.T) {
	p := *validPiConfig().Agent.Pi
	require.Equal(t, PiThinkingMax, p.WorkerThinking())
	p.Thinking = "high"
	require.Error(t, p.Validate())
	for _, value := range []string{"medium", "max", "null", `""`, "high"} {
		raw, err := yaml.Marshal(validPiConfig().Agent.Pi)
		require.NoError(t, err)
		raw = append(raw, []byte("<<: {thinking: "+value+"}\n")...)
		var decoded PiConfig
		err = yaml.Unmarshal(raw, &decoded)
		if value == "medium" || value == "max" {
			require.NoError(t, err)
			require.Equal(t, PiThinking(value), decoded.WorkerThinking())
		} else {
			require.Error(t, err)
		}
	}
}

func TestPiThinkingConfigDefaultsOnlyWhenAbsent(t *testing.T) {
	for _, value := range []string{"", "medium", "max"} {
		label := value
		if label == "" {
			label = "default_max"
		}
		t.Run(label, func(t *testing.T) {
			raw, err := yaml.Marshal(validPiConfig().Agent.Pi)
			require.NoError(t, err)
			if value != "" {
				raw = append(raw, []byte("thinking: "+value+"\n")...)
			}
			var p PiConfig
			require.NoError(t, yaml.Unmarshal(raw, &p))
			require.NoError(t, p.Validate())
			encoded, err := json.Marshal(p)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(encoded, &fields))
			expected := value
			if expected == "" {
				expected = "max"
			}
			require.Equal(t, expected, fields["thinking"], "configuration must retain the selected value, not silently ignore it")
		})
	}
	for _, value := range []string{"null", `""`, "high", "low", "MAX", "true", "12", "[medium]", "{value: medium}"} {
		t.Run("invalid_"+value, func(t *testing.T) {
			raw, err := yaml.Marshal(validPiConfig().Agent.Pi)
			require.NoError(t, err)
			raw = append(raw, []byte("thinking: "+value+"\n")...)
			var p PiConfig
			err = yaml.Unmarshal(raw, &p)
			if err == nil {
				err = p.Validate()
			}
			require.Error(t, err, "an explicit invalid effort must not fall back to max")
		})
	}
}
