package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"

	"mini/internal/applied/database/mysql"
	"mini/internal/applied/llm/ollama"
)

const (
	defaultDurationMinutes = 60
	maxSuggestionSteps     = 96
	suggestionStepMinutes  = 30
)

var (
	eventTableOnce sync.Once
	eventTableErr  error
	// fallback parser for "2026-03-05 14:00".
	dateTimeRe = regexp.MustCompile(`\b(\d{4}-\d{1,2}-\d{1,2}[ T]\d{1,2}:\d{2})\b`)
)

type parsedSchedule struct {
	Title           string `json:"title"`
	StartTime       string `json:"start_time"`
	EndTime         string `json:"end_time"`
	DurationMinutes int    `json:"duration_minutes"`
}

type scheduleEvent struct {
	ID        int64
	Title     string
	StartTime time.Time
	EndTime   time.Time
}

func (p *Procedure) GenerateMessage(ctx context.Context, userInput string) (string, error) {
	if mysql.DatabaseConnection == nil {
		return "", fmt.Errorf("database is not connected")
	}
	if err := ensureScheduleTable(ctx); err != nil {
		return "", err
	}

	start, end, title, err := parseScheduleFromInput(ctx, userInput)
	if err != nil {
		return "I need a clearer time request. Example: `Schedule a meeting from 2026-03-05 14:00 to 15:00`.", nil
	}

	conflict, hasConflict, err := findConflictEvent(ctx, mysql.DatabaseConnection, start, end)
	if err != nil {
		return "", err
	}

	if !hasConflict {
		if err := insertScheduleEvent(ctx, mysql.DatabaseConnection, title, start, end, userInput); err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"Scheduled `%s`\nTime: %s ~ %s\nStatus: Successfully added to your calendar.",
			title,
			start.Format("2006-01-02 15:04"),
			end.Format("2006-01-02 15:04"),
		), nil
	}

	duration := end.Sub(start)
	altStart, altEnd, found, err := findNextAvailableSlot(ctx, mysql.DatabaseConnection, conflict.EndTime, duration)
	if err != nil {
		return "", err
	}

	if !found {
		return fmt.Sprintf(
			"That time is already booked: `%s` (%s ~ %s).\nI could not find an available nearby slot. Please provide a preferred new time.",
			conflict.Title,
			conflict.StartTime.Format("2006-01-02 15:04"),
			conflict.EndTime.Format("2006-01-02 15:04"),
		), nil
	}

	return fmt.Sprintf(
		"That time is already booked: `%s` (%s ~ %s).\nSuggested alternative: %s ~ %s.",
		conflict.Title,
		conflict.StartTime.Format("2006-01-02 15:04"),
		conflict.EndTime.Format("2006-01-02 15:04"),
		altStart.Format("2006-01-02 15:04"),
		altEnd.Format("2006-01-02 15:04"),
	), nil
}

func (p *Procedure) StreamMessage(ctx context.Context, userInput string, onChunk func(string) error) error {
	message, err := p.GenerateMessage(ctx, userInput)
	if err != nil {
		return err
	}

	chunks := chunkByRune(message, 120)
	for _, chunk := range chunks {
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

func parseScheduleFromInput(ctx context.Context, userInput string) (time.Time, time.Time, string, error) {
	parsed, err := parseByLLM(ctx, userInput)
	if err != nil {
		return parseByRegexFallback(userInput)
	}

	start, end, title, err := normalizeParsedSchedule(parsed, userInput)
	if err != nil {
		return parseByRegexFallback(userInput)
	}
	return start, end, title, nil
}

func parseByLLM(ctx context.Context, userInput string) (parsedSchedule, error) {
	if ollama.LLMConnection == nil {
		return parsedSchedule{}, fmt.Errorf("llm is not connected")
	}

	now := time.Now().Format("2006-01-02 15:04")
	prompt := fmt.Sprintf(`You are a scheduling parser.
Current local time: %s
Extract schedule information from user text and return STRICT JSON only:
{"title":"...", "start_time":"YYYY-MM-DD HH:MM", "end_time":"YYYY-MM-DD HH:MM", "duration_minutes":60}
Rules:
- Keep title concise.
- If user gives only start time, infer end_time from duration_minutes (default 60).
- Do not output markdown or explanations.
User text: %s`, now, userInput)

	response, err := ollama.LLMConnection.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	})
	if err != nil {
		return parsedSchedule{}, err
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Content) == "" {
		return parsedSchedule{}, fmt.Errorf("empty llm parse response")
	}

	content := extractJSON(response.Choices[0].Content)
	if strings.TrimSpace(content) == "" {
		return parsedSchedule{}, fmt.Errorf("llm parse is not json")
	}

	result := parsedSchedule{}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return parsedSchedule{}, err
	}
	return result, nil
}

