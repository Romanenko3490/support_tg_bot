package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	_ "modernc.org/sqlite"
)

const (
	SupportGroupLink = "https://t.me/+d9t6S8-8iy1hOTli"
	SupportGroupID   = -1002574381342
	MaxMessagesLimit = 4000
)

type Ticket struct {
	ID        int64
	UserID    int64
	UserName  string
	Title     string
	Message   string
	CreatedAt string
	Status    string
	ThreadID  int
}

type TicketMessage struct {
	ID        int64
	TicketID  int64
	MessageID int
	UserID    int64
	UserName  string
	Text      string
	Date      string
	IsSupport bool
}

var (
	db  *sql.DB
	bot *telebot.Bot
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("=== ЗАПУСК БОТА ПОДДЕРЖКИ ===")

	if err := initDB(); err != nil {
		log.Fatalf("Ошибка инициализации БД: %v", err)
	}
	defer db.Close()

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("Укажите TELEGRAM_BOT_TOKEN в переменных окружения")
	}

	pref := telebot.Settings{
		Token:   token,
		Poller:  &telebot.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c telebot.Context) { log.Printf("Ошибка: %v", err) },
		Verbose: true,
	}

	var err error
	bot, err = telebot.NewBot(pref)
	if err != nil {
		log.Panic(err)
	}

	log.Printf("Бот @%s запущен", bot.Me.Username)

	if err := verifyGroupAccess(); err != nil {
		log.Printf("Предупреждение: %v", err)
	}

	registerHandlers()

	log.Println("=== БОТ ГОТОВ К РАБОТЕ ===")
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
		status TEXT NOT NULL DEFAULT 'open',
		thread_id INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS ticket_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ticket_id INTEGER NOT NULL,
		message_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		user_name TEXT NOT NULL,
		text TEXT NOT NULL,
		date TEXT NOT NULL,
		is_support BOOLEAN NOT NULL DEFAULT FALSE,
		FOREIGN KEY(ticket_id) REFERENCES tickets(id)
	);
	CREATE INDEX IF NOT EXISTS idx_ticket_messages_ticket_id ON ticket_messages(ticket_id);
	`)
	if err != nil {
		return err
	}

	go checkMessagesLimit()
	return nil
}

func checkMessagesLimit() {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM ticket_messages").Scan(&count)
	if err != nil {
		log.Printf("Ошибка проверки количества сообщений: %v", err)
		return
	}

	if count > MaxMessagesLimit {
		excess := count - MaxMessagesLimit
		_, err = db.Exec(`
			DELETE FROM ticket_messages 
			WHERE id IN (
				SELECT id FROM ticket_messages 
				ORDER BY date ASC 
				LIMIT ?
			)`, excess)
		if err != nil {
			log.Printf("Ошибка удаления старых сообщений: %v", err)
		}
	}
}

func verifyGroupAccess() error {
	chat, err := bot.ChatByID(SupportGroupID)
	if err != nil {
		return fmt.Errorf("ошибка получения чата: %v", err)
	}

	member, err := bot.ChatMemberOf(chat, bot.Me)
	if err != nil {
		return fmt.Errorf("ошибка проверки прав: %v", err)
	}

	if !member.CanManageTopics {
		return fmt.Errorf("бот не может управлять темами")
	}

	return nil
}

func registerHandlers() {
	bot.Handle("/start", handleStart)
	bot.Handle("/help", handleHelp)
	bot.Handle("/mytickets", handleMyTickets)
	bot.Handle("/history", handleHistoryCommand)

	bot.Handle(&telebot.Btn{Text: "Новое обращение"}, handleNewTicketButton)
	bot.Handle(&telebot.Btn{Text: "Закрыть обращение"}, handleCloseTicketButton)
	bot.Handle(&telebot.Btn{Text: "Мои обращения"}, handleMyTicketsButton)

	bot.Handle(telebot.OnCallback, handleCallbacks)
	bot.Handle(telebot.OnText, handleTextMessages)
}

func showUserMenu(c telebot.Context) error {
	menu := &telebot.ReplyMarkup{}
	btnNew := menu.Text("Новое обращение")
	btnClose := menu.Text("Закрыть обращение")
	btnHistory := menu.Text("Мои обращения")

	menu.Reply(
		menu.Row(btnNew),
		menu.Row(btnClose),
		menu.Row(btnHistory),
	)

	return c.Send("Выберите действие:", menu)
}

func handleStart(c telebot.Context) error {
	msg := "🛠️ Добро пожаловать в поддержку!\n\n" +
		"Вы можете создать новое обращение или просмотреть историю предыдущих."

	if err := c.Send(msg); err != nil {
		return err
	}
	return showUserMenu(c)
}

func handleHelp(c telebot.Context) error {
	return c.Send(
		"ℹ️ Как использовать бота:\n\n" +
			"1. Нажмите 'Новое обращение' для создания запроса\n" +
			"2. Бот создаст тему в группе поддержки\n" +
			"3. Все ваши последующие сообщения будут добавляться в эту тему\n" +
			"4. Администраторы ответят в теме\n\n" +
			"Группа поддержки: " + SupportGroupLink,
	)
}

func handleNewTicketButton(c telebot.Context) error {
	user := c.Sender()
	openTicket, err := getOpenUserTicket(user.ID)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("Ошибка проверки тикетов: %v", err)
		return c.Send("❌ Ошибка при обработке запроса")
	}

	if openTicket != nil {
		if openTicket.Status == "closed" {
			return c.Send("Ваше предыдущее обращение уже закрыто. Отправьте сообщение с описанием проблемы для создания нового.")
		}
		return c.Send(fmt.Sprintf(
			"❌ У вас уже есть активное обращение #%d\n\n"+
				"Пожалуйста, дождитесь ответа поддержки или закройте текущее обращение.",
			openTicket.ID))
	}

	return c.Send("Отправьте сообщение с описанием проблемы для создания нового обращения.")
}

func handleCloseTicketButton(c telebot.Context) error {
	user := c.Sender()
	openTicket, err := getOpenUserTicket(user.ID)
	if err != nil {
		if err == sql.ErrNoRows {
			return c.Send("У вас нет активных обращений для закрытия.")
		}
		log.Printf("Ошибка проверки тикетов: %v", err)
		return c.Send("❌ Ошибка при обработке запроса")
	}

	if openTicket.Status == "closed" {
		return c.Send("Это обращение уже закрыто.")
	}

	if err := updateTicketStatus(openTicket.ID, "closed"); err != nil {
		log.Printf("Ошибка обновления статуса: %v", err)
		return c.Send("❌ Ошибка при закрытии обращения")
	}

	text := fmt.Sprintf(
		"⚠️ Обращение #%d закрыто пользователем\n\n"+
			"👤 Пользователь: @%s\n"+
			"🕒 Время закрытия: %s",
		openTicket.ID,
		user.Username,
		time.Now().Format("2006-01-02 15:04:05"),
	)

	if _, err := bot.Send(
		telebot.ChatID(SupportGroupID),
		text,
		&telebot.SendOptions{ThreadID: openTicket.ThreadID},
	); err != nil {
		log.Printf("Ошибка отправки уведомления в группу: %v", err)
	}

	return c.Send(fmt.Sprintf("✅ Обращение #%d успешно закрыто.", openTicket.ID))
}

func handleHistoryCommand(c telebot.Context) error {
	return showTicketHistory(c.Sender().ID, c)
}

func handleMyTicketsButton(c telebot.Context) error {
	return handleMyTickets(c)
}

func handleMyTickets(c telebot.Context) error {
	return showTicketHistory(c.Sender().ID, c)
}

func showTicketHistory(userID int64, c telebot.Context) error {
	tickets, err := getUserTickets(userID, 10)
	if err != nil {
		log.Printf("Ошибка получения тикетов: %v", err)
		return c.Send("❌ Ошибка при получении истории обращений")
	}

	if len(tickets) == 0 {
		return c.Send("У вас пока нет обращений.")
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	for _, t := range tickets {
		status := "🟢"
		if t.Status == "closed" {
			status = "🔴"
		} else if t.Status == "in_progress" {
			status = "🟡"
		}

		btn := menu.Data(
			fmt.Sprintf("#%d %s - %s %s", t.ID, t.Title, status, t.CreatedAt[:10]),
			"ticket_"+strconv.FormatInt(t.ID, 10),
		)
		rows = append(rows, menu.Row(btn))
	}

	btnBack := menu.Data("← Назад", "back_to_menu")
	rows = append(rows, menu.Row(btnBack))

	menu.Inline(rows...)

	return c.Send("📋 Ваши последние обращения:", menu)
}

func handleCallbacks(c telebot.Context) error {
	data := c.Callback().Data

	// Удаляем возможные специальные символы в начале
	data = strings.TrimLeft(data, "\f")

	switch {
	case strings.HasPrefix(data, "ticket_"):
		ticketID, err := strconv.ParseInt(strings.TrimPrefix(data, "ticket_"), 10, 64)
		if err != nil {
			log.Printf("Ошибка парсинга ID тикета: %v", err)
			return c.Respond()
		}
		return showTicketDetails(c, ticketID)
	case data == "back_to_menu":
		return handleBackToMenu(c)
	case data == "back_to_history":
		return handleMyTickets(c)
	}
	return nil
}

func showTicketDetails(c telebot.Context, ticketID int64) error {
	ticket, err := getTicket(ticketID)
	if err != nil {
		log.Printf("Ошибка получения тикета: %v", err)
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при получении данных"})
	}

	// Проверяем, принадлежит ли тикет пользователю
	if ticket.UserID != c.Sender().ID {
		return c.Respond(&telebot.CallbackResponse{Text: "Это не ваше обращение"})
	}

	history, err := getTicketHistory(ticketID)
	if err != nil {
		log.Printf("Ошибка получения истории: %v", err)
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при получении истории"})
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf(
		"📋 Обращение #%d\n\n"+
			"📌 Тема: %s\n"+
			"🕒 Создано: %s\n"+
			"🔍 Статус: %s\n\n"+
			"--- История переписки ---\n\n",
		ticket.ID, ticket.Title, ticket.CreatedAt, getStatusText(ticket.Status),
	))

	for _, m := range history {
		sender := "Вы"
		if m.IsSupport {
			sender = fmt.Sprintf("Поддержка (%s)", m.UserName)
		}
		msg.WriteString(fmt.Sprintf(
			"💬 %s [%s]:\n%s\n\n",
			sender, m.Date, m.Text,
		))
	}

	menu := &telebot.ReplyMarkup{}
	btnBack := menu.Data("← Назад к списку", "back_to_history")
	menu.Inline(menu.Row(btnBack))

	// Отправляем новое сообщение с историей вместо редактирования
	_, err = bot.Send(
		c.Sender(),
		msg.String(),
		&telebot.SendOptions{ReplyMarkup: menu},
	)
	if err != nil {
		log.Printf("Ошибка отправки истории: %v", err)
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при отправке истории"})
	}

	return c.Respond()
}

func handleBackToMenu(c telebot.Context) error {
	return showUserMenu(c)
}

func handleTextMessages(c telebot.Context) error {
	if strings.HasPrefix(c.Text(), "/") {
		return nil
	}

	if c.Chat().Type == telebot.ChatPrivate {
		return handleUserMessage(c)
	}

	if c.Chat().ID == SupportGroupID {
		return handleSupportGroupMessage(c)
	}

	return nil
}

func handleUserMessage(c telebot.Context) error {
	user := c.Sender()
	openTicket, err := getOpenUserTicket(user.ID)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("Ошибка проверки тикетов: %v", err)
		return c.Send("❌ Ошибка при обработке сообщения")
	}

	if openTicket == nil {
		return createNewTicket(c)
	}

	if openTicket.Status == "closed" {
		return c.Send("❌ Ваше обращение уже закрыто. Нажмите 'Новое обращение' для создания нового.")
	}

	return forwardToExistingTicket(c, openTicket)
}

func createNewTicket(c telebot.Context) error {
	user := c.Sender()
	msg := c.Message()

	ticket := Ticket{
		UserID:    user.ID,
		UserName:  user.Username,
		Title:     fmt.Sprintf("Обращение от %s", user.FirstName),
		Message:   msg.Text,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
		Status:    "open",
	}

	ticketID, err := createTicket(ticket)
	if err != nil {
		log.Printf("Ошибка создания тикета: %v", err)
		return c.Send("❌ Ошибка при создании обращения")
	}

	if err := sendToSupportGroup(ticketID, ticket, msg); err != nil {
		log.Printf("Ошибка отправки в группу: %v", err)
		return c.Send(fmt.Sprintf(
			"⚠️ Не удалось создать тему в группе.\nСвяжитесь с администратором: %s",
			SupportGroupLink))
	}

	if err := saveMessageToHistory(ticketID, msg.ID, user.ID, user.Username, msg.Text, false); err != nil {
		log.Printf("Ошибка сохранения сообщения в историю: %v", err)
	}

	if err := c.Send(fmt.Sprintf(
		"✅ Обращение #%d создано!\n\n"+
			"Администраторы ответят в группе поддержки:\n%s\n\n"+
			"Все ваши последующие сообщения будут добавляться в эту тему.",
		ticketID, SupportGroupLink)); err != nil {
		return err
	}

	return showUserMenu(c)
}

func forwardToExistingTicket(c telebot.Context, ticket *Ticket) error {
	user := c.Sender()
	msg := c.Message()

	_, err := db.Exec(
		`UPDATE tickets SET message = ? WHERE id = ?`,
		msg.Text, ticket.ID,
	)
	if err != nil {
		log.Printf("Ошибка обновления тикета: %v", err)
		return c.Send("❌ Ошибка при обработке сообщения")
	}

	if err := saveMessageToHistory(ticket.ID, msg.ID, user.ID, user.Username, msg.Text, false); err != nil {
		log.Printf("Ошибка сохранения сообщения в историю: %v", err)
	}

	text := fmt.Sprintf(
		"📨 Новое сообщение по обращению #%d\n\n"+
			"👤 От: %s %s (@%s)\n"+
			"🆔 ID: %d\n\n"+
			"📝 Сообщение:\n%s",
		ticket.ID,
		user.FirstName,
		user.LastName,
		user.Username,
		user.ID,
		msg.Text,
	)

	_, err = bot.Send(
		telebot.ChatID(SupportGroupID),
		text,
		&telebot.SendOptions{ThreadID: ticket.ThreadID},
	)
	if err != nil {
		log.Printf("Ошибка отправки сообщения в тему: %v", err)
		return c.Send("❌ Не удалось отправить сообщение в группу поддержки")
	}

	return c.Send("✅ Ваше сообщение добавлено к обращению #" + strconv.FormatInt(ticket.ID, 10))
}

func handleSupportGroupMessage(c telebot.Context) error {
	if c.Chat().ID != SupportGroupID || c.Sender().ID == bot.Me.ID || c.Message().ThreadID == 0 || c.Message().IsService() {
		return nil
	}

	var ticketID int64
	err := db.QueryRow(
		`SELECT id FROM tickets WHERE thread_id = ?`,
		c.Message().ThreadID,
	).Scan(&ticketID)
	if err != nil {
		log.Printf("Ошибка поиска тикета: %v", err)
		return nil
	}

	ticket, err := getTicket(ticketID)
	if err != nil {
		log.Printf("Ошибка получения тикета: %v", err)
		return nil
	}

	if err := saveMessageToHistory(
		ticketID,
		c.Message().ID,
		c.Sender().ID,
		c.Sender().Username,
		c.Message().Text,
		true,
	); err != nil {
		log.Printf("Ошибка сохранения сообщения поддержки: %v", err)
	}

	replyText := fmt.Sprintf(
		"📨 Ответ по обращению #%d:\n\n%s\n\n",
		ticket.ID,
		c.Message().Text,
	)

	if _, err := bot.Send(telebot.ChatID(ticket.UserID), replyText); err != nil {
		log.Printf("Ошибка отправки ответа: %v", err)
	}
	return nil
}

func getOpenUserTicket(userID int64) (*Ticket, error) {
	row := db.QueryRow(
		`SELECT id, user_id, user_name, title, message, created_at, status, thread_id 
		FROM tickets 
		WHERE user_id = ? AND status != 'closed'
		ORDER BY id DESC LIMIT 1`,
		userID,
	)

	var t Ticket
	err := row.Scan(&t.ID, &t.UserID, &t.UserName, &t.Title, &t.Message, &t.CreatedAt, &t.Status, &t.ThreadID)
	if err != nil {
		return nil, err
	}
	return &t, nil
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

func sendToSupportGroup(ticketID int64, t Ticket, origMsg *telebot.Message) error {
	text := fmt.Sprintf(
		"🚨 Обращение #%d\n\n"+
			"👤 От: %s %s (@%s)\n"+
			"🆔 ID: %d\n\n"+
			"📝 Сообщение:\n%s\n\n"+
			"🕒 Дата: %s\n"+
			"🔗 Статус: %s",
		ticketID,
		origMsg.Sender.FirstName,
		origMsg.Sender.LastName,
		t.UserName,
		t.UserID,
		t.Message,
		t.CreatedAt,
		t.Status,
	)

	topicName := fmt.Sprintf("Обращение #%d: %s", ticketID, t.Title)
	threadID, err := createForumTopic(topicName)
	if err != nil {
		log.Printf("Не удалось создать тему: %v", err)
		return fmt.Errorf("не удалось создать тему: %v", err)
	}

	if threadID != 0 {
		_, err = db.Exec(
			`UPDATE tickets SET thread_id = ? WHERE id = ?`,
			threadID, ticketID,
		)
		if err != nil {
			log.Printf("Ошибка сохранения thread_id: %v", err)
		}
	}

	btnTake := telebot.Btn{Unique: fmt.Sprintf("take_btn_%d", ticketID), Text: "✅ Принято"}
	btnClose := telebot.Btn{Unique: fmt.Sprintf("close_btn_%d", ticketID), Text: "❌ Закрыто"}

	markup := &telebot.ReplyMarkup{}
	markup.Inline(markup.Row(btnTake, btnClose))

	bot.Handle(&btnTake, func(c telebot.Context) error {
		return handleTakeButton(c, ticketID)
	})
	bot.Handle(&btnClose, func(c telebot.Context) error {
		return handleCloseButton(c, ticketID)
	})

	_, err = bot.Send(
		telebot.ChatID(SupportGroupID),
		text,
		&telebot.SendOptions{
			ReplyMarkup: markup,
			ThreadID:    threadID,
		},
	)
	return err
}

func handleTakeButton(c telebot.Context, ticketID int64) error {
	if err := updateTicketStatus(ticketID, "in_progress"); err != nil {
		log.Printf("Ошибка обновления статуса: %v", err)
		return c.Respond()
	}

	ticket, err := getTicket(ticketID)
	if err != nil {
		log.Printf("Ошибка получения тикета: %v", err)
		return c.Respond()
	}

	_, err = bot.Send(
		telebot.ChatID(ticket.UserID),
		fmt.Sprintf("✅ Ваше обращение #%d принято в работу!", ticketID),
	)
	if err != nil {
		log.Printf("Ошибка отправки уведомления пользователю: %v", err)
	}

	btnClose := telebot.Btn{Unique: fmt.Sprintf("close_btn_%d", ticketID), Text: "❌ Закрыто"}
	markup := &telebot.ReplyMarkup{}
	markup.Inline(markup.Row(btnClose))

	editedText := strings.Replace(
		c.Message().Text,
		"🔗 Статус: open",
		"🔗 Статус: in_progress",
		1,
	)

	_, err = bot.Edit(
		c.Message(),
		editedText,
		&telebot.SendOptions{ReplyMarkup: markup},
	)
	if err != nil {
		log.Printf("Ошибка обновления сообщения: %v", err)
	}

	return c.Respond(&telebot.CallbackResponse{Text: "Обращение принято в работу"})
}

func handleCloseButton(c telebot.Context, ticketID int64) error {
	if err := updateTicketStatus(ticketID, "closed"); err != nil {
		log.Printf("Ошибка обновления статуса: %v", err)
		return c.Respond()
	}

	ticket, err := getTicket(ticketID)
	if err != nil {
		log.Printf("Ошибка получения тикета: %v", err)
		return c.Respond()
	}

	_, err = bot.Send(
		telebot.ChatID(ticket.UserID),
		fmt.Sprintf("✔️ Ваше обращение #%d закрыто. Спасибо, что обратились к нам!", ticketID),
	)
	if err != nil {
		log.Printf("Ошибка отправки уведомления пользователю: %v", err)
	}

	editedText := strings.Replace(
		c.Message().Text,
		fmt.Sprintf("🔗 Статус: %s", ticket.Status),
		"🔗 Статус: closed",
		1,
	)

	_, err = bot.Edit(c.Message(), editedText)
	if err != nil {
		log.Printf("Ошибка обновления сообщения: %v", err)
	}

	return c.Respond(&telebot.CallbackResponse{Text: "Обращение закрыто"})
}

func createForumTopic(name string) (int, error) {
	params := map[string]interface{}{
		"chat_id": SupportGroupID,
		"name":    name,
	}

	resp, err := bot.Raw("createForumTopic", params)
	if err != nil {
		return 0, fmt.Errorf("ошибка API: %v", err)
	}

	var result struct {
		Ok     bool `json:"ok"`
		Result struct {
			MessageThreadID int `json:"message_thread_id"`
		} `json:"result"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return 0, fmt.Errorf("ошибка парсинга ответа: %v", err)
	}

	if !result.Ok {
		return 0, fmt.Errorf("ошибка Telegram API")
	}

	return result.Result.MessageThreadID, nil
}

