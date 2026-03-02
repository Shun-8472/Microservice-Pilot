package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"

	redisApplied "mini/internal/applied/cache/redis"
	mysqlApplied "mini/internal/applied/database/mysql"
	appliedLLM "mini/internal/applied/llm"
	chatproc "mini/internal/processor/version/procedure/chat"
)

const (
	sessionTTL      = 24 * time.Hour
	maxSessionTurns = 20
)

var (
	destinationInToRe = regexp.MustCompile(`(?i)\b(?:to|in)\s+([A-Za-z][A-Za-z\s-]{1,40})`)
	destinationZhRe   = regexp.MustCompile(`(?:去|到)\s*([\p{Han}A-Za-z]{2,20})`)
	daysRe            = regexp.MustCompile(`(?i)(\d{1,2})\s*(?:days?|天)`)
	budgetDollarRe    = regexp.MustCompile(`(?i)(?:\$|usd\s*)(\d+(?:\.\d+)?)`)
	budgetKeywordRe   = regexp.MustCompile(`(?i)(?:budget|預算|预算)\s*(?:is|=|:)?\s*(?:usd|\$)?\s*(\d+(?:\.\d+)?)`)
	travelersRe       = regexp.MustCompile(`(?i)(\d+)\s*(?:people|persons|travelers|travellers|位|人)`)
	dateISORe         = regexp.MustCompile(`\b(20\d{2}-\d{1,2}-\d{1,2})\b`)

	mysqlTableOnce sync.Once
	mysqlTableErr  error
)

type sessionTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type userPreferences struct {
	PreferredDestination string
	TypicalBudgetUSD     float64
	PreferredInterests   []string
}

type travelRequest struct {
	OriginalInput string
	IsTravel      bool
	Destination   string
	Days          int
	BudgetUSD     float64
	Travelers     int
	Interests     []string
	StartDate     string
	MissingFields []string
}

type toolState struct {
	Request     travelRequest
	ToolOutputs map[string]string
}

type callableTool interface {
	Name() string
	Input(state toolState) string
	Run(context.Context, toolState) (string, error)
}

type weatherTool struct{}
type attractionsTool struct{}
type budgetTool struct{}
type itineraryTool struct{}

func (p *Procedure) GenerateMessage(ctx context.Context, request chatproc.Request) (chatproc.Response, error) {
	request = normalizeRequest(request)
	turnID := nextTurnID(ctx, request.SessionID)

	shortTurns := loadSessionTurns(ctx, request.SessionID, maxSessionTurns)
	prefs := loadUserPreferences(ctx, request.UserID)
	longTurns := loadLongTermHistory(ctx, request.UserID, 20)

	saveTurn(ctx, request.SessionID, request.UserID, "user", request.UserInput)

	parsed := parseTravelRequest(request.UserInput)
	parsed = applyPreferences(parsed, prefs)

	toolResults := []chatproc.ToolExecutionResult{}
	if parsed.IsTravel {
		toolResults = runToolChain(ctx, parsed)
	}

	finalResponse := buildAssistantResponse(ctx, request, parsed, shortTurns, longTurns, prefs, toolResults, nil)

	saveTurn(ctx, request.SessionID, request.UserID, "assistant", finalResponse)
	if parsed.IsTravel {
		updateUserPreferences(ctx, request.UserID, parsed)
	}

	return chatproc.Response{
		Response:   finalResponse,
		SessionID:  request.SessionID,
		TurnID:     turnID,
		Type:       chatproc.ResponseTypeFinal,
		ToolResult: toolResults,
	}, nil
}