func parseByRegexFallback(userInput string) (time.Time, time.Time, string, error) {
	matched := dateTimeRe.FindStringSubmatch(userInput)
	if len(matched) < 2 {
		return time.Time{}, time.Time{}, "", fmt.Errorf("no datetime in fallback")
	}

	start, err := parseFlexibleDateTime(matched[1])
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}

	title := strings.TrimSpace(userInput)
	if title == "" {
		title = "New schedule"
	}

	end := start.Add(defaultDurationMinutes * time.Minute)
	return start, end, title, nil
}

func normalizeParsedSchedule(parsed parsedSchedule, rawInput string) (time.Time, time.Time, string, error) {
	start, err := parseFlexibleDateTime(parsed.StartTime)
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}

	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		title = strings.TrimSpace(rawInput)
	}
	if title == "" {
		title = "New schedule"
	}

	var end time.Time
	if strings.TrimSpace(parsed.EndTime) != "" {
		end, err = parseFlexibleDateTime(parsed.EndTime)
		if err != nil {
			return time.Time{}, time.Time{}, "", err
		}
	}

	duration := parsed.DurationMinutes
	if duration <= 0 {
		duration = defaultDurationMinutes
	}
	if end.IsZero() || !end.After(start) {
		end = start.Add(time.Duration(duration) * time.Minute)
	}

	return start, end, title, nil
}

func parseFlexibleDateTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty datetime")
	}

	layouts := []string{
		"2006-01-02 15:04",
		"2006-1-2 15:04",
		"2006-01-02T15:04",
		"2006-1-2T15:04",
		time.RFC3339,
	}

	loc := time.Local
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported datetime format: %s", value)
}

func extractJSON(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}") {
		return content
	}

	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		return content[start : end+1]
	}

	return ""
}

func ensureScheduleTable(ctx context.Context) error {
	eventTableOnce.Do(func() {
		if mysql.DatabaseConnection == nil {
			eventTableErr = fmt.Errorf("database connection is nil")
			return
		}

		_, err := mysql.DatabaseConnection.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS calendar_events (
	id BIGINT PRIMARY KEY AUTO_INCREMENT,
	title VARCHAR(255) NOT NULL,
	start_time DATETIME NOT NULL,
	end_time DATETIME NOT NULL,
	source_input TEXT NOT NULL,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	INDEX idx_event_time (start_time, end_time)
)`)
		if err != nil {
			eventTableErr = err
		}
	})

	return eventTableErr
}

func findConflictEvent(ctx context.Context, db *sql.DB, start, end time.Time) (scheduleEvent, bool, error) {
	query := `
SELECT id, title, start_time, end_time
FROM calendar_events
WHERE start_time < ? AND end_time > ?
ORDER BY start_time ASC
LIMIT 1`

	var event scheduleEvent
	err := db.QueryRowContext(ctx, query, end, start).Scan(&event.ID, &event.Title, &event.StartTime, &event.EndTime)
	if err == sql.ErrNoRows {
		return scheduleEvent{}, false, nil
	}
	if err != nil {
		return scheduleEvent{}, false, err
	}

	return event, true, nil
}

func insertScheduleEvent(ctx context.Context, db *sql.DB, title string, start, end time.Time, sourceInput string) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO calendar_events (title, start_time, end_time, source_input)
VALUES (?, ?, ?, ?)
`, title, start, end, sourceInput)
	return err
}

func findNextAvailableSlot(ctx context.Context, db *sql.DB, base time.Time, duration time.Duration) (time.Time, time.Time, bool, error) {
	step := time.Duration(suggestionStepMinutes) * time.Minute
	if duration <= 0 {
		duration = defaultDurationMinutes * time.Minute
	}

	for i := 0; i < maxSuggestionSteps; i++ {
		start := base.Add(time.Duration(i) * step)
		end := start.Add(duration)

		_, hasConflict, err := findConflictEvent(ctx, db, start, end)
		if err != nil {
			return time.Time{}, time.Time{}, false, err
		}
		if !hasConflict {
			return start, end, true, nil
		}
	}

	return time.Time{}, time.Time{}, false, nil
}

func chunkByRune(text string, size int) []string {
	if size <= 0 {
		size = 100
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= size {
		return []string{text}
	}

	result := make([]string, 0, len(runes)/size+1)
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		result = append(result, string(runes[start:end]))
	}
	return result
}
