package channel

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

// errChatNotDetected is returned by API methods when chat_id is 0 (auto-detect pending).
var errChatNotDetected = fmt.Errorf("chat_id not yet detected; send a message to the bot to auto-detect")

// Telegram implements Channel using the Telegram Bot API.
type Telegram struct {
	token        string
	chatID       int64
	allowedUsers map[int64]bool
	client       *http.Client
	mu           sync.Mutex
	actionFn     func(jobID string, action string)
	messageFn    func(topicID string, text string, messageID string)

	// Auto-detection: when chatID starts as 0, these handle one-shot detection.
	detectOnce     sync.Once
	onChatDetected func(chatID int64)
}

func NewTelegram(ctx context.Context, token string, chatID int64, allowedUsers []int64) *Telegram {
	allowed := make(map[int64]bool, len(allowedUsers))
	for _, id := range allowedUsers {
		allowed[id] = true
	}
	t := &Telegram{
		token: token, chatID: chatID, allowedUsers: allowed,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	if chatID == 0 {
		log.Printf("[telegram] chat_id is 0; will auto-detect from first inbound message")
	}
	go t.pollUpdates(ctx)
	t.setCommands()
	return t
}

func (t *Telegram) Ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.chatID != 0
}

// getChatID returns the current chatID, or an error if still awaiting auto-detection.
func (t *Telegram) getChatID() (int64, error) {
	t.mu.Lock()
	id := t.chatID
	t.mu.Unlock()
	if id == 0 {
		return 0, errChatNotDetected
	}
	return id, nil
}

// OnChatDetected registers a callback that fires once when the chat ID is
// auto-detected from the first inbound message. Only relevant when chatID is 0.
func (t *Telegram) OnChatDetected(cb func(chatID int64)) {
	t.mu.Lock()
	t.onChatDetected = cb
	t.mu.Unlock()
}