func (p *Procedure) StreamMessage(ctx context.Context, request chatproc.Request, onEvent func(chatproc.StreamEvent) error) error {
	request = normalizeRequest(request)
	turnID := nextTurnID(ctx, request.SessionID)

	if onEvent != nil {
		if err := onEvent(chatproc.StreamEvent{
			Response:  "Session loaded. Running planner.",
			SessionID: request.SessionID,
			TurnID:    turnID,
			Type:      chatproc.ResponseTypeStatus,
		}); err != nil {
			return err
		}
	}

	shortTurns := loadSessionTurns(ctx, request.SessionID, maxSessionTurns)
	prefs := loadUserPreferences(ctx, request.UserID)
	longTurns := loadLongTermHistory(ctx, request.UserID, 20)

	saveTurn(ctx, request.SessionID, request.UserID, "user", request.UserInput)

	parsed := parseTravelRequest(request.UserInput)
	parsed = applyPreferences(parsed, prefs)

	toolResults := []chatproc.ToolExecutionResult{}
	if parsed.IsTravel {
		toolResults = runToolChain(ctx, parsed)
		if request.ReturnToolResults && onEvent != nil {
			for i := range toolResults {
				result := toolResults[i]
				if err := onEvent(chatproc.StreamEvent{
					Response:   fmt.Sprintf("%s completed", result.ToolName),
					SessionID:  request.SessionID,
					TurnID:     turnID,
					Type:       chatproc.ResponseTypeToolResult,
					ToolResult: &result,
				}); err != nil {
					return err
				}
			}
		}
	}

	finalBuffer := strings.Builder{}
	streamFinal := func(chunk string) error {
		finalBuffer.WriteString(chunk)
		if onEvent == nil {
			return nil
		}
		return onEvent(chatproc.StreamEvent{
			Response:  chunk,
			SessionID: request.SessionID,
			TurnID:    turnID,
			Type:      chatproc.ResponseTypeFinal,
		})
	}

	finalResponse := buildAssistantResponse(ctx, request, parsed, shortTurns, longTurns, prefs, toolResults, streamFinal)
	if strings.TrimSpace(finalBuffer.String()) == "" && onEvent != nil {
		if err := streamByChunks(finalResponse, streamFinal); err != nil {
			return err
		}
	}

	saveTurn(ctx, request.SessionID, request.UserID, "assistant", finalResponse)
	if parsed.IsTravel {
		updateUserPreferences(ctx, request.UserID, parsed)
	}

	return nil
}

func normalizeRequest(request chatproc.Request) chatproc.Request {
	request.SessionID = strings.TrimSpace(request.SessionID)
	if request.SessionID == "" {
		request.SessionID = fmt.Sprintf("s_%d", time.Now().UnixNano())
	}

	request.UserID = strings.TrimSpace(request.UserID)
	if request.UserID == "" {
		request.UserID = request.SessionID
	}

	return request
}

func buildAssistantResponse(
	ctx context.Context,
	request chatproc.Request,
	parsed travelRequest,
	shortTurns []sessionTurn,
	longTurns []sessionTurn,
	prefs userPreferences,
	toolResults []chatproc.ToolExecutionResult,
	onChunk func(string) error,
) string {
	if !parsed.IsTravel {
		return buildGeneralResponse(ctx, request.UserInput, shortTurns, longTurns, onChunk)
	}

	if len(parsed.MissingFields) > 0 {
		return buildMissingFieldsPrompt(parsed)
	}

	fallback := composeDeterministicPlan(parsed, toolResults)
	prompt := buildTravelPrompt(request.UserInput, parsed, shortTurns, longTurns, prefs, toolResults)
	response, err := generateWithLLM(ctx, prompt, onChunk, llms.TextParts(llms.ChatMessageTypeSystem, travelSystemPrompt()))
	if err != nil || strings.TrimSpace(response) == "" {
		return fallback
	}

	return strings.TrimSpace(response)
}

func buildGeneralResponse(ctx context.Context, userInput string, shortTurns, longTurns []sessionTurn, onChunk func(string) error) string {
	if appliedLLM.Connection == nil {
		return "Travel mode is available. Please provide destination + days + budget."
	}

	shortText := buildShortMemoryContext(shortTurns)
	longText := buildLongMemoryContext(longTurns)
	prompt := "Recent context (Redis):\n" + shortText + "\n\nLong-term history (MySQL):\n" + longText + "\n\nUser input:\n" + userInput + "\n\nReply in the same language."
	response, err := generateWithLLM(ctx, prompt, onChunk, llms.TextParts(llms.ChatMessageTypeSystem, generalSystemPrompt()))
	if err != nil || strings.TrimSpace(response) == "" {
		return "Travel mode is available. Please provide destination + days + budget."
	}

	return strings.TrimSpace(response)
}

