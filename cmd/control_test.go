package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/require"
)

// This fence consumes Ciao's actual generator, not a second hand-written YAML fixture.
func TestCiaoGeneratedControlConfigurationLoads(t *testing.T) {
	repo := os.Getenv("CIAO_MAINTENANCE_REPO")
	if repo == "" {
		t.Skip("set CIAO_MAINTENANCE_REPO for the cross-language config loader fence")
	}
	root := t.TempDir()
	script := `import sys
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,sys.argv[1]+"/scripts")
from vendor_maintenance_lib.notification_service import private_config
from vendor_maintenance_lib.safety import private_directory,atomic_write
from vendor_maintenance_lib.catalog import canonical
root=Path(sys.argv[2]); state=private_directory(root/"state")
atomic_write(state/"telegram-owner.json",canonical({"bot_id":123456,"chat_id":42,"allowed_users":[42],"dedicated_owner_confirmed":True}).encode())
secrets=private_directory(root/".config/ciao-maintenance/secrets")
atomic_write(secrets/"telegram-bot-token",("123456:"+"Z"*35).encode())
with patch("vendor_maintenance_lib.notification_service.Path.home",return_value=root):
 private_config({"pylon_home":str(root/"control-home"),"port":18471},{"state":str(state)})
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-c", script, repo, root)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Setenv("HOME", filepath.Join(root, "control-home"))
	t.Setenv("TELEGRAM_BOT_TOKEN", "123456:"+strings.Repeat("Z", 35))
	t.Setenv("CIAO_CONTROL_SECRET", strings.Repeat("a", 64))
	global, err := config.LoadGlobal()
	require.NoError(t, err)
	pyl, err := config.LoadPylon("vendor-maintenance")
	require.NoError(t, err)
	require.Nil(t, pyl.Agent)
	ch, err := controlChannel(global, pyl)
	require.NoError(t, err)
	require.True(t, ch.Ready()) // No network, poller, executor or model.
}

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
