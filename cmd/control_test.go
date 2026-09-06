package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/require"
)

func TestControlRefusesDailyHomeAndUnsafeOrIncompleteRoles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.Error(t, controlHome(home), "ordinary HOME must not be adopted")
	require.NoError(t, os.Mkdir(filepath.Join(home, ".pylon"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon/control-only"), []byte("pylon-control-v1\n"), 0600))
	require.NoError(t, os.Chmod(home, 0700))
	require.NoError(t, controlHome(home))
	t.Setenv("CONTROL_SECRET", strings.Repeat("s", 64))
	t.Setenv("TELEGRAM_BOT_TOKEN", "synthetic")
	g := &config.GlobalConfig{Server: config.ServerConfig{Host: "127.0.0.1", Port: 18471}, Defaults: config.DefaultsConfig{Channel: config.ChannelDefaults{Type: "telegram", Telegram: &config.TelegramConfig{BotToken: "${TELEGRAM_BOT_TOKEN}", ChatID: 42, AllowedUsers: []int64{42}}}}}
	p := &config.PylonConfig{Name: "vendor", Trigger: config.TriggerConfig{Type: "webhook", Secret: "${CONTROL_SECRET}", SignatureHeader: "X-Pylon-Signature"}, Control: &config.ControlConfig{TopicID: "0"}}
	ch, err := controlChannel(g, p)
	require.NoError(t, err)
	require.True(t, ch.Ready())
	for _, host := range []string{"0.0.0.0", "192.0.2.1", "localhost"} {
		g.Server.Host = host
		_, err = controlChannel(g, p)
		require.Error(t, err)
	}
	g.Server.Host = "127.0.0.1"
	p.Agent = &config.PylonAgent{}
	_, err = controlChannel(g, p)
	require.Error(t, err)
	p.Agent = nil
	p.Disabled = true
	_, err = controlChannel(g, p)
	require.Error(t, err)
	p.Disabled = false
	g.Defaults.Channel.Telegram.ChatID = 0
	_, err = controlChannel(g, p)
	require.Error(t, err)
}
