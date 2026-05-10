import asyncio
import logging
import os
import signal
import sys

from dotenv import load_dotenv
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
from langchain_core.tools import BaseTool
from langchain_google_genai import ChatGoogleGenerativeAI
from langchain_mcp_adapters.tools import load_mcp_tools
from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client
from telegram import Update
from telegram.constants import ChatAction
from telegram.ext import (
    Application,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)

from .session import SessionStore

load_dotenv()

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger(__name__)

TELEGRAM_BOT_TOKEN = os.getenv("TELEGRAM_BOT_TOKEN")
GEMINI_API_KEY = os.getenv("GEMINI_API_KEY")
MCP_BRASIL_URL = os.getenv("MCP_BRASIL_URL")

if not TELEGRAM_BOT_TOKEN:
    logger.fatal("TELEGRAM_BOT_TOKEN not set")
    sys.exit(1)

if not GEMINI_API_KEY:
    logger.fatal("GEMINI_API_KEY not set")
    sys.exit(1)

if not MCP_BRASIL_URL:
    logger.fatal("MCP_BRASIL_URL not set")
    sys.exit(1)

if not MCP_BRASIL_URL.endswith("/mcp"):
    MCP_BRASIL_URL = MCP_BRASIL_URL.rstrip("/") + "/mcp"


async def send_typing_loop(chat_id: int, bot):
    while True:
        try:
            await bot.send_chat_action(chat_id=chat_id, action=ChatAction.TYPING)
            await asyncio.sleep(4)
        except asyncio.CancelledError:
            return


TELEGRAM_MAX_LENGTH = 4096


def split_message(text: str, max_length: int = TELEGRAM_MAX_LENGTH) -> list[str]:
    chunks = []
    while len(text) > max_length:
        split_at = text.rfind("\n", 0, max_length)
        if split_at == -1:
            split_at = max_length
        chunks.append(text[:split_at])
        text = text[split_at:].lstrip()
    if text:
        chunks.append(text)
    return chunks


async def send_long_message(update: Update, text: str):
    chunks = split_message(text)
    for i, chunk in enumerate(chunks):
        if i == 0:
            await update.message.reply_text(chunk)
        else:
            await update.message.reply_text(chunk, reply_to_message_id=update.message.message_id)


async def clear_command(update: Update, context: ContextTypes.DEFAULT_TYPE):
    store: SessionStore = context.bot_data["store"]
    await store.clear(update.effective_chat.id)
    await update.message.reply_text(
        "Contexto limpo! Podemos começar uma nova conversa."
    )


async def handle_message(update: Update, context: ContextTypes.DEFAULT_TYPE):
    llm: ChatGoogleGenerativeAI = context.bot_data["llm"]
    tools: list[BaseTool] = context.bot_data["tools"]
    store: SessionStore = context.bot_data["store"]

    chat_id = update.effective_chat.id
    user_text = update.message.text
    username = update.message.from_user.username or "unknown"

    logger.info("Received message from %s: %s", username, user_text)

    messages = await store.get(chat_id)
    messages.append(HumanMessage(content=user_text))

    typing_task = asyncio.create_task(
        send_typing_loop(chat_id, context.bot)
    )

    try:
        llm_with_tools = llm.bind_tools(tools)
        response = await llm_with_tools.ainvoke(messages)
    except Exception as e:
        typing_task.cancel()
        logger.error("Error generating from LLM: %s", e)
        await update.message.reply_text(
            "Desculpe, ocorreu um erro ao consultar o modelo de IA."
        )
        return
    finally:
        if not typing_task.done():
            typing_task.cancel()

    if response.tool_calls:
        messages.append(response)

        for tool_call in response.tool_calls:
            tool_name = tool_call["name"]
            tool_args = tool_call["args"]
            logger.info("LLM requested tool call: %s", tool_name)

            matching_tool = next(
                (t for t in tools if t.name == tool_name), None
            )

            if matching_tool is None:
                result_str = f"Tool '{tool_name}' not found"
            else:
                try:
                    tool_result = await matching_tool.ainvoke(tool_args)
                    result_str = str(tool_result)
                except Exception as e:
                    logger.error("Error calling tool '%s': %s", tool_name, e)
                    result_str = f"Error calling tool: {e}"

            messages.append(
                ToolMessage(content=result_str, tool_call_id=tool_call["id"])
            )

        typing_task2 = asyncio.create_task(
            send_typing_loop(chat_id, context.bot)
        )

        try:
            final_response = await llm.ainvoke(messages)
        except Exception as e:
            typing_task2.cancel()
            logger.error("Error generating final response: %s", e)
            await update.message.reply_text(
                "Ocorreu um erro ao gerar a resposta final com os dados coletados."
            )
            return
        finally:
            if not typing_task2.done():
                typing_task2.cancel()

        messages.append(AIMessage(content=final_response.content))
        await store.set(chat_id, messages)
        await send_long_message(update, final_response.content)
    else:
        messages.append(AIMessage(content=response.content))
        await store.set(chat_id, messages)
        await send_long_message(update, response.content)


async def main():
    logger.info("Connecting to MCP server at %s", MCP_BRASIL_URL)

    llm = ChatGoogleGenerativeAI(
        model="gemini-2.5-flash",
        google_api_key=GEMINI_API_KEY,
        temperature=0.2,
    )

    async with streamablehttp_client(MCP_BRASIL_URL) as (read, write, _):
        async with ClientSession(read, write) as session:
            await session.initialize()
            tools = await load_mcp_tools(session)
            logger.info("Loaded %d tools from MCP server", len(tools))

            store = SessionStore()

            app = (
                Application.builder()
                .token(TELEGRAM_BOT_TOKEN)
                .build()
            )

            app.bot_data["llm"] = llm
            app.bot_data["tools"] = tools
            app.bot_data["store"] = store

            app.add_handler(
                CommandHandler("clear", clear_command, filters.ChatType.PRIVATE)
            )
            app.add_handler(
                MessageHandler(
                    filters.TEXT & filters.ChatType.PRIVATE, handle_message
                )
            )

            await app.initialize()
            await app.start()
            await app.updater.start_polling()

            bot_info = await app.bot.get_me()
            logger.info("Authorized on account %s", bot_info.username)
            logger.info("Bot is running. Press Ctrl+C to stop.")

            stop_event = asyncio.Event()
            loop = asyncio.get_running_loop()

            def handle_signal():
                logger.info("Shutting down...")
                stop_event.set()

            for sig in (signal.SIGINT, signal.SIGTERM):
                try:
                    loop.add_signal_handler(sig, handle_signal)
                except NotImplementedError:
                    pass

            await stop_event.wait()

            logger.info("Stopping bot...")
            await app.updater.stop()
            await app.stop()
            await app.shutdown()
            logger.info("Bot stopped.")


if __name__ == "__main__":
    asyncio.run(main())