func (t *Telegram) callAPI(method string, params map[string]interface{}) (json.RawMessage, error) {
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

func (t *Telegram) CreateTopic(name string) (string, error) {
	chatID, err := t.getChatID()
	if err != nil {
		return "0", err
	}
	if len(name) > 128 {
		name = name[:128]
	}
	raw, err := t.callAPI("createForumTopic", map[string]interface{}{
		"chat_id": chatID, "name": name,
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

func (t *Telegram) sendMsg(topicID, text string, replyMarkup interface{}) (string, error) {
	chatID, err := t.getChatID()
	if err != nil {
		return "", err
	}
	params := map[string]interface{}{
		"chat_id": chatID, "text": text, "parse_mode": "MarkdownV2",
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

// telegramMaxLen is the maximum text length for a single Telegram message.
const telegramMaxLen = 4096

func (t *Telegram) SendMessage(topicID, text string) (string, error) {
	if len(text) <= telegramMaxLen {
		return t.sendMsg(topicID, text, nil)
	}
	chunks := splitMessage(text, telegramMaxLen)
	var lastID string
	for _, chunk := range chunks {
		id, err := t.sendMsg(topicID, chunk, nil)
		if err != nil {
			return lastID, err
		}
		lastID = id
	}
	return lastID, nil
}

func (t *Telegram) ReplyMessage(topicID, text, replyTo string) (string, error) {
	chatID, err := t.getChatID()
	if err != nil {
		return "", err
	}
	params := map[string]interface{}{
		"chat_id": chatID, "text": text, "parse_mode": "MarkdownV2",
	}
	if tid, _ := strconv.ParseInt(topicID, 10, 64); tid != 0 {
		params["message_thread_id"] = tid
	}
	if mid, _ := strconv.ParseInt(replyTo, 10, 64); mid != 0 {
		params["reply_to_message_id"] = mid
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

func (t *Telegram) SendApproval(topicID, text, jobID string) (string, error) {
	keyboard := map[string]interface{}{
		"inline_keyboard": [][]map[string]string{{
			{"text": "Investigate", "callback_data": "investigate:" + jobID},
			{"text": "Ignore", "callback_data": "ignore:" + jobID},
		}},
	}
	return t.sendMsg(topicID, text, keyboard)
}

func (t *Telegram) EditMessage(topicID, messageID, text string) error {
	chatID, err := t.getChatID()
	if err != nil {
		return err
	}
	mid, _ := strconv.ParseInt(messageID, 10, 64)
	_, err = t.callAPI("editMessageText", map[string]interface{}{
		"chat_id": chatID, "message_id": mid, "text": text, "parse_mode": "MarkdownV2",
	})
	return err
}

func (t *Telegram) FormatText(text string) string {
	return MarkdownToTelegramV2(text)
}

func (t *Telegram) CloseTopic(topicID string) error {
	chatID, err := t.getChatID()
	if err != nil {
		return err
	}
	tid, _ := strconv.ParseInt(topicID, 10, 64)
	if tid == 0 {
		return nil
	}
	_, err = t.callAPI("closeForumTopic", map[string]interface{}{
		"chat_id": chatID, "message_thread_id": tid,
	})
	return err
}

func (t *Telegram) SendTyping(topicID string) error {
	chatID, err := t.getChatID()
	if err != nil {
		return err
	}
	params := map[string]interface{}{"chat_id": chatID, "action": "typing"}
	if tid, _ := strconv.ParseInt(topicID, 10, 64); tid != 0 {
		params["message_thread_id"] = tid
	}
	_, err = t.callAPI("sendChatAction", params)
	return err
}

func (t *Telegram) OnAction(cb func(string, string)) {
	t.mu.Lock()
	t.actionFn = cb
	t.mu.Unlock()
}
func (t *Telegram) OnMessage(cb func(string, string, string)) {
	t.mu.Lock()
	t.messageFn = cb
	t.mu.Unlock()
}

func (t *Telegram) isAllowed(userID int64) bool {
	return len(t.allowedUsers) == 0 || t.allowedUsers[userID]
}

func (t *Telegram) pollUpdates(ctx context.Context) {
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
					MessageID       int64  `json:"message_id"`
					Text            string `json:"text"`
					MessageThreadID int64  `json:"message_thread_id"`
					Chat            struct {
						ID   int64  `json:"id"`
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
				t.callAPI("answerCallbackQuery", map[string]interface{}{"callback_query_id": u.CallbackQuery.ID}) //nolint:errcheck // best-effort ack
				if !t.isAllowed(u.CallbackQuery.From.ID) {
					continue
				}
				parts := strings.SplitN(u.CallbackQuery.Data, ":", 2)
				if len(parts) == 2 && actionFn != nil {
					actionFn(parts[1], parts[0])
				}
			}
			if u.Message != nil && !u.Message.From.IsBot && u.Message.Text != "" {
				isPrivate := u.Message.Chat.Type == "private"
				isGroupMsg := u.Message.Chat.Type == "group" || u.Message.Chat.Type == "supergroup"
				if !isPrivate && !isGroupMsg {
					continue
				}
				// Auto-detect chat_id from the first inbound message when chatID is 0.
				t.mu.Lock()
				needsDetect := t.chatID == 0
				t.mu.Unlock()
				if needsDetect && u.Message.Chat.ID != 0 {
					t.detectOnce.Do(func() {
						t.mu.Lock()
						t.chatID = u.Message.Chat.ID
						cb := t.onChatDetected
						t.mu.Unlock()
						log.Printf("[telegram] auto-detected chat_id: %d", u.Message.Chat.ID)
						if cb != nil {
							cb(u.Message.Chat.ID)
						}
					})
				}
				if !t.isAllowed(u.Message.From.ID) {
					continue
				}
				if messageFn != nil {
					topicID := "0"
					if isGroupMsg {
						topicID = strconv.FormatInt(u.Message.MessageThreadID, 10)
					}
					messageFn(
						topicID,
						u.Message.Text,
						strconv.FormatInt(u.Message.MessageID, 10),
					)
				}
			}
		}
	}
}

func (t *Telegram) setCommands() {
	cmds := make([]map[string]string, len(BotCommands))
	for i, c := range BotCommands {
		cmds[i] = map[string]string{"command": c.Name, "description": c.Description}
	}
	t.callAPI("setMyCommands", map[string]interface{}{"commands": cmds}) //nolint:errcheck // best-effort
}

func (t *Telegram) Commands() []Command {
	return BotCommands
}

// TestConnection sends a test message and returns nil on success.
func (t *Telegram) TestConnection() error {
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

// CheckChatAccess verifies the bot can access a chat and create topics.
func CheckChatAccess(token string, chatID int64) error {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getChat", token)
	body, _ := json.Marshal(map[string]interface{}{"chat_id": chatID})
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(raw, &r)
	if !r.OK {
		return fmt.Errorf("%s", r.Description)
	}
	return nil
}

// PollForChat polls Telegram updates until the bot receives a message in a group
// or a DM, returning the chat ID and title. Used during setup to auto-detect.
func PollForChat(token string) (int64, string, error) {
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
			if u.Message != nil {
				ct := u.Message.Chat.Type
				if isGroup(ct) || ct == "private" {
					title := u.Message.Chat.Title
					if ct == "private" {
						title = "DM"
					}
					return u.Message.Chat.ID, title, nil
				}
			}
			if u.MyChatMember != nil && isGroup(u.MyChatMember.Chat.Type) {
				return u.MyChatMember.Chat.ID, u.MyChatMember.Chat.Title, nil
			}
		}
	}
}

func isGroup(t string) bool { return t == "group" || t == "supergroup" }
