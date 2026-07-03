package localserver

import "net/http"

// --- Conversations ---

// @Summary      Create conversation
// @Description  Proxies to the agent backend to create a new conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations [post]
func (s *Server) handleConversationCreate(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      List conversations
// @Description  Proxies to the agent backend to list conversations.
// @Tags         Conversation
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations [get]
func (s *Server) handleConversationList(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation status
// @Description  Proxies to the agent backend to get overall conversation status.
// @Tags         Conversation
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/status [get]
func (s *Server) handleConversationStatus(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation
// @Description  Proxies to the agent backend to retrieve a specific conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id} [get]
func (s *Server) handleConversationGet(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Update conversation
// @Description  Proxies to the agent backend to update a specific conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id} [patch]
func (s *Server) handleConversationUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Delete conversation
// @Description  Proxies to the agent backend to delete a specific conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id} [delete]
func (s *Server) handleConversationDelete(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Send prompt
// @Description  Proxies to the agent backend to send a prompt to a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/prompt [post]
func (s *Server) handleConversationPrompt(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Send prompt asynchronously
// @Description  Proxies to the agent backend to send a prompt asynchronously to a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/prompt/async [post]
func (s *Server) handleConversationPromptAsync(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Abort conversation
// @Description  Proxies to the agent backend to abort an ongoing conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/abort [post]
func (s *Server) handleConversationAbort(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation messages
// @Description  Proxies to the agent backend to retrieve messages for a conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/messages [get]
func (s *Server) handleConversationMessages(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation todo
// @Description  Proxies to the agent backend to retrieve the todo list for a conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/todo [get]
func (s *Server) handleConversationTodo(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation tasks
// @Description  Proxies to the agent backend to retrieve task history for a conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/tasks [get]
func (s *Server) handleConversationTasks(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Execute shell command
// @Description  Proxies to the agent backend to execute a shell command in a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/shell [post]
func (s *Server) handleConversationShell(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Execute conversation command
// @Description  Proxies to the agent backend to execute a command in a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/command [post]
func (s *Server) handleConversationCommand(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Execute conversation command asynchronously
// @Description  Proxies to the agent backend to execute a command asynchronously in a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/command/async [post]
func (s *Server) handleConversationCommandAsync(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// --- Events ---
// handleEvents is implemented in handle_events.go (merged SSE: backend proxy + host events)

// --- Config ---

// @Summary      Get agent config
// @Description  Proxies to the agent backend to retrieve supported config descriptors and current values.
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      503  {object}  envelope
// @Router       /agents/config [get]
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Patch agent config
// @Description  Proxies to the agent backend to update supported config values.
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      503  {object}  envelope
// @Router       /agents/config [patch]
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get model config
// @Description  Proxies to the agent backend to retrieve saved agent model/provider configuration.
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      503  {object}  envelope
// @Router       /agents/models/config [get]
func (s *Server) handleProviderConfigGet(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Patch model config
// @Description  Proxies to the agent backend to update saved agent model/provider configuration.
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      503  {object}  envelope
// @Router       /agents/models/config [patch]
func (s *Server) handleProviderConfigPatch(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// --- Permissions ---

// @Summary      List pending permissions
// @Description  Proxies to the agent backend to list pending permission requests.
// @Tags         Permission
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /permissions [get]
func (s *Server) handlePermissionList(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Reply to permission
// @Description  Proxies to the agent backend to reply to a pending permission request.
// @Tags         Permission
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Permission ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /permissions/{id}/reply [post]
func (s *Server) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// --- Questions ---

// @Summary      List pending questions
// @Description  Proxies to the agent backend to list pending question requests.
// @Tags         Question
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions [get]
func (s *Server) handleQuestionList(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Reply to question
// @Description  Proxies to the agent backend to reply to a pending question.
// @Tags         Question
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Question ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions/{id}/reply [post]
func (s *Server) handleQuestionReply(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Reject question
// @Description  Proxies to the agent backend to reject a pending question.
// @Tags         Question
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Question ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions/{id}/reject [post]
func (s *Server) handleQuestionReject(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}
