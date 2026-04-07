package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func RunSetup() {
	fmt.Println("\n  Pylon Setup\n")

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		fmt.Println("  Step 1: Create your bot")
		fmt.Println("  Open and send /newbot:")
		fmt.Println("  --> https://t.me/BotFather\n")
		fmt.Print("  Paste your bot token: ")
		s := bufio.NewScanner(os.Stdin)
		if s.Scan() {
			token = strings.TrimSpace(s.Text())
		}
		if token == "" {
			log.Fatal("no token provided")
		}
	} else {
		fmt.Println("  Using TELEGRAM_BOT_TOKEN from environment")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	username := setupGetMe(client, token)
	fmt.Printf("  Verified: @%s\n\n", username)

	fmt.Println("  Step 2: Start your bot (tap Start in the chat):")
	fmt.Printf("  --> https://t.me/%s\n\n", username)

	fmt.Println("  Step 3: Create a Telegram group if you don't have one.")
	fmt.Println("  Enable Topics: Group Settings > Topics > toggle on.")
	fmt.Println("  Then add the bot as admin:")
	fmt.Printf("  --> https://t.me/%s?startgroup=setup&admin=manage_topics\n\n", username)

	fmt.Println("  Then send any message in the group.")
	fmt.Println("  Waiting...\n")

	chatID, title := setupPollForGroup(client, token)
	fmt.Printf("  Detected: %s (ID: %d)\n\n", title, chatID)

	env := fmt.Sprintf("export TELEGRAM_BOT_TOKEN=%s\nexport TELEGRAM_CHAT_ID=%d\n", token, chatID)
	if err := os.WriteFile(".env", []byte(env), 0600); err != nil {
		log.Fatalf("writing .env: %v", err)
	}
	fmt.Println("  Wrote .env")
	fmt.Println("  Run:  source .env && make run\n")
}

func setupGetMe(client *http.Client, token string) string {
	resp, err := client.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token))
	if err != nil {
		log.Fatalf("getMe failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || !r.OK {
		log.Fatal("invalid bot token")
	}
	return r.Result.Username
}

func setupPollForGroup(client *http.Client, token string) (int64, string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", token)
	pollClient := &http.Client{Timeout: 45 * time.Second}

	// Clear old updates so we only react to new messages.
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
			"offset": offset, "timeout": 30, "allowed_updates": []string{"message", "my_chat_member"},
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
				UpdateID     int64     `json:"update_id"`
				Message      *chatInfo `json:"message"`
				MyChatMember *struct {
					Chat chatFields `json:"chat"`
				} `json:"my_chat_member"`
			} `json:"result"`
		}
		json.Unmarshal(raw, &result)

		for _, u := range result.Result {
			offset = u.UpdateID + 1
			// Detect group from a message sent in the group.
			if u.Message != nil && isGroup(u.Message.Chat.Type) {
				return u.Message.Chat.ID, u.Message.Chat.Title
			}
			// Detect group from the bot being added to it.
			if u.MyChatMember != nil && isGroup(u.MyChatMember.Chat.Type) {
				return u.MyChatMember.Chat.ID, u.MyChatMember.Chat.Title
			}
		}
	}
}

type chatFields struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type chatInfo struct {
	Chat chatFields `json:"chat"`
}

func isGroup(t string) bool { return t == "group" || t == "supergroup" }
