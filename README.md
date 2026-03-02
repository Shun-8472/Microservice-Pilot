# 🚀 gRPC Microservice with LLM Integration

This is a simple **microservice** implemented with **gRPC** that interacts with an **LLM Model** to generate responses.

## 📌 Features
- **gRPC Communication**: Efficient inter-service communication using **Protocol Buffers (protobuf)**.
- **LLM Integration**: Uses **Ollama** or other models to generate responses.
- **Dependency Injection**: Managed with **Google Wire**.
- **Database Support**: Uses **Redis** and **MySQL**.

## 🔹 Installation
Before running the service, install the following dependencies:
### **1️⃣ Download Docker Images**
- [Docker](https://www.docker.com/)
    ```shell
    docker pull redis:latest
    docker pull mysql:latest
    docker pull ollama/ollama:latest
    ```
### **2️⃣ Start Redis (Docker)**
- [Redis](https://formulae.brew.sh/formula/redis)
    ```shell
    docker run -d --name redis-server -p 6379:6379 redis:latest
    ```
### **3️⃣ Start MySQL (Docker)**
- [MySQL](https://formulae.brew.sh/formula/mysql)
    ```shell
    docker run -d --name mysql-server -p 3306:3306 -e MYSQL_ROOT_PASSWORD=password -e MYSQL_DATABASE=mydb mysql:latest
    ```
### **4️⃣ Start Ollama (Docker)**
- [Ollama](https://ollama.com/)
    ```shell
    docker run -d --name ollama-server -p 11434:11434 ollama/ollama:latest
    ```
### **5️⃣ Install buf (for gRPC Protobuf management)**
- [buf](https://formulae.brew.sh/formula/buf)
  ```shell
  $ brew install buf
  $ go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  $ go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```
### **6️⃣ Install wire (for Dependency Injection)**
- [wire](https://github.com/google/wire)
    ```shell
    $ go install github.com/google/wire/cmd/wire@latest
    ```
  
## 🚀 Running the Service
You can use the Makefile to build and run the service efficiently.
### **1️⃣ Tidy up Go modules**
```shell
  make tidy
```
### **2️⃣ Generate Dependency Injection Code**
```shell
  make inject
```
### **3️⃣ Deploy the service using Docker Compose**
```shell
  make deploy
```

## 🤖 Travel Planning Agent (New)
`ChatService` now runs a simple travel-planning agent flow:
- Parse user input (destination, days, budget, interests)
- Execute callable tools:
  - `weather_lookup_tool`
  - `attraction_search_tool`
  - `budget_calc_tool`
  - `itinerary_scheduler_tool`
- Ask LLM to produce final plan (with deterministic fallback if LLM fails)
- Persist memory:
  - **Short-term session memory** in Redis
  - **Long-term preferences/history** in MySQL

### LLM provider config
Edit `/config/config.yaml`:

```yaml
llm:
  provider: "ollama"   # "ollama" | "vllm" | "openai"
  model: "mistral"
  baseurl: ""          # Example for vLLM: "http://127.0.0.1:8000/v1"
  apikey: ""           # vLLM can usually use any non-empty token
```

### Example gRPC call
```shell
grpcurl -plaintext -d '{
  "session_id":"trip-session-001",
  "user_id":"alice",
  "return_tool_results":true,
  "user_input":"Plan a 4 day trip to Tokyo for 2 people, budget $2000, we like food and culture"
}' 127.0.0.1:8080 chat.v1.ChatService/GenerateMessage
```

### Multi-turn protocol fields
`ChatRequest` now supports:
- `session_id`: keep same value across turns for Redis short-memory
- `user_id`: keep same value across sessions for MySQL long-memory
- `return_tool_results`: if `true`, response includes executed tool outputs

`ChatResponse` now includes:
- `response`: assistant text
- `response_lines[]`: line-split view for easier reading in Postman
- `session_id`, `turn_id`
- `type`: `STATUS` / `TOOL_RESULT` / `FINAL`
- `tool_results[]`: tool I/O traces (for unary and streaming events)
