package localserver

import "net/http"

// --- Terminal ---
//
// The terminal routes are implemented on terminal.Handlers, which lives in
// the terminal package and so isn't reachable to swag's *Server scan. These
// wrappers exist solely to carry the @Router annotations for the generated
// swagger spec — each one delegates to the underlying handler without adding
// behavior.

// @Summary      Create terminal
// @Description  Spawns a new terminal session and returns its id + initial dimensions.
// @Tags         Terminal
// @Accept       json
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /terminal [post]
func (s *Server) handleTerminalCreate(w http.ResponseWriter, r *http.Request) {
	s.termH.HandleCreate(w, r)
}

// @Summary      Kill terminal
// @Description  Terminates a terminal session by id.
// @Tags         Terminal
// @Produce      json
// @Param        id   path      string  true  "Terminal ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /terminal/{id} [delete]
func (s *Server) handleTerminalKill(w http.ResponseWriter, r *http.Request) {
	s.termH.HandleKill(w, r)
}

// @Summary      Resize terminal
// @Description  Updates the PTY row/column dimensions for an existing terminal session.
// @Tags         Terminal
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Terminal ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /terminal/{id}/resize [post]
func (s *Server) handleTerminalResize(w http.ResponseWriter, r *http.Request) {
	s.termH.HandleResize(w, r)
}

// @Summary      Restart terminal
// @Description  Restarts the shell process for an existing terminal session without changing its id.
// @Tags         Terminal
// @Produce      json
// @Param        id   path      string  true  "Terminal ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /terminal/{id}/restart [post]
func (s *Server) handleTerminalRestart(w http.ResponseWriter, r *http.Request) {
	s.termH.HandleRestart(w, r)
}

// @Summary      Stream terminal output
// @Description  Opens an SSE/WebSocket stream of terminal output events for an existing session.
// @Tags         Terminal
// @Produce      json
// @Param        id   path      string  true  "Terminal ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /terminal/{id}/stream [get]
func (s *Server) handleTerminalStream(w http.ResponseWriter, r *http.Request) {
	s.termH.HandleStream(w, r)
}

// @Summary      Send terminal input
// @Description  Forwards input bytes to an existing terminal session's PTY.
// @Tags         Terminal
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Terminal ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /terminal/{id}/input [post]
func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	s.termH.HandleInput(w, r)
}

// @Summary      Terminal input WebSocket
// @Description  Upgrade endpoint that turns the connection into a WebSocket used to push input to any active terminal session.
// @Tags         Terminal
// @Success      101  {string}  websocket.WebSocket
// @Failure      503  {object}  envelope
// @Router       /terminal/input-ws [get]
func (s *Server) handleTerminalInputWS(w http.ResponseWriter, r *http.Request) {
	s.inputWsH.ServeHTTP(w, r)
}
