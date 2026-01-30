package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"talk-to-ugur-back/ai"
	"talk-to-ugur-back/config"
	"talk-to-ugur-back/models/db"
)

type ChatHandler struct {
	queries *db.Queries
	ai      *ai.Client
	cfg     *config.Config
}

var errInvalidVisitorID = errors.New("invalid visitor_id")

func NewChatHandler(queries *db.Queries, aiClient *ai.Client, cfg *config.Config) *ChatHandler {
	return &ChatHandler{
		queries: queries,
		ai:      aiClient,
		cfg:     cfg,
	}
}

type sendMessageRequest struct {
	ThreadID  string `json:"thread_id"`
	VisitorID string `json:"visitor_id"`
	Message   string `json:"message" binding:"required"`
}

type messageResponse struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Emotion   *string   `json:"emotion,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type sendMessageResponse struct {
	VisitorID        string          `json:"visitor_id"`
	ThreadID         string          `json:"thread_id"`
	UserMessage      messageResponse `json:"user_message"`
	AssistantMessage messageResponse `json:"assistant_message"`
}

type createVisitorResponse struct {
	VisitorID string `json:"visitor_id"`
}

func (h *ChatHandler) HandleSendMessage(c *gin.Context) {
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}

	threadUUID, visitorUUID, userMsg, history, ok := h.prepareChat(c, req, message)
	if !ok {
		return
	}

	if strings.EqualFold(c.Query("stream"), "true") {
		h.streamChat(c, threadUUID, visitorUUID, userMsg, history)
		return
	}

	aiReply, err := h.ai.GenerateReply(c.Request.Context(), history)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai request failed"})
		log.Printf("ai error: %v", err)
		return
	}

	assistantMsg, err := h.queries.CreateChatMessage(c.Request.Context(), db.CreateChatMessageParams{
		Uuid:       pgUUID(uuid.New()),
		ThreadUuid: pgUUID(threadUUID),
		Role:       "assistant",
		Content:    aiReply.Text,
		Emotion:    pgText(aiReply.Emotion),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store assistant message"})
		return
	}

	resp := sendMessageResponse{
		VisitorID:        uuidOrEmpty(visitorUUID),
		ThreadID:         threadUUID.String(),
		UserMessage:      toMessageResponse(userMsg),
		AssistantMessage: toMessageResponse(assistantMsg),
	}
	if resp.VisitorID != "" {
		setVisitorCookie(c, resp.VisitorID)
		c.Header("X-Visitor-Id", resp.VisitorID)
	}

	c.JSON(http.StatusOK, resp)
}

func (h *ChatHandler) HandleCreateVisitor(c *gin.Context) {
	ctx := c.Request.Context()
	visitorUUID, err := h.resolveVisitor(ctx, c, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create visitor"})
		return
	}
	setVisitorCookie(c, visitorUUID.String())
	c.Header("X-Visitor-Id", visitorUUID.String())
	c.JSON(http.StatusOK, createVisitorResponse{
		VisitorID: visitorUUID.String(),
	})
}

func (h *ChatHandler) HandleGetMessages(c *gin.Context) {
	threadID := c.Param("thread_id")
	threadUUID, err := uuid.Parse(threadID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid thread_id"})
		return
	}

	ctx := c.Request.Context()
	thread, err := h.queries.GetChatThread(ctx, pgUUID(threadUUID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread not found"})
		return
	}
	if thread.VisitorUuid.Valid {
		h.touchVisitor(ctx, thread.VisitorUuid, c)
	}

	limit := parseLimit(c.Query("limit"))
	var messages []db.ChatMessage
	if limit > 0 {
		messages, err = h.queries.GetChatMessagesByThreadLimit(ctx, db.GetChatMessagesByThreadLimitParams{
			ThreadUuid: pgUUID(threadUUID),
			Limit:      int32(limit),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load messages"})
			return
		}
		reverseMessages(messages)
	} else {
		messages, err = h.queries.GetChatMessagesByThread(ctx, pgUUID(threadUUID))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load messages"})
			return
		}
	}

	responseMessages := make([]messageResponse, 0, len(messages))
	for _, msg := range messages {
		responseMessages = append(responseMessages, toMessageResponse(msg))
	}

	c.JSON(http.StatusOK, gin.H{
		"thread_id": threadUUID.String(),
		"messages":  responseMessages,
	})
}

func (h *ChatHandler) maxHistoryLimit() int {
	if h.cfg == nil || h.cfg.AIMaxHistory <= 0 {
		return 20
	}
	return h.cfg.AIMaxHistory
}

func parseLimit(raw string) int {
	if raw == "" {
		return 0
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func reverseMessages(messages []db.ChatMessage) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}

func toMessageResponse(msg db.ChatMessage) messageResponse {
	var emotion *string
	if msg.Emotion.Valid {
		value := msg.Emotion.String
		emotion = &value
	}
	return messageResponse{
		ID:        uuidString(msg.Uuid),
		Role:      msg.Role,
		Content:   msg.Content,
		Emotion:   emotion,
		CreatedAt: timeFromPg(msg.CreatedAt),
	}
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func pgText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func timeFromPg(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

func uuidOrEmpty(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func setVisitorCookie(c *gin.Context, visitorID string) {
	const maxAgeSeconds = 60 * 60 * 24 * 30
	c.SetCookie("visitor_id", visitorID, maxAgeSeconds, "/", "", false, false)
}

func (h *ChatHandler) prepareChat(c *gin.Context, req sendMessageRequest, message string) (uuid.UUID, uuid.UUID, db.ChatMessage, []db.ChatMessage, bool) {
	ctx := c.Request.Context()
	var threadUUID uuid.UUID
	var visitorUUID uuid.UUID
	if req.ThreadID == "" {
		var err error
		visitorUUID, err = h.resolveVisitor(ctx, c, req.VisitorID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "visitor not found"})
				return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
			}
			if errors.Is(err, errInvalidVisitorID) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid visitor_id"})
				return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create visitor"})
			return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
		}
		threadUUID = uuid.New()
		_, err = h.queries.CreateChatThread(ctx, db.CreateChatThreadParams{
			Uuid:        pgUUID(threadUUID),
			VisitorUuid: pgUUID(visitorUUID),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create thread"})
			return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
		}
	} else {
		var err error
		threadUUID, err = uuid.Parse(req.ThreadID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid thread_id"})
			return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
		}
		thread, err := h.queries.GetChatThread(ctx, pgUUID(threadUUID))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "thread not found"})
			return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
		}
		if thread.VisitorUuid.Valid {
			h.touchVisitor(ctx, thread.VisitorUuid, c)
			visitorUUID = uuid.UUID(thread.VisitorUuid.Bytes)
		}
	}

	userMsg, err := h.queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		Uuid:       pgUUID(uuid.New()),
		ThreadUuid: pgUUID(threadUUID),
		Role:       "user",
		Content:    message,
		Emotion:    pgtype.Text{},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store message"})
		return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
	}

	history, err := h.queries.GetChatMessagesByThreadLimit(ctx, db.GetChatMessagesByThreadLimitParams{
		ThreadUuid: pgUUID(threadUUID),
		Limit:      int32(h.maxHistoryLimit()),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load history"})
		return uuid.UUID{}, uuid.UUID{}, db.ChatMessage{}, nil, false
	}

	reverseMessages(history)

	return threadUUID, visitorUUID, userMsg, history, true
}

func (h *ChatHandler) streamChat(c *gin.Context, threadUUID uuid.UUID, visitorUUID uuid.UUID, userMsg db.ChatMessage, history []db.ChatMessage) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	visitorID := uuidOrEmpty(visitorUUID)
	if visitorID != "" {
		setVisitorCookie(c, visitorID)
		c.Header("X-Visitor-Id", visitorID)
	}

	meta := gin.H{
		"visitor_id":   visitorID,
		"thread_id":    threadUUID.String(),
		"user_message": toMessageResponse(userMsg),
	}
	if err := writeSSE(c, "meta", meta); err != nil {
		log.Printf("sse meta error: %v", err)
		return
	}

	var collected strings.Builder
	aiReply, err := h.ai.StreamReply(c.Request.Context(), history, func(chunk string) error {
		collected.WriteString(chunk)
		return writeSSEData(c, "token", chunk)
	})
	if err != nil {
		log.Printf("ai stream error: %v", err)
		_ = writeSSEData(c, "error", "ai request failed")
		return
	}

	assistantMsg, err := h.queries.CreateChatMessage(c.Request.Context(), db.CreateChatMessageParams{
		Uuid:       pgUUID(uuid.New()),
		ThreadUuid: pgUUID(threadUUID),
		Role:       "assistant",
		Content:    aiReply.Text,
		Emotion:    pgText(aiReply.Emotion),
	})
	if err != nil {
		log.Printf("ai store error: %v", err)
		_ = writeSSEData(c, "error", "failed to store assistant message")
		return
	}

	donePayload := gin.H{
		"assistant_message": toMessageResponse(assistantMsg),
	}
	_ = writeSSE(c, "done", donePayload)
}

func writeSSE(c *gin.Context, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writeSSEData(c, event, string(data))
}

func writeSSEData(c *gin.Context, event, data string) error {
	if event != "" {
		if _, err := c.Writer.Write([]byte("event: " + event + "\n")); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(data, "\n") {
		if _, err := c.Writer.Write([]byte("data: " + line + "\n")); err != nil {
			return err
		}
	}
	if _, err := c.Writer.Write([]byte("\n")); err != nil {
		return err
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (h *ChatHandler) resolveVisitor(ctx context.Context, c *gin.Context, visitorID string) (uuid.UUID, error) {
	info := captureVisitorInfo(c)

	if strings.TrimSpace(visitorID) != "" {
		parsed, err := uuid.Parse(visitorID)
		if err != nil {
			return uuid.UUID{}, errInvalidVisitorID
		}
		_, err = h.queries.UpdateVisitorLastSeen(ctx, db.UpdateVisitorLastSeenParams{
			Uuid:           pgUUID(parsed),
			IpAddress:      info.IPAddress,
			UserAgent:      pgText(info.UserAgent),
			AcceptLanguage: pgText(info.AcceptLanguage),
			Referer:        pgText(info.Referer),
			Host:           pgText(info.Host),
			ForwardedFor:   pgText(info.ForwardedFor),
			RawHeaders:     pgText(info.RawHeaders),
		})
		if err != nil {
			return uuid.UUID{}, err
		}
		return parsed, nil
	}

	visitorUUID := uuid.New()
	_, err := h.queries.CreateVisitor(ctx, db.CreateVisitorParams{
		Uuid:           pgUUID(visitorUUID),
		IpAddress:      info.IPAddress,
		UserAgent:      pgText(info.UserAgent),
		AcceptLanguage: pgText(info.AcceptLanguage),
		Referer:        pgText(info.Referer),
		Host:           pgText(info.Host),
		ForwardedFor:   pgText(info.ForwardedFor),
		RawHeaders:     pgText(info.RawHeaders),
	})
	if err != nil {
		return uuid.UUID{}, err
	}
	return visitorUUID, nil
}

func (h *ChatHandler) touchVisitor(ctx context.Context, visitorUUID pgtype.UUID, c *gin.Context) {
	if !visitorUUID.Valid {
		return
	}
	info := captureVisitorInfo(c)
	_, _ = h.queries.UpdateVisitorLastSeen(ctx, db.UpdateVisitorLastSeenParams{
		Uuid:           visitorUUID,
		IpAddress:      info.IPAddress,
		UserAgent:      pgText(info.UserAgent),
		AcceptLanguage: pgText(info.AcceptLanguage),
		Referer:        pgText(info.Referer),
		Host:           pgText(info.Host),
		ForwardedFor:   pgText(info.ForwardedFor),
		RawHeaders:     pgText(info.RawHeaders),
	})
}

type visitorInfo struct {
	IPAddress      string
	UserAgent      string
	AcceptLanguage string
	Referer        string
	Host           string
	ForwardedFor   string
	RawHeaders     string
}

func captureVisitorInfo(c *gin.Context) visitorInfo {
	headersCopy := map[string][]string{}
	for key, values := range c.Request.Header {
		headersCopy[key] = values
	}
	rawHeaders := ""
	if data, err := json.Marshal(headersCopy); err == nil {
		rawHeaders = string(data)
	}

	return visitorInfo{
		IPAddress:      strings.TrimSpace(c.ClientIP()),
		UserAgent:      strings.TrimSpace(c.GetHeader("User-Agent")),
		AcceptLanguage: strings.TrimSpace(c.GetHeader("Accept-Language")),
		Referer:        strings.TrimSpace(c.GetHeader("Referer")),
		Host:           strings.TrimSpace(c.Request.Host),
		ForwardedFor:   strings.TrimSpace(c.GetHeader("X-Forwarded-For")),
		RawHeaders:     rawHeaders,
	}
}
