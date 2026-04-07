package main

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

type TelegramNotifier struct {
	token     string
	chatID    int64
	client    *http.Client
	mu        sync.Mutex
	actionFn  func(jobID string, action string)
	messageFn func(topicID string, text string)
}

func NewTelegramNotifier(ctx context.Context, token string, chatID int64) *TelegramNotifier {
	t := &TelegramNotifier{
		token:  token,
		chatID: chatID,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	go t.pollUpdates(ctx)
	t.setCommands()
	return t
}

func (t *TelegramNotifier) callAPI(method string, params map[string]interface{}) (json.RawMessage, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", t.token, method)
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshaling params: %w", err)
	}
	resp, err := t.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[telegram] %s failed: %v", method, err)
		return nil, fmt.Errorf("calling %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response for %s: %w", method, err)
	}
	var result struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parsing response for %s: %w", method, err)
	}
	if !result.OK {
		err := fmt.Errorf("telegram %s: %s", method, result.Description)
		log.Printf("[telegram] %v", err)
		return nil, err
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
		log.Printf("[telegram] topic creation failed (falling back to main chat): %v", err)
		return "0", nil
	}
	var topic struct {
		MessageThreadID int64 `json:"message_thread_id"`
	}
	if err := json.Unmarshal(raw, &topic); err != nil {
		log.Printf("[telegram] failed to parse topic response: %v", err)
		return "0", nil
	}
	return strconv.FormatInt(topic.MessageThreadID, 10), nil
}

func (t *TelegramNotifier) sendMsg(topicID string, text string, replyMarkup interface{}) (string, error) {
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
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", fmt.Errorf("parsing sendMessage result: %w", err)
	}
	return strconv.FormatInt(msg.MessageID, 10), nil
}

func (t *TelegramNotifier) SendMessage(topicID string, text string) (string, error) {
	return t.sendMsg(topicID, text, nil)
}

func (t *TelegramNotifier) SendApproval(topicID string, text string, jobID string) (string, error) {
	keyboard := map[string]interface{}{
		"inline_keyboard": [][]map[string]string{{
			{"text": "Investigate", "callback_data": "investigate:" + jobID},
			{"text": "Ignore", "callback_data": "ignore:" + jobID},
		}},
	}
	return t.sendMsg(topicID, text, keyboard)
}

func (t *TelegramNotifier) EditMessage(topicID string, messageID string, text string) error {
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
	params := map[string]interface{}{
		"chat_id": t.chatID, "action": "typing",
	}
	if tid, _ := strconv.ParseInt(topicID, 10, 64); tid != 0 {
		params["message_thread_id"] = tid
	}
	_, err := t.callAPI("sendChatAction", params)
	return err
}

func (t *TelegramNotifier) OnAction(cb func(jobID string, action string)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.actionFn = cb
}

func (t *TelegramNotifier) OnMessage(cb func(topicID string, text string)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messageFn = cb
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
			log.Printf("[telegram] poll error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("[telegram] poll read error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		var result struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID      int64 `json:"update_id"`
				CallbackQuery *struct {
					ID   string `json:"id"`
					Data string `json:"data"`
				} `json:"callback_query"`
				Message *struct {
					Text            string `json:"text"`
					MessageThreadID int64  `json:"message_thread_id"`
					Chat            struct {
						Type string `json:"type"`
					} `json:"chat"`
					From struct {
						IsBot bool `json:"is_bot"`
					} `json:"from"`
				} `json:"message"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || !result.OK {
			log.Printf("[telegram] poll parse error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		t.mu.Lock()
		actionFn := t.actionFn
		messageFn := t.messageFn
		t.mu.Unlock()

		for _, u := range result.Result {
			offset = u.UpdateID + 1

			if u.CallbackQuery != nil {
				t.callAPI("answerCallbackQuery", map[string]interface{}{
					"callback_query_id": u.CallbackQuery.ID,
				})
				parts := strings.SplitN(u.CallbackQuery.Data, ":", 2)
				if len(parts) == 2 && actionFn != nil {
					actionFn(parts[1], parts[0])
				}
			}

			if u.Message != nil && !u.Message.From.IsBot && u.Message.Text != "" &&
				(u.Message.Chat.Type == "group" || u.Message.Chat.Type == "supergroup") {
				if messageFn != nil {
					topicID := strconv.FormatInt(u.Message.MessageThreadID, 10)
					messageFn(topicID, u.Message.Text)
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

func escapeMarkdownV2(s string) string {
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
