# IAsmin - Assessora Política Virtual

IAsmin é uma assistente virtual integrada ao Telegram projetada para atuar como uma assessora de política. Seu foco é fornecer informações precisas sobre dados do Brasil (como economia, legislação e dados abertos), utilizando IA de ponta e consultas dinâmicas a ferramentas.

## Funcionalidades Principais

- **Bot do Telegram**: Interação fluida com os usuários diretamente pelo Telegram.
- **Inteligência Artificial (Groq)**: Utiliza o modelo de linguagem `llama-3.1-8b-instant` via LangChain e a API da Groq, garantindo respostas rápidas e de alta qualidade.
- **Integração com MCP (Model Context Protocol)**: Conecta-se a um servidor MCP (MCP-Brasil) para obter ferramentas em tempo real.
- **Agente Autônomo e Preciso**: Antes de responder, a IA avalia se precisa de contexto adicional. Ela pode chamar automaticamente as ferramentas disponibilizadas pelo servidor MCP para buscar e coletar dados oficiais ou legislações antes de entregar a resposta final ao usuário.

## Pré-requisitos

Para executar o projeto, você precisará das seguintes variáveis de ambiente. Crie um arquivo `.env` na raiz do projeto com o seguinte formato:

```env
TELEGRAM_BOT_TOKEN=seu_token_do_telegram_aqui
GROQ_API_KEY=sua_chave_de_api_da_groq_aqui
MCP_BRASIL_URL=url_do_servidor_mcp_brasil_aqui
```

## Como Rodar

O projeto é escrito em Go. Para compilá-lo e executá-lo, siga os passos abaixo:

1. Certifique-se de ter o [Go](https://go.dev/) instalado na sua máquina.
2. Instale as dependências:
   ```bash
   go mod tidy
   ```
3. Compile e execute o bot:
   ```bash
   go build -o ./src/iasmin ./src/main.go
   ./src/iasmin
   ```

Após esses passos, o bot conectará ao Telegram, registrará as ferramentas do servidor MCP e estará pronto para responder suas mensagens!
