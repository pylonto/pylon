package notifier

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// SlackNotifier implements Notifier using Slack's Web API and Socket Mode.
type SlackNotifier struct {
	api          *slack.Client
	sm           *socketmode.Client
	channelID    string
	allowedUsers map[string]bool
	mu           sync.Mutex
	actionFn     func(jobID string, action string)
	messageFn    func(topicID string, text string, messageID string)
}

func NewSlackNotifier(ctx context.Context, botToken, appToken, channelID string, allowedUsers []string) *SlackNotifier {
	api := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	sm := socketmode.New(api)

	allowed := make(map[string]bool, len(allowedUsers))
	for _, u := range allowedUsers {
		allowed[u] = true
	}

	s := &SlackNotifier{
		api:          api,
		sm:           sm,
		channelID:    channelID,
		allowedUsers: allowed,
	}
	go s.listenSocketMode(ctx)
	return s
}

func (s *SlackNotifier) CreateTopic(name string) (string, error) {
	_, ts, err := s.api.PostMessage(s.channelID,
		slack.MsgOptionText(name, false),
	)
	if err != nil {
		log.Printf("[slack] topic creation failed: %v", err)
		return "", err
	}
	return ts, nil
}

func (s *SlackNotifier) SendMessage(topicID, text string) (string, error) {
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if topicID != "" {
		opts = append(opts, slack.MsgOptionTS(topicID))
	}
	_, ts, err := s.api.PostMessage(s.channelID, opts...)
	if err != nil {
		return "", err
	}
	return ts, nil
}

func (s *SlackNotifier) ReplyMessage(topicID, text, replyTo string) (string, error) {
	return s.SendMessage(topicID, text)
}

func (s *SlackNotifier) SendApproval(topicID, text, jobID string) (string, error) {
	investigateBtn := slack.NewButtonBlockElement(
		"investigate:"+jobID, "investigate",
		slack.NewTextBlockObject(slack.PlainTextType, "Investigate", false, false),
	).WithStyle(slack.StylePrimary)

	ignoreBtn := slack.NewButtonBlockElement(
		"ignore:"+jobID, "ignore",
		slack.NewTextBlockObject(slack.PlainTextType, "Ignore", false, false),
	).WithStyle(slack.StyleDanger)

	actionBlock := slack.NewActionBlock("approval:"+jobID, investigateBtn, ignoreBtn)
	textBlock := slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
		nil, nil,
	)

	opts := []slack.MsgOption{slack.MsgOptionBlocks(textBlock, actionBlock)}
	if topicID != "" {
		opts = append(opts, slack.MsgOptionTS(topicID))
	}

	_, ts, err := s.api.PostMessage(s.channelID, opts...)
	if err != nil {
		return "", err
	}
	return ts, nil
}

func (s *SlackNotifier) EditMessage(topicID, messageID, text string) error {
	_, _, _, err := s.api.UpdateMessage(s.channelID, messageID,
		slack.MsgOptionText(text, false),
		slack.MsgOptionBlocks(), // clear Block Kit blocks (removes stale buttons)
	)
	return err
}

func (s *SlackNotifier) SendTyping(topicID string) error {
	// Slack has no typing indicator API for bots in channels.
	return nil
}

func (s *SlackNotifier) CloseTopic(topicID string) error {
	if topicID == "" {
		return nil
	}
	return s.api.AddReaction("white_check_mark", slack.ItemRef{
		Channel:   s.channelID,
		Timestamp: topicID,
	})
}

func (s *SlackNotifier) OnAction(cb func(string, string)) {
	s.mu.Lock()
	s.actionFn = cb
	s.mu.Unlock()
}

func (s *SlackNotifier) OnMessage(cb func(string, string, string)) {
	s.mu.Lock()
	s.messageFn = cb
	s.mu.Unlock()
}

func (s *SlackNotifier) isAllowed(userID string) bool {
	return len(s.allowedUsers) == 0 || s.allowedUsers[userID]
}

func (s *SlackNotifier) listenSocketMode(ctx context.Context) {
	go func() {
		if err := s.sm.RunContext(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[slack] socket mode exited: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-s.sm.Events:
			if !ok {
				return
			}
			switch evt.Type {
			case socketmode.EventTypeConnected:
				log.Println("[slack] socket mode connected (listening for button clicks and messages)")
			case socketmode.EventTypeConnectionError:
				log.Printf("[slack] socket mode connection error: %v", evt.Data)
			case socketmode.EventTypeInvalidAuth:
				log.Println("[slack] socket mode auth failed -- check SLACK_APP_TOKEN has connections:write scope")
			}
			s.handleEvent(evt)
		}
	}
}

func (s *SlackNotifier) handleEvent(evt socketmode.Event) {
	// Always ack immediately to prevent Slack from showing a warning.
	if evt.Request != nil {
		if err := s.sm.Ack(*evt.Request); err != nil {
			log.Printf("[slack] ack failed: %v", err)
		}
	}

	switch evt.Type {
	case socketmode.EventTypeInteractive:
		callback, ok := evt.Data.(slack.InteractionCallback)
		if !ok {
			return
		}

		if !s.isAllowed(callback.User.ID) {
			return
		}

		if callback.Type == slack.InteractionTypeBlockActions {
			s.mu.Lock()
			actionFn := s.actionFn
			s.mu.Unlock()

			for _, action := range callback.ActionCallback.BlockActions {
				parts := strings.SplitN(action.ActionID, ":", 2)
				if len(parts) == 2 && actionFn != nil {
					go actionFn(parts[1], parts[0])
				}
			}
		}

	case socketmode.EventTypeEventsAPI:
		apiEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}

		if apiEvent.Type == slackevents.CallbackEvent {
			switch ev := apiEvent.InnerEvent.Data.(type) {
			case *slackevents.MessageEvent:
				if ev.BotID != "" || ev.SubType != "" {
					return
				}
				if !s.isAllowed(ev.User) {
					return
				}

				s.mu.Lock()
				messageFn := s.messageFn
				s.mu.Unlock()

				if messageFn != nil {
					go messageFn(ev.ThreadTimeStamp, ev.Text, ev.TimeStamp)
				}
			}
		}
	}
}

// ValidateSlackToken calls auth.test and returns the bot name on success.
func ValidateSlackToken(botToken string) (string, error) {
	api := slack.New(botToken)
	resp, err := api.AuthTest()
	if err != nil {
		return "", fmt.Errorf("auth.test failed: %w", err)
	}
	return resp.User, nil
}

// ListBotChannels returns channels the bot has been invited to.
func ListBotChannels(botToken string) ([]slack.Channel, error) {
	api := slack.New(botToken)
	channels, _, err := api.GetConversations(&slack.GetConversationsParameters{
		Types:           []string{"public_channel", "private_channel"},
		Limit:           100,
		ExcludeArchived: true,
	})
	if err != nil {
		return nil, fmt.Errorf("listing channels: %w", err)
	}
	return channels, nil
}

// CheckSlackAccess verifies the bot can access a given channel.
func CheckSlackAccess(botToken, channelID string) (string, error) {
	api := slack.New(botToken)
	ch, err := api.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return "", fmt.Errorf("channel %s: %w", channelID, err)
	}
	return ch.Name, nil
}