func buildMissingFieldsPrompt(parsed travelRequest) string {
	questions := make([]string, 0, len(parsed.MissingFields))
	for _, field := range parsed.MissingFields {
		switch field {
		case "destination":
			questions = append(questions, "- Which destination do you want to visit?")
		case "trip length (days)":
			questions = append(questions, "- How many days should the trip be?")
		default:
			questions = append(questions, "- Please provide "+field+".")
		}
	}
	return "I need a few details before generating the plan:\n" + strings.Join(questions, "\n")
}

func travelSystemPrompt() string {
	return "You are a travel planning agent. Produce practical itineraries with clear schedule, budget guidance, and risk mitigation. Keep the answer concise."
}

func generalSystemPrompt() string {
	return "You are a practical assistant. For travel asks, request missing details and then provide actionable plans."
}

func parseTravelRequest(input string) travelRequest {
	lowerInput := strings.ToLower(input)
	req := travelRequest{
		OriginalInput: input,
		IsTravel:      detectTravelIntent(lowerInput),
		Travelers:     1,
	}

	req.Destination = extractDestination(input)
	req.Days = extractDays(input)
	req.BudgetUSD = extractBudget(input)
	req.Travelers = extractTravelers(input)
	req.Interests = extractInterests(lowerInput)
	req.StartDate = extractStartDate(input)

	if req.Destination != "" && req.Days > 0 {
		req.IsTravel = true
	}
	if req.IsTravel {
		if req.Destination == "" {
			req.MissingFields = append(req.MissingFields, "destination")
		}
		if req.Days <= 0 {
			req.MissingFields = append(req.MissingFields, "trip length (days)")
		}
	}

	return req
}

func applyPreferences(req travelRequest, prefs userPreferences) travelRequest {
	if req.Destination == "" && prefs.PreferredDestination != "" {
		req.Destination = prefs.PreferredDestination
	}
	if req.BudgetUSD <= 0 && prefs.TypicalBudgetUSD > 0 {
		req.BudgetUSD = prefs.TypicalBudgetUSD
	}
	if len(req.Interests) == 0 && len(prefs.PreferredInterests) > 0 {
		req.Interests = prefs.PreferredInterests
	}

	req.MissingFields = req.MissingFields[:0]
	if req.IsTravel {
		if req.Destination == "" {
			req.MissingFields = append(req.MissingFields, "destination")
		}
		if req.Days <= 0 {
			req.MissingFields = append(req.MissingFields, "trip length (days)")
		}
	}

	return req
}

func detectTravelIntent(lowerInput string) bool {
	keywords := []string{
		"travel", "trip", "itinerary", "vacation", "journey", "plan",
		"旅遊", "旅游", "旅行", "行程", "自由行",
	}
	for _, keyword := range keywords {
		if strings.Contains(lowerInput, keyword) {
			return true
		}
	}
	return false
}

func extractDestination(input string) string {
	if matched := destinationInToRe.FindStringSubmatch(input); len(matched) > 1 {
		return cleanDestination(matched[1])
	}
	if matched := destinationZhRe.FindStringSubmatch(input); len(matched) > 1 {
		return strings.TrimSpace(matched[1])
	}
	return ""
}

func cleanDestination(raw string) string {
	raw = strings.TrimSpace(strings.Trim(raw, " ,.;:!?\"'()[]"))
	if raw == "" {
		return ""
	}

	stopWords := map[string]struct{}{
		"for": {}, "with": {}, "from": {}, "on": {}, "at": {}, "next": {},
		"this": {}, "trip": {}, "travel": {}, "vacation": {}, "and": {},
	}

	parts := strings.Fields(raw)
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		word := strings.ToLower(strings.Trim(part, " ,.;:!?\"'()[]"))
		if _, stop := stopWords[word]; stop {
			break
		}
		filtered = append(filtered, strings.Trim(part, " ,.;:!?\"'()[]"))
		if len(filtered) >= 4 {
			break
		}
	}

	return strings.TrimSpace(strings.Join(filtered, " "))
}

func extractDays(input string) int {
	matched := daysRe.FindStringSubmatch(input)
	if len(matched) <= 1 {
		return 0
	}
	days, err := strconv.Atoi(matched[1])
	if err != nil || days <= 0 {
		return 0
	}
	return days
}

