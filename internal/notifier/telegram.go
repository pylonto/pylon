package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TelegramNotifier implements Notifier using the Telegram Bot API.
type TelegramNotifier struct {
	token        string
	chatID       int64
	allowedUsers map[int64]bool
	client       *http.Client
	mu           sync.Mutex
	actionFn     func(jobID string, action string)
	messageFn    func(topicID string, text string)
}

func NewTelegramNotifier(ctx context.Context, token string, chatID int64, allowedUsers []int64) *TelegramNotifier {
	allowed := make(map[int64]bool, len(allowedUsers))
	for _, id := range allowedUsers {
		allowed[id] = true
	}
	t := &TelegramNotifier{
		token: token, chatID: chatID, allowedUsers: allowed,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	go t.pollUpdates(ctx)
	t.setCommands()
	return t
}

func (t *TelegramNotifier) callAPI(method string, params map[string]interface{}) (json.RawMessage, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", t.token, method)
	body, _ := json.Marshal(params)
	resp, err := t.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parsing %s response: %w", method, err)
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, result.Description)
	}
	return result.Result, nil
}

func (t *TelegramNotifier) CreateTopic(name string) (string, error) {
	if len(name) > 128 {
		name = name[:128]
	}
	raw, err := t.callAPI("createForumTopic", map[string]interface{}{
		"chat_id": t.chatID, "name": name,
	})
	if err != nil {
		log.Printf("[telegram] topic creation failed, using main chat: %v", err)
		return "0", nil
	}
	var topic struct {
		MessageThreadID int64 `json:"message_thread_id"`
	}
	json.Unmarshal(raw, &topic)
	return strconv.FormatInt(topic.MessageThreadID, 10), nil
}

