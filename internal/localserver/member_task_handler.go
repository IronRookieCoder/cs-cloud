package localserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"cs-cloud/internal/membertask"
)

func (s *Server) requireLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			writeErr(w, http.StatusForbidden, "loopback_required", "member task transport is only available on loopback")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleMemberTasks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	relative := strings.TrimPrefix(r.URL.Path, "/api/v1/member-tasks")
	relative = strings.Trim(relative, "/")
	if relative == "" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		if err := s.memberTasks.ReconcileAll(ctx); err != nil {
			writeMemberTaskError(w, err)
			return
		}
		tasks, err := s.memberTasks.List(ctx)
		if err != nil {
			writeMemberTaskError(w, err)
			return
		}
		writeOK(w, tasks)
		return
	}
	segments := strings.Split(relative, "/")
	if len(segments) > 2 {
		writeErr(w, http.StatusBadRequest, "invalid_task_key", "member task path is invalid")
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_task_key", "member task key encoding is invalid")
		return
	}
	key, err := membertask.ParseTaskKey(string(raw))
	if err != nil {
		writeMemberTaskError(w, err)
		return
	}
	if err := s.memberTasks.Reconcile(ctx, key); err != nil {
		writeMemberTaskError(w, err)
		return
	}
	if len(segments) == 1 {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		task, err := s.memberTasks.Get(ctx, key)
		if err != nil {
			writeMemberTaskError(w, err)
			return
		}
		writeOK(w, task)
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	s.handleMemberTaskAction(ctx, w, r, key, segments[1])
}

type memberTaskActionRequest struct {
	PreviewID  string                `json:"preview_id"`
	Decision   string                `json:"decision"`
	Reason     string                `json:"reason"`
	DeleteMode membertask.DeleteMode `json:"mode"`
	WorkDir    string                `json:"workdir"`
}

func (s *Server) handleMemberTaskAction(ctx context.Context, w http.ResponseWriter, r *http.Request, key membertask.TaskKey, action string) {
	var request memberTaskActionRequest
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			writeErr(w, http.StatusBadRequest, "invalid_arguments", "request body is invalid")
			return
		}
	}
	var result any
	var err error
	switch action {
	case "handle":
		result, err = s.memberTasks.Handle(ctx, key, membertask.PrepareOptions{WorkDir: request.WorkDir})
	case "submit":
		if request.PreviewID == "" {
			result, err = s.memberTasks.PreviewSubmit(ctx, key)
		} else {
			result, err = s.memberTasks.ConfirmOperation(ctx, key, request.PreviewID)
		}
	case "review":
		if request.PreviewID == "" {
			result, err = s.memberTasks.PreviewReview(ctx, key, request.Decision, request.Reason)
		} else {
			result, err = s.memberTasks.ConfirmOperation(ctx, key, request.PreviewID)
		}
	case "delete":
		if request.PreviewID == "" {
			result, err = s.memberTasks.PreviewDelete(ctx, key, request.DeleteMode)
		} else {
			err = s.memberTasks.ConfirmDelete(ctx, key, request.PreviewID)
			result = map[string]bool{"deleted": err == nil}
		}
	case "recover":
		result, err = s.memberTasks.RecoverOperation(ctx, key)
	default:
		writeErr(w, http.StatusNotFound, "unknown_action", "member task action is not supported")
		return
	}
	if err != nil {
		writeMemberTaskError(w, err)
		return
	}
	writeOK(w, result)
}

func writeMemberTaskError(w http.ResponseWriter, err error) {
	var taskErr *membertask.TaskError
	if !errors.As(err, &taskErr) {
		writeErr(w, http.StatusInternalServerError, "internal_error", "member task operation failed")
		return
	}
	status := http.StatusInternalServerError
	switch taskErr.Code {
	case "invalid_arguments", "invalid_task_key", "invalid_task_role", "invalid_task_state", "invalid_workdir", "unsafe_task_path":
		status = http.StatusBadRequest
	case "authentication_required":
		status = http.StatusUnauthorized
	case "resource_access_denied", "task_not_assigned", "loopback_required":
		status = http.StatusForbidden
	case "task_not_found", "operation_not_found":
		status = http.StatusNotFound
	case "preview_stale", "remote_ref_changed", "operation_ref_conflict", "delete_recovery_required", "force_discard_required", "prepare_location_conflict":
		status = http.StatusConflict
	case "provider_not_supported", "input_material_modified", "local_changes_present", "material_content_unavailable", "repository_access_unavailable", "unsupported_task_store_schema":
		status = http.StatusUnprocessableEntity
	case "cloud_unavailable", "operation_result_unknown":
		status = http.StatusServiceUnavailable
	}
	writeErr(w, status, taskErr.Code, taskErr.Message)
}