func getUserTickets(userID int64, limit int) ([]Ticket, error) {
	rows, err := db.Query(
		`SELECT id, title, status, created_at FROM tickets
		WHERE user_id = ? ORDER BY created_at DESC LIMIT ?`,
		userID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

func getTicket(id int64) (*Ticket, error) {
	row := db.QueryRow(
		`SELECT id, user_id, user_name, title, message, created_at, status, thread_id 
		FROM tickets WHERE id = ?`,
		id,
	)

	var t Ticket
	err := row.Scan(&t.ID, &t.UserID, &t.UserName, &t.Title, &t.Message, &t.CreatedAt, &t.Status, &t.ThreadID)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func updateTicketStatus(id int64, status string) error {
	_, err := db.Exec(
		`UPDATE tickets SET status = ? WHERE id = ?`,
		status, id,
	)
	return err
}

func saveMessageToHistory(ticketID int64, messageID int, userID int64, userName, text string, isSupport bool) error {
	_, err := db.Exec(
		`INSERT INTO ticket_messages 
		(ticket_id, message_id, user_id, user_name, text, date, is_support)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ticketID, messageID, userID, userName, text, time.Now().Format("2006-01-02 15:04:05"), isSupport,
	)
	return err
}

func getTicketHistory(ticketID int64) ([]TicketMessage, error) {
	rows, err := db.Query(
		`SELECT user_name, text, date, is_support 
		FROM ticket_messages 
		WHERE ticket_id = ? 
		ORDER BY date ASC`,
		ticketID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []TicketMessage
	for rows.Next() {
		var m TicketMessage
		err := rows.Scan(&m.UserName, &m.Text, &m.Date, &m.IsSupport)
		if err != nil {
			return nil, err
		}
		history = append(history, m)
	}
	return history, nil
}

func getStatusText(status string) string {
	switch status {
	case "open":
		return "🟢 Открыто"
	case "in_progress":
		return "🟡 В работе"
	case "closed":
		return "🔴 Закрыто"
	default:
		return status
	}
}