func (t *TelegramNotifier) sendMsg(topicID, text string, replyMarkup interface{}) (string, error) {
	params := map[string]interface{}{
		"chat_id": t.chatID, "text": text, "parse_mode": "MarkdownV2",
	}
	if tid, _ := strconv.ParseInt(topicID, 10, 64); tid != 0 {
		params["message_thread_id"] = tid
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}
	raw, err := t.callAPI("sendMessage", params)
	if err != nil {
		return "", err
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	json.Unmarshal(raw, &msg)
	return strconv.FormatInt(msg.MessageID, 10), nil
}

func (t *TelegramNotifier) SendMessage(topicID, text string) (string, error) {
	return t.sendMsg(topicID, text, nil)
}

func (t *TelegramNotifier) SendApproval(topicID, text, jobID string) (string, error) {
	keyboard := map[string]interface{}{
		"inline_keyboard": [][]map[string]string{{
			{"text": "Investigate", "callback_data": "investigate:" + jobID},
			{"text": "Ignore", "callback_data": "ignore:" + jobID},
		}},
	}
	return t.sendMsg(topicID, text, keyboard)
}

func (t *TelegramNotifier) EditMessage(topicID, messageID, text string) error {
	mid, _ := strconv.ParseInt(messageID, 10, 64)
	_, err := t.callAPI("editMessageText", map[string]interface{}{
		"chat_id": t.chatID, "message_id": mid, "text": text, "parse_mode": "MarkdownV2",
	})
	return err
}

func (t *TelegramNotifier) CloseTopic(topicID string) error {
	tid, _ := strconv.ParseInt(topicID, 10, 64)
	if tid == 0 {
		return nil
	}
	_, err := t.callAPI("closeForumTopic", map[string]interface{}{
		"chat_id": t.chatID, "message_thread_id": tid,
	})
	return err
}

func (t *TelegramNotifier) SendTyping(topicID string) error {
	params := map[string]interface{}{"chat_id": t.chatID, "action": "typing"}
	if tid, _ := strconv.ParseInt(topicID, 10, 64); tid != 0 {
		params["message_thread_id"] = tid
	}
	_, err := t.callAPI("sendChatAction", params)
	return err
}

func (t *TelegramNotifier) OnAction(cb func(string, string)) {
	t.mu.Lock()
	t.actionFn = cb
	t.mu.Unlock()
}
func (t *TelegramNotifier) OnMessage(cb func(string, string)) {
	t.mu.Lock()
	t.messageFn = cb
	t.mu.Unlock()
}

func (t *TelegramNotifier) isAllowed(userID int64) bool {
	return len(t.allowedUsers) == 0 || t.allowedUsers[userID]
}

func (t *TelegramNotifier) pollUpdates(ctx context.Context) {
	pollClient := &http.Client{Timeout: 45 * time.Second}
	var offset int64

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", t.token)
		params, _ := json.Marshal(map[string]interface{}{
			"offset": offset, "timeout": 30,
			"allowed_updates": []string{"callback_query", "message"},
		})
		resp, err := pollClient.Post(url, "application/json", bytes.NewReader(params))
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID      int64 `json:"update_id"`
				CallbackQuery *struct {
					ID   string `json:"id"`
					Data string `json:"data"`
					From struct {
						ID int64 `json:"id"`
					} `json:"from"`
				} `json:"callback_query"`
				Message *struct {
					Text            string `json:"text"`
					MessageThreadID int64  `json:"message_thread_id"`
					Chat            struct {
						Type string `json:"type"`
					} `json:"chat"`
					From struct {
						ID    int64 `json:"id"`
						IsBot bool  `json:"is_bot"`
					} `json:"from"`
				} `json:"message"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &result) != nil || !result.OK {
			time.Sleep(5 * time.Second)
			continue
		}

		t.mu.Lock()
		actionFn, messageFn := t.actionFn, t.messageFn
		t.mu.Unlock()

		for _, u := range result.Result {
			offset = u.UpdateID + 1
			if u.CallbackQuery != nil {
				t.callAPI("answerCallbackQuery", map[string]interface{}{"callback_query_id": u.CallbackQuery.ID})
				if !t.isAllowed(u.CallbackQuery.From.ID) {
					continue
				}
				parts := strings.SplitN(u.CallbackQuery.Data, ":", 2)
				if len(parts) == 2 && actionFn != nil {
					actionFn(parts[1], parts[0])
				}
			}
			if u.Message != nil && !u.Message.From.IsBot && u.Message.Text != "" &&
				(u.Message.Chat.Type == "group" || u.Message.Chat.Type == "supergroup") {
				if !t.isAllowed(u.Message.From.ID) {
					continue
				}
				if messageFn != nil {
					messageFn(strconv.FormatInt(u.Message.MessageThreadID, 10), u.Message.Text)
				}
			}
		}
	}
}

func (t *TelegramNotifier) setCommands() {
	t.callAPI("setMyCommands", map[string]interface{}{
		"commands": []map[string]string{
			{"command": "done", "description": "Close the current job and stop the agent"},
			{"command": "agents", "description": "List all active agents"},
		},
	})
}

// TestConnection sends a test message and returns nil on success.
func (t *TelegramNotifier) TestConnection() error {
	_, err := t.callAPI("getMe", map[string]interface{}{})
	return err
}

// GetBotUsername returns the bot's username.
func GetBotUsername(token string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &r) != nil || !r.OK {
		return "", fmt.Errorf("invalid bot token")
	}
	return r.Result.Username, nil
}

// PollForGroup polls Telegram updates until the bot receives a message in a group,
// returning the chat ID and title. Used during setup to auto-detect the group.
func PollForGroup(token string) (int64, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	pollClient := &http.Client{Timeout: 45 * time.Second}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", token)

	// Clear old updates.
	var offset int64
	body, _ := json.Marshal(map[string]interface{}{"offset": -1, "limit": 1})
	if resp, err := client.Post(apiURL, "application/json", bytes.NewReader(body)); err == nil {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var r struct {
			Result []struct {
				UpdateID int64 `json:"update_id"`
			} `json:"result"`
		}
		json.Unmarshal(raw, &r)
		if len(r.Result) > 0 {
			offset = r.Result[0].UpdateID + 1
		}
	}

	for {
		body, _ := json.Marshal(map[string]interface{}{
			"offset": offset, "timeout": 30,
			"allowed_updates": []string{"message", "my_chat_member"},
		})
		resp, err := pollClient.Post(apiURL, "application/json", bytes.NewReader(body))
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result struct {
			Result []struct {
				UpdateID int64 `json:"update_id"`
				Message  *struct {
					Chat struct {
						ID    int64  `json:"id"`
						Title string `json:"title"`
						Type  string `json:"type"`
					} `json:"chat"`
				} `json:"message"`
				MyChatMember *struct {
					Chat struct {
						ID    int64  `json:"id"`
						Title string `json:"title"`
						Type  string `json:"type"`
					} `json:"chat"`
				} `json:"my_chat_member"`
			} `json:"result"`
		}
		json.Unmarshal(raw, &result)

		for _, u := range result.Result {
			offset = u.UpdateID + 1
			if u.Message != nil && isGroup(u.Message.Chat.Type) {
				return u.Message.Chat.ID, u.Message.Chat.Title, nil
			}
			if u.MyChatMember != nil && isGroup(u.MyChatMember.Chat.Type) {
				return u.MyChatMember.Chat.ID, u.MyChatMember.Chat.Title, nil
			}
		}
	}
}

func isGroup(t string) bool { return t == "group" || t == "supergroup" }

// EscapeMarkdownV2 escapes special characters for Telegram MarkdownV2.
func EscapeMarkdownV2(s string) string {
	const special = `_*[]()~` + "`" + `>#+-=|{}.!`
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
