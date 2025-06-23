package main

import (
	"database/sql"
	"encoding/json"
	_ "encoding/json"
	"fmt"
	"gopkg.in/telebot.v3"
	"log"
	_ "modernc.org/sqlite"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	SupportGroupLink = "https://t.me/+cZ8_qXqR_uI5Zjcy"
	SupportGroupID   = -1002574381342
)

type Ticket struct {
	ID        int
	UserID    int64
	UserName  string
	Title     string
	Message   string
	CreatedAt string
	Status    string
}

var db *sql.DB
var bot *telebot.Bot

func main() {
	// 1. Инициализация БД
	if err := initDB(); err != nil {
		log.Panicf("Ошибка инициализации БД: %v", err)
	}
	defer db.Close()

	// 2. Инициализация бота
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("Укажите TELEGRAM_BOT_TOKEN в переменных окружения")
	}

	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	var err error
	bot, err = telebot.NewBot(pref)
	if err != nil {
		log.Panic(err)
	}

	log.Printf("Бот поддержки @%s запущен", bot.Me.Username)

	// 3. Проверка доступности группы
	if err := verifyGroupAccess(); err != nil {
		log.Panicf("Ошибка доступа к группе: %v", err)
	}

	// 4. Регистрация обработчиков
	bot.Handle("/start", handleStart)
	bot.Handle("/help", handleHelp)
	bot.Handle("/mytickets", handleMyTickets)
	bot.Handle(telebot.OnText, handleTextMessage)
	bot.Handle(telebot.OnCallback, handleCallback)

	// 5. Запуск бота
	log.Println("Бот готов к работе")
	bot.Start()
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite", "file:support.db?cache=shared")
	if err != nil {
		return err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS tickets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		user_name TEXT NOT NULL,
		title TEXT NOT NULL,
		message TEXT NOT NULL,
		created_at TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'open'
	)`)
	return err
}

func verifyGroupAccess() error {
	// Замените на этот упрощённый вариант для теста
	_, err := bot.ChatByID(SupportGroupID)
	return err
}

func handleStart(c telebot.Context) error {
	// Создаем постоянное меню с одной кнопкой Start
	markup := &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnStart := markup.Text("🔄 Главное меню")
	markup.Reply(markup.Row(btnStart))

	return c.Send(
		"🛠️ Добро пожаловать в поддержку!\n\n"+
			"Просто напишите о своей проблеме, и я создам обращение.\n\n"+
			"Используйте команды:\n"+
			"/mytickets - список ваших обращений\n"+
			"/help - справка",
		&telebot.SendOptions{ReplyMarkup: markup},
	)
}

func handleHelp(c telebot.Context) error {
	return c.Send(
		"ℹ️ Справка по боту поддержки:\n\n" +
			"1. Напишите боту о своей проблеме\n" +
			"2. Бот создаст обращение в группе поддержки\n" +
			"3. Администраторы рассмотрят ваш запрос\n\n" +
			"Группа поддержки: " + SupportGroupLink,
	)
}

func handleMyTickets(c telebot.Context) error {
	tickets, err := getUserTickets(c.Sender().ID)
	if err != nil {
		log.Printf("Ошибка получения тикетов: %v", err)
		return c.Send("❌ Ошибка при получении списка обращений")
	}

	if len(tickets) == 0 {
		return c.Send("У вас нет активных обращений.")
	}

	var msg strings.Builder
	msg.WriteString("📋 Ваши обращения:\n\n")
	for _, t := range tickets {
		msg.WriteString(fmt.Sprintf(
			"#%d - %s\nСтатус: %s\nДата: %s\n\n",
			t.ID, t.Title, t.Status, t.CreatedAt))
	}

	return c.Send(msg.String())
}

func handleTextMessage(c telebot.Context) error {
	// Игнорируем команды
	if c.Message().Text[0] == '/' {
		return nil
	}

	// Создаем тикет
	ticket := Ticket{
		UserID:    c.Sender().ID,
		UserName:  c.Sender().Username,
		Title:     fmt.Sprintf("Обращение от %s", c.Sender().FirstName),
		Message:   c.Message().Text,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
		Status:    "open",
	}

	// Сохраняем в БД
	id, err := createTicket(ticket)
	if err != nil {
		log.Printf("Ошибка создания тикета: %v", err)
		return c.Send("❌ Ошибка при создании обращения")
	}

	// Отправляем в группу поддержки
	if err := sendToSupportGroup(id, ticket, c.Message()); err != nil {
		log.Printf("Ошибка отправки в группу: %v", err)
		return c.Send(fmt.Sprintf(
			"⚠️ Не удалось отправить обращение в группу поддержки.\n\n"+
				"Попробуйте позже или свяжитесь с администратором:\n%s",
			SupportGroupLink))
	}

	// Уведомляем пользователя
	return c.Send(fmt.Sprintf(
		"✅ Обращение #%d создано!\n\n"+
			"Тема: %s\n"+
			"Статус: %s\n\n"+
			"Вы можете отслеживать прогресс в группе поддержки:\n%s",
		id, ticket.Title, ticket.Status, SupportGroupLink))
}

func sendToSupportGroup(ticketID int64, t Ticket, origMsg *telebot.Message) error {
	text := fmt.Sprintf(
		"🚨 Новое обращение #%d\n\n"+
			"👤 От: %s %s (@%s)\n"+
			"🆔 UserID: %d\n\n"+ // Добавлена строка
			"📌 Тема: %s\n\n"+
			"📝 Сообщение:\n%s\n\n"+
			"🕒 Дата: %s\n"+
			"🔗 Статус: %s",
		ticketID,
		origMsg.Sender.FirstName,
		origMsg.Sender.LastName,
		t.UserName,
		t.UserID, // Добавлено
		t.Title,
		t.Message,
		t.CreatedAt,
		t.Status,
	)

	// 1. Сначала создаем тему (если группа - форум)
	topicName := fmt.Sprintf("Обращение #%d: %s", ticketID, t.Title)
	threadID, err := createForumTopic(topicName)
	if err != nil {
		log.Printf("Не удалось создать тему: %v. Пробую без темы", err)
		threadID = 0 // 0 означает "без темы"
	}

	// Создаем кнопки с правильным форматом данных
	markup := &telebot.ReplyMarkup{}
	markup.Inline(
		markup.Row(
			markup.Data("✅ Принято", "take_btn", strconv.FormatInt(ticketID, 10)),
			markup.Data("❌ Закрыто", "close_btn", strconv.FormatInt(ticketID, 10)),
		),
	)

	// 3. Отправляем сообщение
	_, err = bot.Send(
		telebot.ChatID(SupportGroupID),
		text,
		&telebot.SendOptions{
			ReplyMarkup: markup,
			ThreadID:    threadID, // 0 - игнорируется, если не форум
		},
	)

	return err
}

func createForumTopic(name string) (int, error) {
	// Используем Raw API Telegram
	params := map[string]interface{}{
		"chat_id": SupportGroupID,
		"name":    name,
	}

	var result struct {
		Result struct {
			MessageThreadID int `json:"message_thread_id"`
		} `json:"result"`
	}

	resp, err := bot.Raw("createForumTopic", params)
	if err != nil {
		return 0, err
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return 0, err
	}

	return result.Result.MessageThreadID, nil
}

func createTicket(t Ticket) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO tickets (user_id, user_name, title, message, created_at, status)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.UserID, t.UserName, t.Title, t.Message, t.CreatedAt, t.Status,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func getUserTickets(userID int64) ([]Ticket, error) {
	rows, err := db.Query(
		`SELECT id, title, status, created_at FROM tickets
		WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.CreatedAt)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