func extractBudget(input string) float64 {
	for _, re := range []*regexp.Regexp{budgetDollarRe, budgetKeywordRe} {
		matched := re.FindStringSubmatch(input)
		if len(matched) <= 1 {
			continue
		}
		value, err := strconv.ParseFloat(matched[1], 64)
		if err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func extractTravelers(input string) int {
	matched := travelersRe.FindStringSubmatch(input)
	if len(matched) <= 1 {
		return 1
	}
	value, err := strconv.Atoi(matched[1])
	if err != nil || value <= 0 {
		return 1
	}
	return value
}

func extractStartDate(input string) string {
	matched := dateISORe.FindStringSubmatch(input)
	if len(matched) <= 1 {
		return ""
	}
	return matched[1]
}

func extractInterests(lowerInput string) []string {
	type mapping struct {
		name     string
		keywords []string
	}
	mappings := []mapping{
		{name: "food", keywords: []string{"food", "eat", "restaurant", "美食", "吃"}},
		{name: "culture", keywords: []string{"culture", "museum", "history", "藝術", "文化", "博物館"}},
		{name: "nature", keywords: []string{"nature", "hiking", "mountain", "beach", "戶外", "健行", "自然"}},
		{name: "shopping", keywords: []string{"shopping", "mall", "market", "購物"}},
		{name: "nightlife", keywords: []string{"nightlife", "bar", "club", "夜生活"}},
		{name: "sport", keywords: []string{"snowbording", "ski"}},
	}

	interests := make([]string, 0, len(mappings))
	for _, m := range mappings {
		for _, keyword := range m.keywords {
			if strings.Contains(lowerInput, keyword) {
				interests = append(interests, m.name)
				break
			}
		}
	}

	if len(interests) == 0 {
		return []string{"culture", "food", "nature"}
	}
	return interests
}

func runToolChain(ctx context.Context, req travelRequest) []chatproc.ToolExecutionResult {
	tools := []callableTool{
		weatherTool{},
		attractionsTool{},
		budgetTool{},
		itineraryTool{},
	}

	state := toolState{
		Request:     req,
		ToolOutputs: map[string]string{},
	}

	results := make([]chatproc.ToolExecutionResult, 0, len(tools))
	for _, tool := range tools {
		output, err := tool.Run(ctx, state)
		success := err == nil
		if !success {
			output = "error: " + err.Error()
		}

		state.ToolOutputs[tool.Name()] = output
		results = append(results, chatproc.ToolExecutionResult{
			ToolName:   tool.Name(),
			ToolInput:  tool.Input(state),
			ToolOutput: output,
			Success:    success,
		})
	}

	return results
}

func (weatherTool) Name() string { return "weather_lookup_tool" }
func (weatherTool) Input(state toolState) string {
	return fmt.Sprintf("destination=%s,start_date=%s,input=%s", state.Request.Destination, state.Request.StartDate, state.Request.OriginalInput)
}
func (weatherTool) Run(_ context.Context, state toolState) (string, error) {
	season := inferSeason(state.Request.OriginalInput, state.Request.StartDate)
	tip := map[string]string{
		"winter": "Likely cold weather. Prioritize indoor attractions and layered clothing.",
		"summer": "Likely warm weather. Prioritize hydration and avoid long noon outdoor blocks.",
		"mild":   "Mild weather expected. Keep one flexible indoor backup slot each day.",
	}
	return fmt.Sprintf("Weather heuristic for %s: %s", state.Request.Destination, tip[season]), nil
}

func (attractionsTool) Name() string { return "attraction_search_tool" }
func (attractionsTool) Input(state toolState) string {
	return fmt.Sprintf("destination=%s,interests=%s", state.Request.Destination, strings.Join(state.Request.Interests, ","))
}
func (attractionsTool) Run(_ context.Context, state toolState) (string, error) {
	city := strings.ToLower(strings.TrimSpace(state.Request.Destination))
	suggestions := map[string][]string{
		"tokyo":         {"Senso-ji", "Meiji Shrine", "Ueno Park", "Tsukiji Outer Market", "Shibuya"},
		"osaka":         {"Osaka Castle", "Dotonbori", "Shinsekai", "Kuromon Market", "Umeda Sky Building"},
		"kyoto":         {"Fushimi Inari", "Kiyomizu-dera", "Arashiyama", "Nishiki Market", "Gion"},
		"taipei":        {"Taipei 101", "National Palace Museum", "Shilin Night Market", "Elephant Mountain", "Beitou"},
		"seoul":         {"Gyeongbokgung", "Bukchon Hanok Village", "Myeongdong", "N Seoul Tower", "Hongdae"},
		"singapore":     {"Gardens by the Bay", "Marina Bay", "Sentosa", "Hawker Centres", "Chinatown"},
		"paris":         {"Louvre", "Eiffel Tower", "Montmartre", "Seine Walk", "Le Marais"},
		"london":        {"British Museum", "Tower Bridge", "Covent Garden", "South Bank", "Notting Hill"},
		"new york":      {"Central Park", "MoMA", "Brooklyn Bridge", "Chelsea Market", "Broadway"},
		"san francisco": {"Golden Gate Bridge", "Ferry Building", "Alcatraz", "Mission District", "Lands End"},
	}

	items := []string{"Main Square", "Historic District", "City Museum", "Local Food Market", "Riverside Walk"}
	for key, values := range suggestions {
		if strings.Contains(city, key) {
			items = values
			break
		}
	}
	return strings.Join(items, ", "), nil
}

func (budgetTool) Name() string { return "budget_calc_tool" }
func (budgetTool) Input(state toolState) string {
	return fmt.Sprintf("budget=%.0f,days=%d,travelers=%d,destination=%s", state.Request.BudgetUSD, state.Request.Days, state.Request.Travelers, state.Request.Destination)
}
func (budgetTool) Run(_ context.Context, state toolState) (string, error) {
	totalBudget := state.Request.BudgetUSD
	estimated := false
	if totalBudget <= 0 {
		totalBudget = estimateBudgetUSD(state.Request.Destination, state.Request.Days, state.Request.Travelers)
		estimated = true
	}

	days := state.Request.Days
	if days <= 0 {
		days = 1
	}

	perDay := totalBudget / float64(days)
	output := fmt.Sprintf("total=%.0f USD; per_day=%.0f USD; split: stay %.0f / food %.0f / transport %.0f / activities %.0f / buffer %.0f",
		totalBudget,
		perDay,
		totalBudget*0.35,
		totalBudget*0.25,
		totalBudget*0.20,
		totalBudget*0.15,
		totalBudget*0.05,
	)
	if estimated {
		output += "; note=estimated_budget"
	}
	return output, nil
}

func (itineraryTool) Name() string { return "itinerary_scheduler_tool" }
func (itineraryTool) Input(state toolState) string {
	return fmt.Sprintf("destination=%s,days=%d,interests=%s", state.Request.Destination, state.Request.Days, strings.Join(state.Request.Interests, ","))
}
func (itineraryTool) Run(_ context.Context, state toolState) (string, error) {
	days := state.Request.Days
	if days <= 0 {
		days = 3
	}
	if days > 7 {
		days = 7
	}

	attractionsRaw := state.ToolOutputs["attraction_search_tool"]
	attractions := strings.Split(attractionsRaw, ",")
	for i := range attractions {
		attractions[i] = strings.TrimSpace(attractions[i])
	}

	var builder strings.Builder
	for day := 1; day <= days; day++ {
		a1 := attractions[(day-1)%len(attractions)]
		a2 := attractions[(day)%len(attractions)]
		builder.WriteString(fmt.Sprintf("Day %d: Morning %s; Afternoon %s; Evening local food district\n", day, a1, a2))
	}
	return strings.TrimSpace(builder.String()), nil
}

func estimateBudgetUSD(destination string, days, travelers int) float64 {
	if days <= 0 {
		days = 3
	}
	if travelers <= 0 {
		travelers = 1
	}

	dailyBaseline := 170.0
	cityCostIndex := map[string]float64{
		"tokyo":         220,
		"osaka":         200,
		"kyoto":         190,
		"taipei":        140,
		"seoul":         190,
		"bangkok":       110,
		"singapore":     230,
		"paris":         280,
		"london":        290,
		"new york":      320,
		"san francisco": 300,
	}

	lowerDestination := strings.ToLower(strings.TrimSpace(destination))
	for city, baseline := range cityCostIndex {
		if strings.Contains(lowerDestination, city) {
			dailyBaseline = baseline
			break
		}
	}

	return math.Round(dailyBaseline * float64(days) * (0.75 + 0.25*float64(travelers)))
}

func inferSeason(input, startDate string) string {
	lowerInput := strings.ToLower(input)
	switch {
	case strings.Contains(lowerInput, "winter"), strings.Contains(lowerInput, "dec"), strings.Contains(lowerInput, "jan"), strings.Contains(lowerInput, "feb"):
		return "winter"
	case strings.Contains(lowerInput, "summer"), strings.Contains(lowerInput, "jun"), strings.Contains(lowerInput, "jul"), strings.Contains(lowerInput, "aug"):
		return "summer"
	}

	if strings.HasPrefix(startDate, "20") {
		parts := strings.Split(startDate, "-")
		if len(parts) >= 2 {
			month, err := strconv.Atoi(parts[1])
			if err == nil {
				switch month {
				case 12, 1, 2:
					return "winter"
				case 6, 7, 8:
					return "summer"
				}
			}
		}
	}
	return "mild"
}

func buildTravelPrompt(
	userInput string,
	req travelRequest,
	shortTurns []sessionTurn,
	longTurns []sessionTurn,
	prefs userPreferences,
	toolResults []chatproc.ToolExecutionResult,
) string {
	var builder strings.Builder
	builder.WriteString("User input:\n")
	builder.WriteString(userInput)
	builder.WriteString("\n\nSession short-memory (Redis):\n")
	builder.WriteString(buildShortMemoryContext(shortTurns))
	builder.WriteString("\n\nLong-term history (MySQL):\n")
	builder.WriteString(buildLongMemoryContext(longTurns))
	builder.WriteString("\n\nLong-term memory (MySQL preferences):\n")
	builder.WriteString(fmt.Sprintf("- preferred_destination: %s\n", safeString(prefs.PreferredDestination, "none")))
	builder.WriteString(fmt.Sprintf("- typical_budget_usd: %.0f\n", prefs.TypicalBudgetUSD))
	builder.WriteString(fmt.Sprintf("- preferred_interests: %s\n", strings.Join(prefs.PreferredInterests, ", ")))

	builder.WriteString("\nParsed request:\n")
	builder.WriteString(fmt.Sprintf("- destination: %s\n", req.Destination))
	builder.WriteString(fmt.Sprintf("- days: %d\n", req.Days))
	builder.WriteString(fmt.Sprintf("- budget_usd: %.0f\n", req.BudgetUSD))
	builder.WriteString(fmt.Sprintf("- travelers: %d\n", req.Travelers))
	builder.WriteString(fmt.Sprintf("- interests: %s\n", strings.Join(req.Interests, ", ")))

	builder.WriteString("\nTool outputs:\n")
	for _, result := range toolResults {
		builder.WriteString(fmt.Sprintf("[%s] %s\n", result.ToolName, result.ToolOutput))
	}

	builder.WriteString("\nWrite the final travel plan with:\n")
	builder.WriteString("1) Day-by-day schedule\n")
	builder.WriteString("2) Budget and optimization tips\n")
	builder.WriteString("3) Risks and mitigations\n")
	builder.WriteString("4) Next clarifying question only if necessary\n")

	return builder.String()
}

func composeDeterministicPlan(req travelRequest, toolResults []chatproc.ToolExecutionResult) string {
	var builder strings.Builder
	builder.WriteString("Travel Planning Draft\n")
	builder.WriteString(fmt.Sprintf("- Destination: %s\n", safeString(req.Destination, "not set")))
	builder.WriteString(fmt.Sprintf("- Duration: %d days\n", req.Days))
	builder.WriteString(fmt.Sprintf("- Budget: %.0f USD\n", req.BudgetUSD))
	builder.WriteString(fmt.Sprintf("- Travelers: %d\n\n", req.Travelers))

	for _, result := range toolResults {
		builder.WriteString(fmt.Sprintf("[%s]\n%s\n\n", result.ToolName, result.ToolOutput))
	}
	builder.WriteString("Reply with updates and I will refine the itinerary.")
	return strings.TrimSpace(builder.String())
}

func buildShortMemoryContext(turns []sessionTurn) string {
	if len(turns) == 0 {
		return "No recent turns."
	}

	var builder strings.Builder
	for _, turn := range turns {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", turn.Role, turn.Content))
	}
	return strings.TrimSpace(builder.String())
}

func buildLongMemoryContext(turns []sessionTurn) string {
	if len(turns) == 0 {
		return "No long-term history."
	}

	var builder strings.Builder
	for _, turn := range turns {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", turn.Role, turn.Content))
	}
	return strings.TrimSpace(builder.String())
}

func saveTurn(ctx context.Context, sessionID, userID, role, content string) {
	saveTurnToRedis(ctx, sessionID, role, content)
	saveTurnToMySQL(ctx, sessionID, userID, role, content)
}

func loadSessionTurns(ctx context.Context, sessionID string, limit int) []sessionTurn {
	client := redisApplied.CacheConnection
	if client == nil {
		return nil
	}

	key := sessionTurnsKey(sessionID)
	rows, err := client.LRange(key, int64(-limit), -1).Result()
	if err != nil {
		return nil
	}

	turns := make([]sessionTurn, 0, len(rows))
	for _, row := range rows {
		var turn sessionTurn
		if err := json.Unmarshal([]byte(row), &turn); err == nil {
			turns = append(turns, turn)
		}
	}
	return turns
}

func saveTurnToRedis(ctx context.Context, sessionID, role, content string) {
	client := redisApplied.CacheConnection
	if client == nil {
		return
	}

	turn := sessionTurn{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	payload, err := json.Marshal(turn)
	if err != nil {
		return
	}

	key := sessionTurnsKey(sessionID)
	if err := client.RPush(key, payload).Err(); err != nil {
		return
	}
	client.LTrim(key, -maxSessionTurns, -1)
	client.Expire(key, sessionTTL)
}

func nextTurnID(ctx context.Context, sessionID string) int64 {
	client := redisApplied.CacheConnection
	if client != nil {
		if value, err := client.Incr(sessionTurnCounterKey(sessionID)).Result(); err == nil {
			client.Expire(sessionTurnCounterKey(sessionID), sessionTTL)
			return value
		}
	}
	return time.Now().UnixNano()
}

func sessionTurnsKey(sessionID string) string {
	return "chat:session:" + sessionID + ":turns"
}

func sessionTurnCounterKey(sessionID string) string {
	return "chat:session:" + sessionID + ":turn_counter"
}

func ensureMySQLTables(ctx context.Context) error {
	mysqlTableOnce.Do(func() {
		db := mysqlApplied.DatabaseConnection
		if db == nil {
			mysqlTableErr = fmt.Errorf("mysql connection is nil")
			return
		}

		if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS chat_history (
	id BIGINT PRIMARY KEY AUTO_INCREMENT,
	session_id VARCHAR(128) NOT NULL,
	user_id VARCHAR(128) NOT NULL,
	role VARCHAR(32) NOT NULL,
	content TEXT NOT NULL,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	INDEX idx_user_created (user_id, created_at)
)`); err != nil {
			mysqlTableErr = err
			return
		}

		if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS user_preferences (
	user_id VARCHAR(128) NOT NULL,
	pref_key VARCHAR(128) NOT NULL,
	pref_value TEXT NOT NULL,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
	PRIMARY KEY (user_id, pref_key)
)`); err != nil {
			mysqlTableErr = err
			return
		}
	})

	return mysqlTableErr
}

func saveTurnToMySQL(ctx context.Context, sessionID, userID, role, content string) {
	db := mysqlApplied.DatabaseConnection
	if db == nil {
		return
	}
	if err := ensureMySQLTables(ctx); err != nil {
		return
	}

	_, _ = db.ExecContext(
		ctx,
		`INSERT INTO chat_history (session_id, user_id, role, content) VALUES (?, ?, ?, ?)`,
		sessionID, userID, role, content,
	)
}

func loadUserPreferences(ctx context.Context, userID string) userPreferences {
	prefs := userPreferences{}
	db := mysqlApplied.DatabaseConnection
	if db == nil || strings.TrimSpace(userID) == "" {
		return prefs
	}
	if err := ensureMySQLTables(ctx); err != nil {
		return prefs
	}

	rows, err := db.QueryContext(ctx, `SELECT pref_key, pref_value FROM user_preferences WHERE user_id = ?`, userID)
	if err != nil {
		return prefs
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		switch key {
		case "preferred_destination":
			prefs.PreferredDestination = value
		case "typical_budget_usd":
			prefs.TypicalBudgetUSD, _ = strconv.ParseFloat(value, 64)
		case "preferred_interests":
			if strings.TrimSpace(value) != "" {
				prefs.PreferredInterests = strings.Split(value, ",")
				for i := range prefs.PreferredInterests {
					prefs.PreferredInterests[i] = strings.TrimSpace(prefs.PreferredInterests[i])
				}
			}
		}
	}

	return prefs
}

func loadLongTermHistory(ctx context.Context, userID string, limit int) []sessionTurn {
	db := mysqlApplied.DatabaseConnection
	if db == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	if err := ensureMySQLTables(ctx); err != nil {
		return nil
	}

	rows, err := db.QueryContext(ctx, `
SELECT role, content, created_at
FROM chat_history
WHERE user_id = ?
ORDER BY id DESC
LIMIT ?
`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := []sessionTurn{}
	for rows.Next() {
		var role, content string
		var createdAt time.Time
		if err := rows.Scan(&role, &content, &createdAt); err != nil {
			continue
		}
		result = append(result, sessionTurn{
			Role:      role,
			Content:   content,
			CreatedAt: createdAt.Format(time.RFC3339),
		})
	}

	// reverse to chronological order
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

func updateUserPreferences(ctx context.Context, userID string, req travelRequest) {
	if strings.TrimSpace(userID) == "" {
		return
	}
	db := mysqlApplied.DatabaseConnection
	if db == nil {
		return
	}
	if err := ensureMySQLTables(ctx); err != nil {
		return
	}

	upsertPreference(ctx, db, userID, "preferred_destination", req.Destination)
	if req.BudgetUSD > 0 {
		upsertPreference(ctx, db, userID, "typical_budget_usd", fmt.Sprintf("%.0f", req.BudgetUSD))
	}
	if len(req.Interests) > 0 {
		upsertPreference(ctx, db, userID, "preferred_interests", strings.Join(req.Interests, ","))
	}
}

func upsertPreference(ctx context.Context, db *sql.DB, userID, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	_, _ = db.ExecContext(ctx, `
INSERT INTO user_preferences (user_id, pref_key, pref_value)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE pref_value = VALUES(pref_value)
`, userID, key, value)
}

func generateWithLLM(ctx context.Context, prompt string, onChunk func(string) error, prefix ...llms.MessageContent) (string, error) {
	if appliedLLM.Connection == nil {
		return "", fmt.Errorf("llm connection is not initialized")
	}

	messages := make([]llms.MessageContent, 0, len(prefix)+1)
	messages = append(messages, prefix...)
	if len(prefix) == 0 {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeSystem, travelSystemPrompt()))
	}
	messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, prompt))

	callOptions := []llms.CallOption{llms.WithTemperature(0.2)}
	if onChunk != nil {
		callOptions = append(callOptions, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
			text := string(chunk)
			if strings.TrimSpace(text) == "" {
				return nil
			}
			return onChunk(text)
		}))
		_, err := appliedLLM.Connection.GenerateContent(ctx, messages, callOptions...)
		return "", err
	}

	result, err := appliedLLM.Connection.GenerateContent(ctx, messages, callOptions...)
	if err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", nil
	}
	return strings.TrimSpace(result.Choices[0].Content), nil
}

func streamByChunks(text string, onChunk func(string) error) error {
	if onChunk == nil {
		return nil
	}

	runes := []rune(text)
	const chunkSize = 140
	for start := 0; start < len(runes); start += chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		if err := onChunk(string(runes[start:end])); err != nil {
			return err
		}
	}
	return nil
}

func safeString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
