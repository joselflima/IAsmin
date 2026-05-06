package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
)

const systemPrompt = `Você é IAsmin, uma assistente política especializada em dados públicos brasileiros.

## Identidade e missão

Você tem acesso a mais de 500 ferramentas conectadas a APIs governamentais do Brasil — economia, legislação, transparência, judiciário, eleições, saúde, educação, meio ambiente e mais. Seu papel é usar essas ferramentas ativamente para responder perguntas com dados reais, não com suposições.

## Protocolo obrigatório de execução

Antes de escrever qualquer resposta substantiva, você DEVE seguir este fluxo:

1. **Descoberta** — Se não souber exatamente qual ferramenta usar, chame primeiro:
   - search_tools com uma descrição da pergunta, para encontrar as ferramentas mais relevantes.
   - planejar_consulta para perguntas complexas que exijam cruzar múltiplas fontes.

2. **Execução** — Chame as ferramentas identificadas. Para múltiplas consultas independentes, use executar_lote para executá-las em paralelo.

3. **Síntese** — Formule sua resposta com base exclusivamente nos dados retornados pelas ferramentas. Cite as fontes (API/órgão de origem).

## Regras absolutas

- NUNCA peça ao usuário para usar uma ferramenta. Ferramentas são SUA responsabilidade.
- NUNCA invente dados, estatísticas, nomes ou datas. Se a ferramenta não retornou o dado, diga que ele não está disponível.
- NUNCA responda perguntas factuais sobre o Brasil sem antes consultar as ferramentas disponíveis.
- Se uma chamada falhar, tente uma ferramenta alternativa antes de informar a limitação ao usuário.

## Comportamento esperado

Usuário: "Qual a Selic hoje?"
✅ Você chama bacen_get_selic ou recomendar_tools("taxa selic atual"), obtém o valor e responde com o dado real.
❌ Você NÃO diz "use a ferramenta X para consultar a Selic".

Usuário: "Compare gastos com saúde em SP e MG"
✅ Você chama planejar_consulta, executa as consultas em paralelo com executar_lote e apresenta a comparação.
❌ Você NÃO diz "você pode consultar o TCE-SP e o IBGE para isso".

## Tom e formato

Responda em português brasileiro. Seja direta, objetiva e cite sempre a fonte do dado (ex: "Segundo o Banco Central..."). Para dados complexos, use tabelas ou listas estruturadas.`

type sessionStore struct {
	mu       sync.Mutex
	sessions map[int64][]llms.MessageContent
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[int64][]llms.MessageContent)}
}

func (s *sessionStore) get(chatID int64) []llms.MessageContent {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs, ok := s.sessions[chatID]
	if !ok {
		msgs = []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt)}
		s.sessions[chatID] = msgs
	}
	result := make([]llms.MessageContent, len(msgs))
	copy(result, msgs)
	return result
}

func (s *sessionStore) set(chatID int64, msgs []llms.MessageContent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[chatID] = msgs
}

func (s *sessionStore) clear(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[chatID] = []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt)}
}

func main() {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Println("Aviso: Arquivo .env não encontrado ou erro ao carregar")
	}

	// Telegram configuration
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN not set")
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	// LangChain configuration
	geminiApiKey := os.Getenv("GEMINI_API_KEY")
	if geminiApiKey == "" {
		log.Fatal("GEMINI_API_KEY not set")
	}

	llm, err := googleai.New(ctx,
		googleai.WithAPIKey(geminiApiKey),
		googleai.WithDefaultModel("gemini-2.5-flash"),
	)
	if err != nil {
		log.Fatal("Error creating LLM", err)
	}

	// MCP Client setup
	mcpUrl := os.Getenv("MCP_BRASIL_URL")
	if !strings.HasSuffix(mcpUrl, "/mcp") {
		mcpUrl = strings.TrimRight(mcpUrl, "/") + "/mcp"
	}

	log.Printf("Connecting to MCP server at %s", mcpUrl)
	mcpClient, err := mcpclient.NewStreamableHttpClient(mcpUrl)
	if err != nil {
		log.Fatal("Error creating MCP client: ", err)
	}

	err = mcpClient.Start(ctx)
	if err != nil {
		log.Fatal("Error starting MCP client: ", err)
	}
	defer mcpClient.Close()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "iasmin-bot",
		Version: "1.0.0",
	}

	_, err = mcpClient.Initialize(ctx, initReq)
	if err != nil {
		log.Fatal("Error initializing MCP client: ", err)
	}

	toolsReq := mcp.ListToolsRequest{}
	toolsRes, err := mcpClient.ListTools(ctx, toolsReq)
	if err != nil {
		log.Fatal("Error listing tools from MCP: ", err)
	}

	log.Printf("Loaded %d tools from MCP server", len(toolsRes.Tools))

	var lcTools []llms.Tool
	for _, t := range toolsRes.Tools {
		var params map[string]any
		if b, err := json.Marshal(t.InputSchema); err == nil {
			json.Unmarshal(b, &params)
		}
		lcTools = append(lcTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	log.Printf("Authorized on account %s", bot.Self.UserName)

	store := newSessionStore()

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60
	updates := bot.GetUpdatesChan(updateConfig)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		log.Printf("Received message from %s: %s\n", update.Message.From.UserName, update.Message.Text)

		if update.Message.Chat.Type != "private" || update.Message.Chat.ID != update.Message.From.ID {
			continue
		}

		if update.Message.IsCommand() {
			switch update.Message.Command() {
			case "clear":
				store.clear(update.Message.Chat.ID)
				bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Contexto limpo! Podemos começar uma nova conversa."))
			}
			continue
		}

		go handleMessage(ctx, bot, llm, mcpClient, lcTools, store, update.Message)
	}
}