func updateTicketStatus(ticketID int64, status string) error {
	_, err := db.Exec(
		`UPDATE tickets SET status = ? WHERE id = ?`,
		status, ticketID,
	)
	return err
}

func getTicket(ticketID int64) (*Ticket, error) {
	row := db.QueryRow(
		`SELECT id, user_id, user_name, title, message, created_at, status 
         FROM tickets WHERE id = ?`,
		ticketID,
	)

	var t Ticket
	err := row.Scan(&t.ID, &t.UserID, &t.UserName, &t.Title, &t.Message, &t.CreatedAt, &t.Status)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func handleCallback(c telebot.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}

	log.Printf("Callback received - Unique: '%s', Data: '%s'", cb.Unique, cb.Data)
	log.Printf("Full callback struct: %+v", cb)

	// Очищаем данные от управляющих символов
	cleanData := strings.TrimFunc(cb.Data, func(r rune) bool {
		return r < 32 || r == 127
	})

	// Разбиваем данные на части
	parts := strings.Split(cleanData, "|")
	if len(parts) < 2 { // Изменили условие для большей гибкости
		log.Printf("Invalid callback data format: %s", cleanData)
		return c.Respond(&telebot.CallbackResponse{
			Text: "Ошибка обработки запроса",
		})
	}

	action := parts[0]
	ticketIDStr := parts[len(parts)-1] // Берем последнюю часть как ticketID

	ticketID, err := strconv.ParseInt(ticketIDStr, 10, 64)
	if err != nil {
		log.Printf("Failed to parse ticket ID: %v", err)
		return c.Respond(&telebot.CallbackResponse{
			Text: "Ошибка обработки запроса",
		})
	}

	var status, userMsg string
	var newMarkup *telebot.ReplyMarkup

	// Определяем действие
	switch action {
	case "take_btn":
		status = "in_progress"
		userMsg = fmt.Sprintf("✅ Ваше обращение #%d принято в работу!", ticketID)

		// Создаем новую клавиатуру только с кнопкой "Закрыть"
		newMarkup = &telebot.ReplyMarkup{}
		newMarkup.Inline(
			newMarkup.Row(
				newMarkup.Data("❌ Закрыто", "close_btn", fmt.Sprintf("close_btn|%d", ticketID)), // Исправлено
			),
		)

	case "close_btn":
		status = "closed"
		userMsg = fmt.Sprintf("✔️ Обращение #%d закрыто. Спасибо!", ticketID)
		newMarkup = &telebot.ReplyMarkup{} // Пустая клавиатура
	default:
		log.Printf("Unknown callback action: %s", action)
		return c.Respond()
	}

	// Обновляем статус в БД
	if err := updateTicketStatus(ticketID, status); err != nil {
		log.Printf("Failed to update ticket status: %v", err)
	}

	// Отправляем уведомление пользователю
	if ticket, err := getTicket(ticketID); err == nil {
		if _, err := bot.Send(telebot.ChatID(ticket.UserID), userMsg); err != nil {
			log.Printf("Failed to notify user: %v", err)
		}
	}

	// Обновляем сообщение в группе поддержки
	if cb.Message != nil {
		newText := strings.Replace(cb.Message.Text, "🔗 Статус: open", "🔗 Статус: "+status, 1)
		if _, err := bot.Edit(cb.Message, newText); err != nil {
			log.Printf("Failed to update message text: %v", err)
		}

		if _, err := bot.EditReplyMarkup(cb.Message, newMarkup); err != nil {
			log.Printf("Failed to update buttons: %v", err)
		}
	}

	return c.Respond()
}

// Обновленная функция создания кнопок
func createTicketButtons(ticketID int64) *telebot.ReplyMarkup {
	markup := &telebot.ReplyMarkup{}
	markup.Inline(
		markup.Row(
			markup.Data("✅ Принято", "take_ticket", strconv.FormatInt(ticketID, 10)),
			markup.Data("❌ Закрыто", "close_ticket", strconv.FormatInt(ticketID, 10)),
		),
	)
	return markup
}