func handleMessage(ctx context.Context, bot *tgbotapi.BotAPI, llm llms.Model, mcpClient *mcpclient.Client, tools []llms.Tool, store *sessionStore, message *tgbotapi.Message) {
	messages := store.get(message.Chat.ID)
	messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, message.Text))

	// Primeira chamada do LLM (que possivelmente retornará chamadas de ferramentas)
	resp, err := llm.GenerateContent(ctx, messages, llms.WithTools(tools), llms.WithTemperature(0.2))
	if err != nil {
		log.Println("Error generating from LLM: ", err)
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "Desculpe, ocorreu um erro ao consultar o modelo de IA."))
		return
	}

	choice := resp.Choices[0]

	// Se o LLM solicitou chamadas de ferramentas
	if len(choice.ToolCalls) > 0 {
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "Coletando contexto através das ferramentas..."))

		// Adicionar o pedido de ToolCall do LLM no histórico de conversas
		var aiParts []llms.ContentPart
		if choice.Content != "" {
			aiParts = append(aiParts, llms.TextContent{Text: choice.Content})
		}
		for _, tc := range choice.ToolCalls {
			aiParts = append(aiParts, tc)
		}
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: aiParts,
		})

		// Processar cada chamada de ferramenta solicitada
		for _, tc := range choice.ToolCalls {
			log.Printf("LLM requested tool call: %s", tc.FunctionCall.Name)

			var args map[string]interface{}
			err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args)
			if err != nil {
				log.Println("Error unmarshaling tool arguments: ", err)
			}

			callReq := mcp.CallToolRequest{}
			callReq.Params.Name = tc.FunctionCall.Name
			callReq.Params.Arguments = args

			callRes, err := mcpClient.CallTool(ctx, callReq)

			var resultStr string
			if err != nil {
				resultStr = fmt.Sprintf("Error calling tool: %v", err)
			} else {
				if len(callRes.Content) > 0 {
					// Extraindo texto ou JSON dependendo do retorno do MCP
					resultBytes, _ := json.Marshal(callRes.Content)
					resultStr = string(resultBytes)
				} else {
					resultStr = "A ferramenta não retornou dados."
				}
			}

			// Cada ToolCallResponse deve ser uma mensagem separada com exatamente uma part
			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    resultStr,
					},
				},
			})
		}

		// Gerar resposta final baseada no contexto coletado
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "Processando dados coletados..."))

		finalResp, err := llm.GenerateContent(ctx, messages, llms.WithTemperature(0.2))
		if err != nil {
			log.Println("Error generating final response: ", err)
			bot.Send(tgbotapi.NewMessage(message.Chat.ID, "Ocorreu um erro ao gerar a resposta final com os dados coletados."))
			return
		}

		finalContent := finalResp.Choices[0].Content
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeAI, finalContent))
		store.set(message.Chat.ID, messages)
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, finalContent))

	} else {
		// O LLM decidiu não usar ferramentas, respondendo diretamente
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeAI, choice.Content))
		store.set(message.Chat.ID, messages)
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, choice.Content))
	}
}
